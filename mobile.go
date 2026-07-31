package main

/*
#cgo LDFLAGS: -lm
#include <stdlib.h>
*/
import "C"
import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/mr-tron/base58"
	"xionia-xtp/src/config"
	"xionia-xtp/src/crypto"
	"xionia-xtp/src/xtp"
)

var dataDir string

func logf(format string, args ...interface{}) {
	if dataDir == "" {
		dataDir = "."
	}
	f, _ := os.OpenFile(dataDir+"/xionia.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if f != nil {
		fmt.Fprintf(f, time.Now().Format("15:04:05")+" "+format+"\n", args...)
		f.Close()
	}
}

func inDataDir(fn func()) {
	if dataDir == "" {
		dataDir = "."
		_ = os.MkdirAll(dataDir, 0755)
		_ = os.Chdir(dataDir)
	} else {
		_ = os.Chdir(dataDir)
	}
	fn()
}

//export XioniaSetDataDir
func XioniaSetDataDir(path *C.char) {
	dataDir = C.GoString(path)
	if dataDir == "" {
		dataDir = "."
	}
	_ = os.MkdirAll(dataDir, 0755)
	_ = os.Chdir(dataDir)
	os.Setenv("XION_HOME", dataDir)
	logf("DataDir: %s", dataDir)
}

// ========== MOTOR DE RED ==========
type InnerPayload struct {
	FromDID string `json:"from"`
	TS      int64  `json:"ts"`
	Cmd     string `json:"cmd"`
	Sig     string `json:"sig"`
}

type peerKeys struct {
	DID       string
	PubKeyEd  []byte
	PubKeyX   []byte // ← FIX: mismo bug que session.go — sin esto no hay Noise IK posible
	SharedKey []byte
}

var (
	// connMu protege TODO el estado de la conexión activa (globalConn,
	// globalConnWS, globalUseWS). Se toca desde la goroutine de runNode()
	// Y desde llamadas FFI disparadas por la UI de Flutter en paralelo,
	// así que sin este mutex hay una carrera de datos real.
	connMu       sync.Mutex
	globalConn   *net.UDPConn
	globalConnWS *websocket.Conn
	globalUseWS  bool

	// quitMu protege el ciclo de vida de globalQuit para que Reset() no
	// pueda hacer close() de un canal ya cerrado (panic).
	quitMu     sync.Mutex
	globalQuit = make(chan struct{})

	globalID     *crypto.Identity
	nodeRunning  bool
	nodeMu       sync.Mutex
	recvMu       sync.Mutex
	recvMessages []string
	aclIndexMu   sync.RWMutex
	globalACLIdx map[[4]byte]peerKeys
	lastPublicIP string

	// XTP: Transport Manager (Fase 2). Se crea una sola vez, dentro de
	// runNode(), que solo corre una vez por vida de la app (guardado por
	// nodeMu/nodeRunning). Mismo patrón que shell.go.
	globalTM *xtp.TransportManager

	// lastActivity: última vez que hubo señal de vida real con el Faro
	// (mensaje recibido o ANNOUNCE mandado con éxito). Watchdog puramente
	// en Go, independiente de que algún isolate de Dart esté atento —
	// si el proceso entero se congela (freezer/Doze agresivo) y luego
	// se destraba, esto fuerza una reconexión sin depender de nadie más.
	activityMu   sync.Mutex
	lastActivity time.Time
)

func touchActivity() {
	activityMu.Lock()
	lastActivity = time.Now()
	activityMu.Unlock()
}

func staleSince() time.Duration {
	activityMu.Lock()
	defer activityMu.Unlock()
	if lastActivity.IsZero() {
		return 0
	}
	return time.Since(lastActivity)
}

func ensureIdentity() *crypto.Identity {
	if globalID != nil {
		return globalID
	}
	inDataDir(func() {
		id, err := crypto.LoadOrCreateIdentity()
		if err == nil {
			globalID = id
			crypto.SetSelfDID(id.DID)
		} else {
			logf("ensureIdentity ERROR: %v", err)
		}
	})
	return globalID
}

