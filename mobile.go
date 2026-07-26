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
"os"
"strings"
"sync"
"time"
"unsafe"

"github.com/gorilla/websocket"
"xionia-xtp/src/config"
"xionia-xtp/src/crypto"
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
SharedKey []byte
}

var (
globalConn     *net.UDPConn
globalConnWS   *websocket.Conn
globalUseWS    bool
globalQuit     = make(chan struct{})
globalID       *crypto.Identity
nodeRunning    bool
nodeMu         sync.Mutex
recvMu         sync.Mutex
recvMessages   []string
aclIndexMu     sync.RWMutex
globalACLIdx   map[[4]byte]peerKeys
lastPublicIP   string
)

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
if globalConn != nil {
globalConn.Close()
globalConn = nil
}
if globalConnWS != nil {
globalConnWS.Close()
globalConnWS = nil
}
if strings.Contains(addr, ":443") || strings.Contains(addr, ":8443") {
wsURL := fmt.Sprintf("wss://%s/ws", addr)
dialer := websocket.Dialer{
TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
HandshakeTimeout: 5 * time.Second,
}
ws, _, err := dialer.Dial(wsURL, nil)
if err != nil {
return err
}
globalConnWS = ws
globalUseWS = true
return nil
}
udpAddr, err := net.ResolveUDPAddr("udp", addr)
if err != nil {
return err
}
conn, err := net.DialUDP("udp", nil, udpAddr)
if err != nil {
return err
}
globalConn = conn
globalUseWS = false
return nil
}

func sendToFaro(msg string) error {
if globalUseWS && globalConnWS != nil {
return globalConnWS.WriteMessage(websocket.TextMessage, []byte(msg))
}
if globalConn != nil {
_, err := globalConn.Write([]byte(msg))
return err
}
return fmt.Errorf("sin conexion")
}

func readFromFaro() (string, error) {
if globalUseWS && globalConnWS != nil {
globalConnWS.SetReadDeadline(time.Now().Add(15 * time.Second))
_, msg, err := globalConnWS.ReadMessage()
return string(msg), err
}
if globalConn != nil {
buf := make([]byte, 65536)
globalConn.SetReadDeadline(time.Now().Add(15 * time.Second))
n, _, err := globalConn.ReadFromUDP(buf)
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
index[kid] = peerKeys{DID: did, PubKeyEd: pubEd, SharedKey: sharedKey}
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

func handleCommand(cmd string) string {
if strings.HasPrefix(cmd, "PING") || strings.HasPrefix(cmd, "/ping") {
return fmt.Sprintf("✅ PONG | %s", time.Now().Format("15:04:05"))
}
if strings.HasPrefix(cmd, "CHAT:") {
return fmt.Sprintf("✅ Recibido: %s", strings.TrimPrefix(cmd, "CHAT:"))
}
return "✅ ACK"
}

func runNode(myID *crypto.Identity) {
// Carga inicial
XioniaReloadACL()

// ANNOUNCE cada 15s
go func() {
ticker := time.NewTicker(15 * time.Second)
defer ticker.Stop()
for {
select {
case <-globalQuit:
return
case <-ticker.C:
if globalConn == nil && globalConnWS == nil {
continue
}
ts := fmt.Sprintf("%d", time.Now().Unix())
sig := base64.StdEncoding.EncodeToString(myID.SignMessage([]byte(ts)))
msg := fmt.Sprintf("ANNOUNCE %s %s %s", myID.DID, ts, sig)
_ = sendToFaro(addPadding(msg))
}
}
}()

// Listener principal (con reconexión como shell.go)
for {
select {
case <-globalQuit:
return
default:
}

raw, err := readFromFaro()
if err != nil {
// Socket roto: reconectar
if !globalUseWS && globalConn != nil {
globalConn.Close()
globalConn = nil
}
if globalUseWS && globalConnWS != nil {
globalConnWS.Close()
globalConnWS = nil
}
time.Sleep(2 * time.Second)
addr := config.GetFaroAddr()
if addr != "" {
_ = connectToFaro(addr)
}
continue
}

raw = stripPadding(raw)
raw = extractPayload(raw)

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
go runNode(id)
}

// ========== EXPORTADAS ==========

//export XioniaReset
func XioniaReset() {
select {
case <-globalQuit:
globalQuit = make(chan struct{})
default:
close(globalQuit)
globalQuit = make(chan struct{})
}
if globalConn != nil {
globalConn.Close()
globalConn = nil
}
if globalConnWS != nil {
globalConnWS.Close()
globalConnWS = nil
}
nodeRunning = false
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
return C.CString("OK")
}

//export XioniaGetFaroAddr
func XioniaGetFaroAddr() *C.char {
if globalConnWS != nil {
return C.CString(config.GetFaroAddr() + " (WS)")
}
if globalConn != nil {
return C.CString(config.GetFaroAddr() + " (UDP)")
}
return C.CString(config.GetFaroAddr() + " (off)")
}

//export XioniaSendChat
func XioniaSendChat(targetC, msgC *C.char) *C.char {
target := C.GoString(targetC)
msg := C.GoString(msgC)

id := ensureIdentity()
if id == nil {
return C.CString("ERROR: sin identidad")
}

targetDID, _ := crypto.ResolveNode(target)
if targetDID == "" {
targetDID = target
}

result := ExecuteRealCommand(id, targetDID, "CHAT:"+msg)
return C.CString(result)
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
