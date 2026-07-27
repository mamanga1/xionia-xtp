package crypto

import (
"crypto/ed25519"
"crypto/rand"
"encoding/base64"
"encoding/json"
"fmt"
"sync"
"time"

"github.com/mr-tron/base58"
)

// ──────────────────────────────────────────────
// GATE — Puerta soberana del Faro
// Solo entran nodos con did:maia válido.
// El resto: descartado en silencio. Zero logs.
// RAM: ~2 MB con 1000 nodos autorizados.
// ──────────────────────────────────────────────

type Gate struct {
mu       sync.RWMutex
allowed  map[string]*gateEntry
maxNodes int
ttl      time.Duration
}

type gateEntry struct {
DID       string
ExpiresAt time.Time
}

// Handshake es el primer paquete que manda un nodo al faro.
type Handshake struct {
DID   string `json:"did"`
Pub   string `json:"pub"`   // base58(ed25519 pubkey)
TS    int64  `json:"ts"`    // unix seconds
Nonce string `json:"nonce"` // base64(32 bytes random)
Sig   string `json:"sig"`   // base64(ed25519 sign("did|ts|nonce"))
}

func NewGate(maxNodes int, ttl time.Duration) *Gate {
if maxNodes <= 0 {
maxNodes = 500
}
if ttl <= 0 {
ttl = 2 * time.Hour
}
g := &Gate{
allowed:  make(map[string]*gateEntry),
maxNodes: maxNodes,
ttl:      ttl,
}
// Limpieza automática de expirados
go func() {
for range time.Tick(5 * time.Minute) {
g.cleanup()
}
}()
return g
}

// ── LADO CLIENTE ──────────────────────────────

// CreateHandshake genera el paquete de autenticación
// usando el MISMO algoritmo de DID que identity.go
func CreateHandshake(id *Identity) ([]byte, error) {
nonce := make([]byte, 32)
if _, err := rand.Read(nonce); err != nil {
return nil, fmt.Errorf("nonce: %w", err)
}

ts := time.Now().Unix()
nonceB64 := base64.StdEncoding.EncodeToString(nonce)

// Mensaje a firmar: "did:maia:xxxx|1722067200|base64nonce"
msg := fmt.Sprintf("%s|%d|%s", id.DID, ts, nonceB64)

// Firma con la clave privada Ed25519 del nodo
sig := id.SignMessage([]byte(msg))

hs := Handshake{
DID:   id.DID,
Pub:   base58.Encode(id.PubKeyEd),
TS:    ts,
Nonce: nonceB64,
Sig:   base64.StdEncoding.EncodeToString(sig),
}
return json.Marshal(hs)
}

// ── LADO FARO ─────────────────────────────────

// VerifyHandshake valida un handshake entrante.
// Usa el MISMO algoritmo de DID que identity.go.
// Si es válido, autoriza la IP. Si no, retorna error.
func (g *Gate) VerifyHandshake(data []byte, remoteAddr string) (string, error) {
var hs Handshake
if err := json.Unmarshal(data, &hs); err != nil {
return "", fmt.Errorf("json inválido")
}

// 1. Ventana temporal: 60 segundos (anti-replay)
now := time.Now().Unix()
if now-hs.TS > 60 || hs.TS-now > 60 {
return "", fmt.Errorf("ts fuera de ventana")
}

// 2. Nonce: 32 bytes mínimo (anti-bots que mandan basura)
nonceBytes, err := base64.StdEncoding.DecodeString(hs.Nonce)
if err != nil || len(nonceBytes) < 32 {
return "", fmt.Errorf("nonce inválido")
}

// 3. Clave pública: tamaño exacto Ed25519
pubBytes, err := base58.Decode(hs.Pub)
if err != nil {
return "", fmt.Errorf("pubkey base58 inválida")
}
if len(pubBytes) != ed25519.PublicKeySize {
return "", fmt.Errorf("pubkey tamaño inválido")
}
pub := ed25519.PublicKey(pubBytes)

// 4. Verificar firma Ed25519
msg := fmt.Sprintf("%s|%d|%s", hs.DID, hs.TS, hs.Nonce)
sigBytes, err := base64.StdEncoding.DecodeString(hs.Sig)
if err != nil {
return "", fmt.Errorf("sig base64 inválida")
}
if !ed25519.Verify(pub, []byte(msg), sigBytes) {
return "", fmt.Errorf("firma no verifica")
}

// 5. ★ Regenerar el DID desde la clave pública ★
// Mismo algoritmo que identity.go línea 70:
//   fmt.Sprintf("did:maia:%s", base58.Encode(id.PubKeyEd))
expectedDID := fmt.Sprintf("did:maia:%s", base58.Encode(pub))
if hs.DID != expectedDID {
return "", fmt.Errorf("DID no coincide con clave")
}

// 6. Autorizar
g.authorize(remoteAddr, hs.DID)
return hs.DID, nil
}

// IsAllowed — ¿Esta IP ya pasó por el Gate?
func (g *Gate) IsAllowed(addr string) bool {
g.mu.RLock()
defer g.mu.RUnlock()
e, ok := g.allowed[addr]
if !ok {
return false
}
return time.Now().Before(e.ExpiresAt)
}

// GetDID — ¿Qué DID tiene esta IP?
func (g *Gate) GetDID(addr string) string {
g.mu.RLock()
defer g.mu.RUnlock()
e, ok := g.allowed[addr]
if !ok {
return ""
}
return e.DID
}

// Count — ¿Cuántos nodos autorizados hay?
func (g *Gate) Count() int {
g.mu.RLock()
defer g.mu.RUnlock()
return len(g.allowed)
}

func (g *Gate) authorize(addr, did string) {
g.mu.Lock()
defer g.mu.Unlock()
if len(g.allowed) >= g.maxNodes {
g.cleanupLocked()
}
g.allowed[addr] = &gateEntry{
DID:       did,
ExpiresAt: time.Now().Add(g.ttl),
}
}

func (g *Gate) cleanup() {
g.mu.Lock()
defer g.mu.Unlock()
g.cleanupLocked()
}

func (g *Gate) cleanupLocked() {
now := time.Now()
for k, v := range g.allowed {
if now.After(v.ExpiresAt) {
delete(g.allowed, k)
}
}
}