func connectToFaro(addr string) error {
	logf("Conectando a: %s", addr)

	connMu.Lock()
	if globalConn != nil {
		globalConn.Close()
		globalConn = nil
	}
	if globalConnWS != nil {
		globalConnWS.Close()
		globalConnWS = nil
	}
	connMu.Unlock()

	// 1. UDP primero (default, incluye el propio 443) — con Gate DID.
	if err := connectUDP(addr); err == nil {
		return nil
	}

	// 2. WSS fallback — con Gate DID por headers.
	if err := connectWS(addr); err == nil {
		return nil
	}

	return fmt.Errorf("sin ruta al faro %s", addr)
}

func connectUDP(addr string) error {
	myID := ensureIdentity()
	if myID == nil {
		return fmt.Errorf("sin identidad")
	}

	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return fmt.Errorf("resolviendo UDP: %v", err)
	}
	conn, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		return fmt.Errorf("conectando UDP: %v", err)
	}

	// Handshake del Gate DID: sin esto el faro descarta todo en silencio.
	hs, err := crypto.CreateHandshake(myID)
	if err != nil {
		conn.Close()
		return err
	}
	conn.Write(hs)

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	ack := make([]byte, 1024)
	n, err := conn.Read(ack)
	if err != nil {
		conn.Close()
		return fmt.Errorf("timeout handshake UDP")
	}
	if !strings.Contains(string(ack[:n]), `"ack":"ok"`) {
		conn.Close()
		return fmt.Errorf("handshake UDP rechazado")
	}
	conn.SetReadDeadline(time.Time{})

	connMu.Lock()
	globalConn = conn
	globalUseWS = false
	connMu.Unlock()
	logf("Conectado por UDP (Gate OK): %s", addr)
	return nil
}

func connectWS(addr string) error {
	myID := ensureIdentity()
	if myID == nil {
		return fmt.Errorf("sin identidad")
	}

	wsHost := addr
	if !strings.Contains(wsHost, ":") {
		wsHost += ":443"
	}
	if strings.HasSuffix(wsHost, ":54321") {
		wsHost = strings.TrimSuffix(wsHost, ":54321") + ":443"
	}

	nonce := make([]byte, 32)
	rand.Read(nonce)
	ts := time.Now().Unix()
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)
	msg := fmt.Sprintf("%s|%d|%s", myID.DID, ts, nonceB64)
	sig := myID.SignMessage([]byte(msg))

	headers := http.Header{}
	headers.Set("X-Xionia-DID", myID.DID)
	headers.Set("X-Xionia-Pub", base58.Encode(myID.PubKeyEd))
	headers.Set("X-Xionia-TS", fmt.Sprintf("%d", ts))
	headers.Set("X-Xionia-Nonce", nonceB64)
	headers.Set("X-Xionia-Sig", base64.StdEncoding.EncodeToString(sig))

	wsURL := fmt.Sprintf("wss://%s/ws", wsHost)
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 5 * time.Second,
	}
	ws, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return fmt.Errorf("conectando WS: %v", err)
	}

	connMu.Lock()
	globalConnWS = ws
	globalUseWS = true
	connMu.Unlock()
	logf("Conectado por WSS (Gate OK): %s", wsHost)
	return nil
}

func sendToFaro(msg string) error {
	connMu.Lock()
	useWS := globalUseWS
	connWS := globalConnWS
	conn := globalConn
	connMu.Unlock()

	if useWS && connWS != nil {
		return connWS.WriteMessage(websocket.TextMessage, []byte(msg))
	}
	if conn != nil {
		_, err := conn.Write([]byte(msg))
		return err
	}
	return fmt.Errorf("sin conexion")
}

