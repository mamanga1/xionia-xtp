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
	nonces   map[string]int64
	quit     chan struct{}
}

type gateEntry struct {
	DID       string
	ExpiresAt time.Time
}

// Handshake es el primer paquete que manda un nodo al faro.
type Handshake struct {
	DID   string `json:"did"`
	Pub   string `json:"pub"`
	TS    int64  `json:"ts"`
	Nonce string `json:"nonce"`
	Sig   string `json:"sig"`
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
		nonces:   make(map[string]int64),
		quit:     make(chan struct{}),
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-g.quit:
				return
			case <-ticker.C:
				g.cleanup()
			}
		}
	}()
	return g
}

// Close detiene la goroutine de limpieza del Gate.
func (g *Gate) Close() {
	select {
	case <-g.quit:
	default:
		close(g.quit)
	}
}

// ── LADO CLIENTE ──────────────────────────────

func CreateHandshake(id *Identity) ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	ts := time.Now().Unix()
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)
	msg := fmt.Sprintf("%s|%d|%s", id.DID, ts, nonceB64)
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

func (g *Gate) VerifyHandshake(data []byte, remoteAddr string) (string, error) {
	var hs Handshake
	if err := json.Unmarshal(data, &hs); err != nil {
		return "", fmt.Errorf("json inválido")
	}

	now := time.Now().Unix()
	if now-hs.TS > 60 || hs.TS-now > 60 {
		return "", fmt.Errorf("ts fuera de ventana")
	}

	nonceBytes, err := base64.StdEncoding.DecodeString(hs.Nonce)
	if err != nil || len(nonceBytes) < 32 {
		return "", fmt.Errorf("nonce inválido")
	}

	// Anti-replay: verificar que el nonce no fue usado
	g.mu.RLock()
	_, nonceUsed := g.nonces[hs.Nonce]
	g.mu.RUnlock()
	if nonceUsed {
		return "", fmt.Errorf("nonce reutilizado (replay)")
	}

	pubBytes, err := base58.Decode(hs.Pub)
	if err != nil {
		return "", fmt.Errorf("pubkey base58 inválida")
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return "", fmt.Errorf("pubkey tamaño inválido")
	}
	pub := ed25519.PublicKey(pubBytes)

	msg := fmt.Sprintf("%s|%d|%s", hs.DID, hs.TS, hs.Nonce)
	sigBytes, err := base64.StdEncoding.DecodeString(hs.Sig)
	if err != nil {
		return "", fmt.Errorf("sig base64 inválida")
	}
	if !ed25519.Verify(pub, []byte(msg), sigBytes) {
		return "", fmt.Errorf("firma no verifica")
	}

	expectedDID := fmt.Sprintf("did:maia:%s", base58.Encode(pub))
	if hs.DID != expectedDID {
		return "", fmt.Errorf("DID no coincide con clave")
	}

	// Nonce válido: registrar para anti-replay
	g.mu.Lock()
	g.nonces[hs.Nonce] = now + 60
	g.mu.Unlock()

	g.authorize(remoteAddr, hs.DID)
	return hs.DID, nil
}

func (g *Gate) IsAllowed(addr string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	e, ok := g.allowed[addr]
	if !ok {
		return false
	}
	return time.Now().Before(e.ExpiresAt)
}

func (g *Gate) GetDID(addr string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	e, ok := g.allowed[addr]
	if !ok {
		return ""
	}
	return e.DID
}

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
	nowUnix := now.Unix()
	for nonce, exp := range g.nonces {
		if nowUnix > exp {
			delete(g.nonces, nonce)
		}
	}
}
