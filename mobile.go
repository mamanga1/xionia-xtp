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

// ── PATCH A ─────────────────────────────────────────────────
// BuildType se setea en tiempo de compilación:
// go build -ldflags "-X main.BuildType=release"
// El build.sh de Android debe incluir ese flag para release builds.
var BuildType = "debug"

// ────────────────────────────────────────────────────────────

// Faros semilla — fallback automático si el principal falla
var seedFaros = []string{
	"190.220.45.26:54321",
	"150.136.55.87:54321",
}

// connectToFaroWithFallback intenta conectar al faro configurado
// y si falla prueba los faros semilla en orden.
func connectToFaroWithFallback() error {
	addr := config.GetFaroAddr()
	faros := []string{}
	if addr != "" {
		faros = append(faros, addr)
	}
	for _, s := range seedFaros {
		if s != addr {
			faros = append(faros, s)
		}
	}
	for _, faro := range faros {
		if err := connectToFaro(faro); err == nil {
			if faro != addr {
				_ = config.SetFaroAddr(faro)
				logf("[FALLBACK] conectado a faro alternativo: %s", faro)
			}
			return nil
		}
	}
	return fmt.Errorf("sin ruta a ningún faro disponible")
}


var dataDir string

func logf(format string, args ...interface{}) {
	if dataDir == "" {
		dataDir = "."
	}
	// PATCH B — permisos 0600 (era 0644)
	f, _ := os.OpenFile(dataDir+"/xionia.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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
	// PATCH C — permisos seguros en el directorio de identidad
	os.Chmod(dataDir, 0700)
	logf("DataDir: %s", dataDir)
}

// ========== UTILIDADES ==========

func stripANSI(s string) string {
	var result []byte
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
		} else {
			result = append(result, s[i])
			i++
		}
	}
	return string(result)
}

func cleanCmd(cmd string) string {
	s := strings.TrimPrefix(cmd, "CHAT:")
	return stripANSI(s)
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
	PubKeyX   []byte
	SharedKey []byte
}