func readFromFaro() (string, error) {
	connMu.Lock()
	useWS := globalUseWS
	connWS := globalConnWS
	conn := globalConn
	connMu.Unlock()

	if useWS && connWS != nil {
		connWS.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, msg, err := connWS.ReadMessage()
		return string(msg), err
	}
	if conn != nil {
		buf := make([]byte, 65536)
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", fmt.Errorf("sin conexion")
}

func addPadding(payload string) string {
	size := 50 + int(time.Now().UnixNano()%150)
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	padding := make([]byte, size)
	for i := range padding {
		randBuf := make([]byte, 1)
		if _, err := rand.Read(randBuf); err != nil {
			padding[i] = charset[0]
			continue
		}
		padding[i] = charset[int(randBuf[0])%len(charset)]
	}
	return fmt.Sprintf("%s|%s", payload, string(padding))
}

func stripPadding(data string) string {
	if idx := strings.LastIndex(data, "|"); idx != -1 {
		return data[:idx]
	}
	return data
}

func extractPayload(raw string) string {
	if strings.HasPrefix(raw, "RELAY ") {
		fields := strings.Fields(raw)
		if len(fields) >= 4 {
			return fields[3]
		}
	} else if strings.HasPrefix(raw, "RESPONSE ") {
		fields := strings.Fields(raw)
		if len(fields) >= 3 {
			return fields[2]
		}
	} else if strings.HasPrefix(raw, "ACK ") {
		fields := strings.Fields(raw)
		if len(fields) >= 3 {
			return fields[2]
		}
	}
	return raw
}

// XTP: implementa xtp.FaroSender reusando el sendToFaro que ya existe
// (el mismo socket UDP/WS que usa el resto de mobile.go).
type faroSenderMobile struct{}

func (f faroSenderMobile) SendToFaro(msg string) error {
	return sendToFaro(msg)
}

// buildXTPACLIndex: mismo patrón que shell.go — convierte el índice ACL
// local (map[[4]byte]peerKeys) al tipo que espera el paquete xtp.
func buildXTPACLIndex() map[[4]byte]xtp.PeerKeys {
	aclIndexMu.RLock()
	defer aclIndexMu.RUnlock()
	idx := make(map[[4]byte]xtp.PeerKeys, len(globalACLIdx))
	for kid, pk := range globalACLIdx {
		idx[kid] = xtp.PeerKeys{
			DID:       pk.DID,
			PubKeyEd:  pk.PubKeyEd,
			PubKeyX:   pk.PubKeyX,
			SharedKey: pk.SharedKey,
		}
	}
	return idx
}

func buildACLIndex(myID *crypto.Identity) (map[[4]byte]peerKeys, error) {
	acl, err := crypto.LoadACL()
	if err != nil {
		return nil, err
	}
	index := make(map[[4]byte]peerKeys)
	for did, peer := range acl.Peers {
		pubEd, err := hex.DecodeString(peer.PubKeyEd)
		if err != nil {
			logf("buildACLIndex hex PubKeyEd fail %s: %v", did, err)
			continue
		}
		pubX, err := hex.DecodeString(peer.PubKeyX)
		if err != nil {
			logf("buildACLIndex hex PubKeyX fail %s: %v", did, err)
			continue
		}
		sharedKey, err := crypto.DeriveSharedKey(myID.PrivKeyX, pubX)
		if err != nil {
			logf("buildACLIndex derive key fail %s: %v", did, err)
			continue
		}
		kid := crypto.DeriveKeyID(pubX)
		index[kid] = peerKeys{DID: did, PubKeyEd: pubEd, PubKeyX: pubX, SharedKey: sharedKey}
	}
	return index, nil
}

//export XioniaReloadACL
func XioniaReloadACL() {
	id := ensureIdentity()
	if id == nil {
		return
	}
	idx, err := buildACLIndex(id)
	if err != nil {
		logf("ReloadACL error: %v", err)
		return
	}
	aclIndexMu.Lock()
	globalACLIdx = idx
	aclIndexMu.Unlock()
	logf("ReloadACL: %d pares", len(idx))

	// XTP: mantener su copia del ACL sincronizada — si no se hace esto,
	// un contacto agregado después de crear el TransportManager nunca
	// va a poder abrir sesión directa, solo relay.
	if globalTM != nil {
		globalTM.UpdateACL(buildXTPACLIndex())
	}
}

func buildEncryptedPayload(myID *crypto.Identity, sharedKey []byte, inner InnerPayload) (string, error) {
	innerJSON, _ := json.Marshal(inner)
	inner.Sig = base64.StdEncoding.EncodeToString(myID.SignMessage(innerJSON))
	innerJSON, _ = json.Marshal(inner)
	encrypted, err := crypto.EncryptPayload(sharedKey, innerJSON)
	if err != nil {
		return "", err
	}
	kid := crypto.DeriveKeyID(myID.PubKeyX[:])
	return fmt.Sprintf("%s|%s", hex.EncodeToString(kid[:]), base64.StdEncoding.EncodeToString(encrypted)), nil
}

func sendAnnounce(myID *crypto.Identity) {
	connMu.Lock()
	noConn := globalConn == nil && globalConnWS == nil
	connMu.Unlock()
	if noConn {
		return
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := base64.StdEncoding.EncodeToString(myID.SignMessage([]byte(ts)))
	msg := fmt.Sprintf("ANNOUNCE %s %s %s", myID.DID, ts, sig)
	if err := sendToFaro(addPadding(msg)); err != nil {
		logf("sendAnnounce ERROR: %v", err)
	} else {
		logf("ANNOUNCE enviado")
		touchActivity()
	}
}

func runNode(myID *crypto.Identity, quit chan struct{}) {
	// Red de seguridad: en una lib cgo un panic sin recuperar en
	// cualquier goroutine tira abajo el proceso entero de la app
	// (no solo esta goroutine). Sin esto, un bug de red podría
	// crashear XionChat en vez de solo reconectar.
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC recuperado en runNode: %v — reintentando en 2s", r)
			nodeMu.Lock()
			stillWanted := nodeRunning
			nodeMu.Unlock()
			time.Sleep(2 * time.Second)
			if stillWanted {
				go runNode(myID, quit)
			}
		}
	}()

	// Carga inicial
	XioniaReloadACL()

	// XTP: crear el TransportManager una sola vez. runNode() solo corre
	// una vez por vida de la app (lo garantiza nodeMu/nodeRunning en
	// startNodeIfNeeded), así que este es el lugar correcto — mismo
	// patrón que runInteractiveShell() en shell.go.
	globalTM = xtp.NewTransportManager(
		myID,
		faroSenderMobile{},
		buildXTPACLIndex(),
		xtp.ManagerCallbacks{
			// OnMessage empuja al MISMO buffer que ya consume
			// XioniaPollMessages() — Flutter no necesita cambiar nada,
			// los mensajes que lleguen por directo (Noise IK) o por
			// relay entran por el mismo caño hacia la UI.
			OnMessage: func(peerDID, displayName, command string) {
				fullMsg := fmt.Sprintf("%s: %s", displayName, command)
				recvMu.Lock()
				recvMessages = append(recvMessages, fullMsg)
				if len(recvMessages) > 200 {
					recvMessages = recvMessages[len(recvMessages)-200:]
				}
				recvMu.Unlock()
				logf("[XTP MSG %s]: %s", peerDID, command)
			},
			OnDirectSessionActive: func(peerDID string) {
				logf("[XTP] sesión directa activa con %s", peerDID)
			},
			OnDirectSessionLost: func(peerDID string) {
				logf("[XTP] sesión directa perdida con %s", peerDID)
			},
			OnFallbackToRelay: func(peerDID string) {
				logf("[XTP] fallback a relay con %s (hole punching falló)", peerDID)
			},
			OnStateChange: func(from, to xtp.State, event xtp.Event) {
				logf("[XTP] FSM: %s -> %s (%s)", from, to, event)
			},
			OnError: func(context string, err error) {
				logf("[XTP] error en %s: %v", context, err)
			},
		},
		xtp.DefaultManagerConfig(),
	)
	// El faro ya está conectado a esta altura (connectToFaro corrió antes
	// de que startNodeIfNeeded() dispare este runNode), así que avisamos
	// a la FSM de una: mismo orden que shell.go.
	globalTM.FSM().Send(xtp.EvConnectFaro, nil)
	globalTM.FSM().Send(xtp.EvFaroConnected, nil)
	globalTM.FSM().Send(xtp.EvAnnounceSent, nil)

	// Primer ANNOUNCE inmediato (no esperar el ticker) para re-punchear
	// el NAT lo antes posible al conectar/reconectar.
	sendAnnounce(myID)

	// Retry a los 3s: el primer ANNOUNCE a veces sale antes de que el
	// faro termine de procesar el handshake del Gate ("hay que
	// saludarse dos veces"). Mismo fix que shell.go.
	go func() {
		select {
		case <-quit:
			return
		case <-time.After(3 * time.Second):
			sendAnnounce(myID)
		}
	}()

	// ANNOUNCE cada 10s
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				sendAnnounce(myID)
			}
		}
	}()

	// Watchdog: si pasan 20s sin ninguna señal de vida (ni recibimos
	// nada del Faro ni logramos mandar un ANNOUNCE), fuerza reconexión
	// ya mismo. No depende de que el read loop haga timeout por su
	// cuenta (eso tarda hasta 15s por ciclo) ni de que algún isolate de
	// Dart esté atento — es puramente Go, sobrevive a que el proceso se
	// haya congelado y recién ahora vuelva a correr.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				if stale := staleSince(); stale > 20*time.Second {
					logf("[WATCHDOG] sin actividad hace %v, forzando reconexion", stale)
					connMu.Lock()
					if globalConn != nil {
						globalConn.Close()
						globalConn = nil
					}
					if globalConnWS != nil {
						globalConnWS.Close()
						globalConnWS = nil
					}
					connMu.Unlock()
					addr := config.GetFaroAddr()
					if addr != "" {
						if err := connectToFaro(addr); err == nil {
							sendAnnounce(myID)
							if globalTM != nil {
								globalTM.FSM().Send(xtp.EvFaroConnected, nil)
								globalTM.FSM().Send(xtp.EvAnnounceSent, nil)
							}
							logf("[WATCHDOG] reconectado y ANNOUNCE enviado")
						} else {
							logf("[WATCHDOG] reconexion fallo: %v", err)
						}
					}
				}
			}
		}
	}()

	// Listener principal (con reconexión como shell.go)
	for {
		select {
		case <-quit:
			return
		default:
		}

		raw, err := readFromFaro()
		if err != nil {
			select {
			case <-quit:
				return
			default:
			}
			// Socket roto o timeout: reconectar
			connMu.Lock()
			if !globalUseWS && globalConn != nil {
				globalConn.Close()
				globalConn = nil
			}
			if globalUseWS && globalConnWS != nil {
				globalConnWS.Close()
				globalConnWS = nil
			}
			connMu.Unlock()
			time.Sleep(2 * time.Second)
			addr := config.GetFaroAddr()
			if addr != "" {
				_ = connectToFaro(addr)
			}
			continue
		}

		// Cualquier paquete recibido, sea lo que sea, prueba que el
		// camino de red funciona — alimenta al watchdog.
		touchActivity()

		raw = stripPadding(raw)
		raw = extractPayload(raw)

		// XTP: rutear al TransportManager (signaling de sesión directa
		// o mensaje por relay ya envuelto). Mismo orden que shell.go:
		// después de stripPadding/extractPayload, antes de ACK_IP.
		if globalTM != nil && globalTM.HandleIncoming(raw) {
			continue
		}

		// ACK_IP: roaming
		if strings.HasPrefix(raw, "ACK_IP ") {
			parts := strings.SplitN(raw, " ", 2)
			if len(parts) == 2 {
				currentPublicIP := parts[1]
				if lastPublicIP != "" && lastPublicIP != currentPublicIP {
					logf("IP pública cambió: %s → %s", lastPublicIP, currentPublicIP)
					ts := fmt.Sprintf("%d", time.Now().Unix())
					sig := base64.StdEncoding.EncodeToString(myID.SignMessage([]byte(ts)))
					msg := fmt.Sprintf("ANNOUNCE %s %s %s", myID.DID, ts, sig)
					_ = sendToFaro(addPadding(msg))
				}
				lastPublicIP = currentPublicIP
			}
			continue
		}

		if strings.HasPrefix(raw, "ACK") {
			continue
		}

		parts := strings.SplitN(raw, "|", 2)
		if len(parts) != 2 {
			continue
		}

		kidBytes, err := hex.DecodeString(parts[0])
		if err != nil || len(kidBytes) != 4 {
			continue
		}
		var kid [4]byte
		copy(kid[:], kidBytes)

		aclIndexMu.RLock()
		peer, exists := globalACLIdx[kid]
		aclIndexMu.RUnlock()

		if !exists {
			logf("[NODO] Peer KID %x no encontrado en ACL", kid)
			continue
		}

		ciphertext, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		plaintext, err := crypto.DecryptPayload(peer.SharedKey, ciphertext)
		if err != nil {
			logf("[NODO] Decrypt fail from %s: %v", peer.DID, err)
			continue
		}

		var inner InnerPayload
		if json.Unmarshal(plaintext, &inner) != nil {
			continue
		}

		innerForVerify := inner
		innerForVerify.Sig = ""
		verifyJSON, _ := json.Marshal(innerForVerify)
		sigBytes, err := base64.StdEncoding.DecodeString(inner.Sig)
		if err != nil || !crypto.VerifyMessage(peer.PubKeyEd, verifyJSON, sigBytes) {
			logf("[NODO] Firma inválida de %s", peer.DID)
			continue
		}
		if time.Now().Unix()-inner.TS > 60 {
			continue
		}

		displayName := crypto.ResolveDID(peer.DID)
		fullMsg := fmt.Sprintf("%s: %s", displayName, inner.Cmd)

		recvMu.Lock()
		recvMessages = append(recvMessages, fullMsg)
		if len(recvMessages) > 200 {
			recvMessages = recvMessages[len(recvMessages)-200:]
		}
		recvMu.Unlock()

		logf("[MSG %s]: %s", peer.DID, inner.Cmd)
	}
}

func ExecuteRealCommand(myID *crypto.Identity, targetDID, command string) string {
	acl, err := crypto.LoadACL()
	if err != nil {
		return fmt.Sprintf("❌ ACL: %v", err)
	}
	_, pubX, err := acl.GetPeerKeys(targetDID)
	if err != nil {
		return "❌ DID no encontrado"
	}
	sharedKey, err := crypto.DeriveSharedKey(myID.PrivKeyX, pubX)
	if err != nil {
		return fmt.Sprintf("❌ Clave: %v", err)
	}
	inner := InnerPayload{
		FromDID: myID.DID,
		TS:      time.Now().Unix(),
		Cmd:     command,
	}
	payload, err := buildEncryptedPayload(myID, sharedKey, inner)
	if err != nil {
		return fmt.Sprintf("❌ Cifrado: %v", err)
	}
	relayCmd := fmt.Sprintf("RELAY %s %s %s", targetDID, myID.DID, addPadding(payload))
	if err := sendToFaro(relayCmd); err != nil {
		return fmt.Sprintf("❌ Envio: %v", err)
	}
	return "📤 Enviado"
}

func startNodeIfNeeded() {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if nodeRunning {
		return
	}
	id := ensureIdentity()
	if id == nil {
		return
	}
	nodeRunning = true

	quitMu.Lock()
	quit := globalQuit
	quitMu.Unlock()

	go runNode(id, quit)
}