var (
	connMu       sync.Mutex
	globalConn   *net.UDPConn
	globalConnWS *websocket.Conn
	globalUseWS  bool

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

	globalTM *xtp.TransportManager

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

var idMu sync.Mutex

func ensureIdentity() *crypto.Identity {
	if globalID != nil {
		return globalID
	}
	idMu.Lock()
	defer idMu.Unlock()
	if globalID != nil {
		return globalID
	}
	inDataDir(func() {
		id, err := crypto.LoadOrCreateIdentity()
		if err == nil {
			globalID = id
			crypto.SetSelfDID(id.DID)
			// PATCH C — asegurar permisos de node.key al cargar/crear
			os.Chmod(dataDir+"/node.key", 0600)
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

	if err := connectUDP(addr); err == nil {
		return nil
	}
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

	// PATCH A — InsecureSkipVerify solo activo en debug builds.
	// En release (BuildType="release") siempre es false.
	// Para activar en debug: XION_INSECURE_WS=1 (solo desarrollo local).
	insecureSkip := BuildType != "release" && os.Getenv("XION_INSECURE_WS") == "1"

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: insecureSkip},
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
	randBuf := make([]byte, size)
	rand.Read(randBuf)
	padding := make([]byte, size)
	for i := range padding {
		padding[i] = charset[int(randBuf[i])%len(charset)]
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

type faroSenderMobile struct{}

func (f faroSenderMobile) SendToFaro(msg string) error {
	return sendToFaro(msg)
}

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

func pushMsg(displayName, cmd string) {
	fullMsg := fmt.Sprintf("%s: %s", displayName, cleanCmd(cmd))
	recvMu.Lock()
	recvMessages = append(recvMessages, fullMsg)
	if len(recvMessages) > 200 {
		recvMessages = recvMessages[len(recvMessages)-200:]
	}
	recvMu.Unlock()
}

func pushGroupMsg(groupAlias, displayName, text string) {
	fullMsg := fmt.Sprintf("[GRUPO:%s] %s: %s", groupAlias, displayName, stripANSI(text))
	recvMu.Lock()
	recvMessages = append(recvMessages, fullMsg)
	if len(recvMessages) > 200 {
		recvMessages = recvMessages[len(recvMessages)-200:]
	}
	recvMu.Unlock()
}

func runNode(myID *crypto.Identity, quit chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC recuperado en runNode: %v — reintentando", r)
			nodeMu.Lock()
			stillWanted := nodeRunning
			nodeMu.Unlock()
			// PATCH D — backoff inicial antes de reintentar tras panic
			time.Sleep(2 * time.Second)
			if stillWanted {
				go runNode(myID, quit)
			}
		}
	}()

	XioniaReloadACL()

	globalTM = xtp.NewTransportManager(
		myID,
		faroSenderMobile{},
		buildXTPACLIndex(),
		xtp.ManagerCallbacks{
			OnMessage: func(peerDID, displayName, command string) {
				if strings.HasPrefix(command, "GROUP_SYNC:") {
					parts := strings.SplitN(command, ":", 3)
					if len(parts) == 3 {
						alias := parts[1]
						var group crypto.Group
						if json.Unmarshal([]byte(parts[2]), &group) == nil {
							if group.Admin != peerDID {
								logf("[XTP] GROUP_SYNC rechazado: %s no es admin", peerDID)
							} else {
								crypto.SaveGroupDirect(alias, &group)
								logf("[XTP] Grupo '%s' sincronizado (%d miembros)", alias, len(group.Members))
							}
						}
					}
					return
				}
				if strings.HasPrefix(command, "GROUP_DELETE:") {
					parts := strings.SplitN(command, ":", 2)
					if len(parts) == 2 {
						crypto.DeleteGroup(parts[1])
						logf("[XTP] Grupo '%s' eliminado por admin", parts[1])
					}
					return
				}
				if strings.HasPrefix(command, "GROUP_KICKED:") {
					parts := strings.SplitN(command, ":", 2)
					if len(parts) == 2 {
						crypto.RemoveMember(parts[1], myID.DID)
						logf("[XTP] Expulsado del grupo '%s'", parts[1])
					}
					return
				}
				if strings.HasPrefix(command, "GROUP_LEAVE:") {
					parts := strings.SplitN(command, ":", 3)
					if len(parts) == 3 {
						crypto.RemoveMember(parts[1], parts[2])
						logf("[XTP] %s salió del grupo '%s'", parts[2], parts[1])
					}
					return
				}
				// GROUP_JOIN: nuevo miembro manda su ACL al admin
				if strings.HasPrefix(command, "GROUP_JOIN:") {
					parts := strings.SplitN(command, ":", 3)
					if len(parts) == 3 {
						alias := parts[1]
						aclPacket := parts[2] // "acl import did pubEd pubX"
						// Importar ACL del nuevo miembro
						inDataDir(func() {
							lines := strings.Split(aclPacket, "\n")
							for _, line := range lines {
								line = strings.TrimSpace(line)
								if strings.HasPrefix(line, "acl import ") {
									fields := strings.Fields(line)
									if len(fields) >= 5 {
										crypto.AddPeer(fields[2], fields[3], fields[4])
									}
								}
							}
						})
						logf("[XTP] GROUP_JOIN: %s se unió a '%s'", peerDID, alias)
						// Si soy admin, agregar al grupo y distribuir ACLs
						group, ok := crypto.GetGroup(alias)
						if ok && group.Admin == myID.DID {
							crypto.AddMember(alias, peerDID)
							// Distribuir ACL del nuevo a todos los miembros
							for _, memberDID := range group.Members {
								if memberDID == myID.DID || memberDID == peerDID {
									continue
								}
								shareCmd := fmt.Sprintf("GROUP_ACL_SHARE:%s:%s", alias, aclPacket)
								if globalTM != nil {
									globalTM.Send(memberDID, shareCmd)
								}
							}
							// Mandar al nuevo las ACLs de todos los miembros
							acl, err := crypto.LoadACL()
							if err == nil {
								for _, memberDID := range group.Members {
									if memberDID == peerDID {
										continue
									}
									pubEd, pubX, err2 := acl.GetPeerKeys(memberDID)
									if err2 == nil {
										memberACL := fmt.Sprintf("acl import %s %x %x", memberDID, pubEd, pubX)
										shareCmd := fmt.Sprintf("GROUP_ACL_SHARE:%s:%s", alias, memberACL)
										if globalTM != nil {
											globalTM.Send(peerDID, shareCmd)
										}
									}
								}
							}
							// Sync grupo actualizado
							group, _ = crypto.GetGroup(alias)
							if group != nil {
								groupJSON, _ := json.Marshal(group)
								syncCmd := fmt.Sprintf("GROUP_SYNC:%s:%s", alias, string(groupJSON))
								for _, memberDID := range group.Members {
									if memberDID == myID.DID {
										continue
									}
									if globalTM != nil {
										globalTM.Send(memberDID, syncCmd)
									}
								}
							}
						}
					}
					return
				}
				// GROUP_ACL_SHARE: admin distribuye ACL de un miembro
				if strings.HasPrefix(command, "GROUP_ACL_SHARE:") {
					parts := strings.SplitN(command, ":", 3)
					if len(parts) == 3 {
						aclPacket := parts[2]
						inDataDir(func() {
							lines := strings.Split(aclPacket, "\n")
							for _, line := range lines {
								line = strings.TrimSpace(line)
								if strings.HasPrefix(line, "acl import ") {
									fields := strings.Fields(line)
									if len(fields) >= 5 {
										crypto.AddPeer(fields[2], fields[3], fields[4])
									}
								}
							}
						})
						XioniaReloadACL()
						logf("[XTP] GROUP_ACL_SHARE: ACL importada automáticamente")
					}
					return
				}
				if strings.HasPrefix(command, "GROUP:") {
					parts := strings.SplitN(command, ":", 3)
					if len(parts) == 3 {
						pushGroupMsg(parts[1], displayName, parts[2])
					}
					return
				}
				pushMsg(displayName, command)
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

	globalTM.FSM().Send(xtp.EvConnectFaro, nil)
	globalTM.FSM().Send(xtp.EvFaroConnected, nil)
	globalTM.FSM().Send(xtp.EvAnnounceSent, nil)

	sendAnnounce(myID)

	go func() {
		select {
		case <-quit:
			return
		case <-time.After(3 * time.Second):
			sendAnnounce(myID)
		}
	}()

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

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				if stale := staleSince(); stale > 10*time.Second {
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
					// Reintentar hasta 3 veces con espera — la red puede
					// tardar en estar disponible al cambiar WiFi→datos
					reconectado := false
					for intento := 1; intento <= 3; intento++ {
						if err := connectToFaroWithFallback(); err == nil {
							reconectado = true
							break
						}
						logf("[WATCHDOG] intento %d/3 fallo, esperando 3s...", intento)
						time.Sleep(3 * time.Second)
					}
					if reconectado {
						sendAnnounce(myID)
						if globalTM != nil {
							globalTM.FSM().Send(xtp.EvFaroConnected, nil)
							globalTM.FSM().Send(xtp.EvAnnounceSent, nil)
						}
						logf("[WATCHDOG] reconectado y ANNOUNCE enviado")
					} else {
						logf("[WATCHDOG] reconexion fallo a todos los faros tras 3 intentos")
					}
				}
			}
		}
	}()

	// PATCH D — backoff exponencial en el loop principal de reconexión
	reconnectBackoff := 2 * time.Second

	for {
		select {
		case <-quit:
			if globalTM != nil {
				globalTM.Close()
				globalTM = nil
			}
			return
		default:
		}

		raw, err := readFromFaro()
		if err != nil {
			select {
			case <-quit:
				if globalTM != nil {
					globalTM.Close()
					globalTM = nil
				}
				return
			default:
			}
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

			// PATCH D — esperar con backoff exponencial (2s→4s→8s→...→60s)
			logf("[RECONEXION] esperando %v antes de reintentar", reconnectBackoff)
			time.Sleep(reconnectBackoff)
			reconnectBackoff = reconnectBackoff * 2
			if reconnectBackoff > 60*time.Second {
				reconnectBackoff = 60 * time.Second
			}

			if err := connectToFaroWithFallback(); err == nil {
				// Conexión exitosa — resetear backoff
				reconnectBackoff = 2 * time.Second
				logf("[RECONEXION] exitosa, backoff reseteado")
			}
			continue
		}

		// Conexión activa — resetear backoff
		reconnectBackoff = 2 * time.Second
		touchActivity()

		raw = stripPadding(raw)
		raw = extractPayload(raw)

		if globalTM != nil && globalTM.HandleIncoming(raw) {
			continue
		}

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
		pushMsg(displayName, inner.Cmd)
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

	time.Sleep(300 * time.Millisecond)

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
	aclIndexMu.Lock()
	globalACLIdx = nil
	aclIndexMu.Unlock()
	idMu.Lock()
	globalID = nil
	idMu.Unlock()

	lastPublicIP = ""
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

// ========== GRUPOS — FFI ==========

//export XioniaCreateGroup
func XioniaCreateGroup(aliasC, nameC *C.char) *C.char {
	alias := C.GoString(aliasC)
	name := C.GoString(nameC)
	if alias == "" {
		return C.CString("ERROR: alias vacío")
	}
	id := ensureIdentity()
	if id == nil {
		return C.CString("ERROR: sin identidad")
	}
	var result string
	inDataDir(func() {
		if err := crypto.CreateGroup(alias, name, id.DID); err != nil {
			result = "ERROR: " + err.Error()
		} else {
			result = "OK"
		}
	})
	return C.CString(result)
}

//export XioniaGroupSend
func XioniaGroupSend(aliasC, messageC *C.char) *C.char {
	alias := C.GoString(aliasC)
	msg := C.GoString(messageC)
	id := ensureIdentity()
	if id == nil {
		return C.CString("ERROR: sin identidad")
	}

	var group *crypto.Group
	inDataDir(func() {
		g, ok := crypto.GetGroup(alias)
		if ok {
			group = g
		}
	})
	if group == nil {
		return C.CString("ERROR: grupo no encontrado")
	}

	cmd := fmt.Sprintf("GROUP:%s:%s", alias, msg)
	sent := 0
	for _, memberDID := range group.Members {
		if memberDID == id.DID {
			continue
		}
		if globalTM != nil {
			if _, err := globalTM.Send(memberDID, cmd); err == nil {
				sent++
			}
		} else {
			if ExecuteRealCommand(id, memberDID, cmd) == "📤 Enviado" {
				sent++
			}
		}
	}
	return C.CString(fmt.Sprintf("OK:%d", sent))
}

//export XioniaListGroups
func XioniaListGroups() *C.char {
	type GroupInfo struct {
		Alias   string   `json:"alias"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
		Admin   string   `json:"admin"`
	}
	var result string
	inDataDir(func() {
		store, err := crypto.LoadGroups()
		if err != nil || store == nil {
			result = "[]"
			return
		}
		list := make([]GroupInfo, 0, len(store.Groups))
		for alias, g := range store.Groups {
			list = append(list, GroupInfo{
				Alias:   alias,
				Name:    g.Name,
				Members: g.Members,
				Admin:   g.Admin,
			})
		}
		b, _ := json.Marshal(list)
		result = string(b)
	})
	if result == "" {
		result = "[]"
	}
	return C.CString(result)
}

//export XioniaGroupAddMember
func XioniaGroupAddMember(aliasC, targetDIDC *C.char) *C.char {
	alias := C.GoString(aliasC)
	targetDID := C.GoString(targetDIDC)
	id := ensureIdentity()
	if id == nil {
		return C.CString("ERROR: sin identidad")
	}

	var result string
	inDataDir(func() {
		group, ok := crypto.GetGroup(alias)
		if !ok {
			result = "ERROR: grupo no encontrado"
			return
		}
		if group.Admin != id.DID {
			result = "ERROR: solo el admin puede agregar miembros"
			return
		}
		if err := crypto.AddMember(alias, targetDID); err != nil {
			result = "ERROR: " + err.Error()
			return
		}
		group, _ = crypto.GetGroup(alias)
		if group == nil {
			result = "OK"
			return
		}
		groupJSON, err := json.Marshal(group)
		if err == nil {
			syncCmd := fmt.Sprintf("GROUP_SYNC:%s:%s", alias, string(groupJSON))
			if globalTM != nil {
				globalTM.Send(targetDID, syncCmd)
			} else {
				ExecuteRealCommand(id, targetDID, syncCmd)
			}
		}
		result = "OK"
	})
	return C.CString(result)
}

func main() {}