// ========== EXPORTADAS ==========

//export XioniaReset
func XioniaReset() {
	nodeMu.Lock()
	quitMu.Lock()
	close(globalQuit)
	globalQuit = make(chan struct{})
	quitMu.Unlock()
	nodeRunning = false
	nodeMu.Unlock()

	if globalTM != nil {
		globalTM.Close()
		globalTM = nil
	}

	connMu.Lock()
	if globalConn != nil {
		globalConn.Close()
		globalConn = nil
	}
	if globalConnWS != nil {
		globalConnWS.Close()
		globalConnWS = nil
	}
	connMu.Unlock()

	inDataDir(func() {
		crypto.ClearACL()
		if aliases, err := crypto.LoadAliases(); err == nil {
			for name := range aliases.List() {
				aliases.Remove(name)
			}
			aliases.Save()
		}
		config.ResetFaroAddr()
		os.RemoveAll(".xion")
		os.Remove("node.key")
		os.Remove("acl.json")
		os.Remove("aliases.json")
	})
	globalID = nil
}

//export XioniaGetMyIdentity
func XioniaGetMyIdentity() *C.char {
	var result string
	inDataDir(func() {
		id, err := crypto.LoadOrCreateIdentity()
		if err != nil {
			result = "ERROR: " + err.Error()
			return
		}
		pubEdHex := hex.EncodeToString(id.PubKeyEd)
		pubXHex := hex.EncodeToString(id.PubKeyX[:])
		result = fmt.Sprintf("DID: %s\nPubEd: %s\nPubX: %s", id.DID, pubEdHex, pubXHex)
	})
	return C.CString(result)
}

//export XioniaExportACLPacket
func XioniaExportACLPacket() *C.char {
	var result string
	inDataDir(func() {
		id, err := crypto.LoadOrCreateIdentity()
		if err != nil {
			return
		}
		pubEdHex := hex.EncodeToString(id.PubKeyEd)
		pubXHex := hex.EncodeToString(id.PubKeyX[:])
		result = fmt.Sprintf("acl import %s %s %s", id.DID, pubEdHex, pubXHex)
	})
	return C.CString(result)
}

//export XioniaImportACLPacket
func XioniaImportACLPacket(packetC *C.char) {
	packetStr := C.GoString(packetC)
	inDataDir(func() {
		lines := strings.Split(packetStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "acl import ") {
				fields := strings.Fields(line)
				if len(fields) >= 5 {
					crypto.AddPeer(fields[2], fields[3], fields[4])
				}
			} else if strings.HasPrefix(line, "alias add ") {
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					crypto.AddAlias(fields[2], fields[3])
				}
			}
		}
	})
}

//export XioniaSetAlias
func XioniaSetAlias(didC, aliasC *C.char) {
	did := C.GoString(didC)
	alias := C.GoString(aliasC)
	inDataDir(func() {
		crypto.AddAlias(alias, did)
	})
}

//export XioniaRemoveAlias
func XioniaRemoveAlias(didC *C.char) {
	did := C.GoString(didC)
	inDataDir(func() {
		if aliases, err := crypto.LoadAliases(); err == nil {
			aliases.Remove(did)
			aliases.Save()
		}
	})
}

//export XioniaListAliases
func XioniaListAliases() *C.char {
	var result string
	inDataDir(func() {
		aliases, err := crypto.LoadAliases()
		if err != nil {
			result = "[]"
			return
		}
		type AliasEntry struct {
			Alias string `json:"alias"`
			DID   string `json:"did"`
		}
		list := make([]AliasEntry, 0)
		for name, did := range aliases.List() {
			list = append(list, AliasEntry{Alias: name, DID: did})
		}
		jsonBytes, _ := json.Marshal(list)
		result = string(jsonBytes)
	})
	return C.CString(result)
}

//export XioniaConnectFaro
func XioniaConnectFaro(addrC *C.char) *C.char {
	addr := C.GoString(addrC)
	logf("ConnectFaro: %s", addr)

	inDataDir(func() {
		_ = config.SetFaroAddr(addr)
	})

	if err := connectToFaro(addr); err != nil {
		logf("ConnectFaro ERROR: %v", err)
		return C.CString("ERROR: " + err.Error())
	}

	startNodeIfNeeded()

	// Si el nodo ya estaba corriendo (ej: la app vuelve de background y
	// Flutter llama ConnectFaro de nuevo para forzar reconexión), el
	// socket se reabrió arriba pero el goroutine de announce sigue con
	// su propio timer de 15s. Mandamos uno ya mismo para no esperar.
	if id := ensureIdentity(); id != nil {
		go sendAnnounce(id)
	}
	return C.CString("OK")
}

//export XioniaGetFaroAddr
func XioniaGetFaroAddr() *C.char {
	connMu.Lock()
	hasWS := globalConnWS != nil
	hasUDP := globalConn != nil
	connMu.Unlock()

	if hasWS {
		return C.CString(config.GetFaroAddr() + " (WS)")
	}
	if hasUDP {
		return C.CString(config.GetFaroAddr() + " (UDP)")
	}
	return C.CString(config.GetFaroAddr() + " (off)")
}

// sendChatCommon: usado tanto por XioniaSendChat como por XioniaSendXTP.
// Si el TransportManager ya está armado, lo usa (elige directo Noise IK
// o relay automáticamente); si no, cae al camino legacy de siempre.
func sendChatCommon(target, msg string) string {
	id := ensureIdentity()
	if id == nil {
		return "ERROR: sin identidad"
	}

	targetDID, _ := crypto.ResolveNode(target)
	if targetDID == "" {
		targetDID = target
	}

	if globalTM != nil {
		transport, err := globalTM.Send(targetDID, "CHAT:"+msg)
		if err != nil {
			return fmt.Sprintf("❌ Error XTP: %v", err)
		}
		return fmt.Sprintf("📤 Enviado (%s)", transport)
	}

	return ExecuteRealCommand(id, targetDID, "CHAT:"+msg)
}

//export XioniaSendChat
func XioniaSendChat(targetC, msgC *C.char) *C.char {
	target := C.GoString(targetC)
	msg := C.GoString(msgC)
	return C.CString(sendChatCommon(target, msg))
}

//export XioniaSendXTP
func XioniaSendXTP(targetC, msgC *C.char) *C.char {
	target := C.GoString(targetC)
	msg := C.GoString(msgC)
	return C.CString(sendChatCommon(target, msg))
}

//export XioniaGetMyDID
func XioniaGetMyDID() *C.char {
	var result string
	inDataDir(func() {
		id, err := crypto.LoadOrCreateIdentity()
		if err != nil {
			result = "ERROR: " + err.Error()
			return
		}
		result = id.DID
	})
	return C.CString(result)
}

//export XioniaGetContactsJSON
func XioniaGetContactsJSON() *C.char {
	var result string
	inDataDir(func() {
		acl, err := crypto.LoadACL()
		if err != nil {
			result = "[]"
			return
		}
		type Contact struct {
			DID   string `json:"did"`
			Alias string `json:"alias"`
		}
		contacts := make([]Contact, 0)
		for did := range acl.Peers {
			alias := crypto.ResolveDIDToAlias(did)
			contacts = append(contacts, Contact{DID: did, Alias: alias})
		}
		jsonBytes, _ := json.Marshal(contacts)
		result = string(jsonBytes)
	})
	return C.CString(result)
}

//export XioniaPollMessages
func XioniaPollMessages() *C.char {
	recvMu.Lock()
	defer recvMu.Unlock()
	if len(recvMessages) == 0 {
		return C.CString("[]")
	}
	jsonBytes, _ := json.Marshal(recvMessages)
	recvMessages = nil
	return C.CString(string(jsonBytes))
}

//export XioniaFreeString
func XioniaFreeString(s *C.char) {
	C.free(unsafe.Pointer(s))
}

func main() {}
