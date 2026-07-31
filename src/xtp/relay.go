package xtp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xionia-xtp/src/crypto"
)

// PeerKeys contiene las claves de un peer autorizado (del ACL).
type PeerKeys struct {
	DID       string
	PubKeyEd  []byte
	PubKeyX   []byte // ← Clave pública X25519 (para Noise IK)
	SharedKey []byte
}

// InnerPayload es el mensaje antes de cifrar (formato de Fase 1).
type InnerPayload struct {
	FromDID string `json:"from"`
	TS      int64  `json:"ts"`
	Cmd     string `json:"cmd"`
	Sig     string `json:"sig"`
}

type RelayCallbacks struct {
	OnMessage func(peerDID string, displayName string, command string)
	OnError   func(peerDID string, err error)
}

type RelayTransport struct {
	mu sync.RWMutex

	identity *crypto.Identity
	faro     FaroSender

	aclIndex map[[4]byte]PeerKeys
	aclByDID map[string]PeerKeys

	cb RelayCallbacks

	closed bool

	incomingChan chan IncomingMessage
}

type IncomingMessage struct {
	PeerDID     string
	DisplayName string
	Command     string
	Timestamp   int64
}

func NewRelayTransport(identity *crypto.Identity, faro FaroSender, aclIndex map[[4]byte]PeerKeys, cb RelayCallbacks) *RelayTransport {
	aclByDID := make(map[string]PeerKeys, len(aclIndex))
	for _, pk := range aclIndex {
		aclByDID[pk.DID] = pk
	}

	return &RelayTransport{
		identity:     identity,
		faro:         faro,
		aclIndex:     aclIndex,
		aclByDID:     aclByDID,
		cb:           cb,
		incomingChan: make(chan IncomingMessage, 100),
	}
}

func (rt *RelayTransport) Send(peerDID string, command string) error {
	rt.mu.RLock()
	if rt.closed {
		rt.mu.RUnlock()
		return fmt.Errorf("relay transport cerrado")
	}
	peer, exists := rt.aclByDID[peerDID]
	rt.mu.RUnlock()

	if !exists {
		// FIX: no paniquear si peerDID tiene menos de 20 caracteres
		if len(peerDID) > 20 {
			return fmt.Errorf("peer %s no está en el ACL", peerDID[:20]+"...")
		}
		return fmt.Errorf("peer %s no está en el ACL", peerDID)
	}

	inner := InnerPayload{
		FromDID: rt.identity.DID,
		TS:      time.Now().Unix(),
		Cmd:     command,
	}

	innerJSON, _ := json.Marshal(inner)
	inner.Sig = base64.StdEncoding.EncodeToString(
		rt.identity.SignMessage(innerJSON),
	)
	innerJSON, _ = json.Marshal(inner)

	ciphertext, err := crypto.EncryptPayload(peer.SharedKey, innerJSON)
	if err != nil {
		return fmt.Errorf("cifrando payload: %w", err)
	}

	kid := crypto.DeriveKeyID(rt.identity.PubKeyX[:])
	payload := fmt.Sprintf("%s|%s",
		hex.EncodeToString(kid[:]),
		base64.StdEncoding.EncodeToString(ciphertext),
	)

	payload = addRelayPadding(payload)

	relayMsg := fmt.Sprintf("RELAY %s %s %s", peerDID, rt.identity.DID, payload)
	if err := rt.faro.SendToFaro(relayMsg); err != nil {
		return fmt.Errorf("enviando RELAY al faro: %w", err)
	}

	return nil
}

func (rt *RelayTransport) HandleIncoming(raw string) bool {
	rt.mu.RLock()
	if rt.closed {
		rt.mu.RUnlock()
		return false
	}
	aclIndex := rt.aclIndex
	rt.mu.RUnlock()

	parts := strings.SplitN(raw, "|", 2)
	if len(parts) != 2 {
		return false
	}

	kidBytes, err := hex.DecodeString(parts[0])
	if err != nil || len(kidBytes) != 4 {
		return false
	}

	var kid [4]byte
	copy(kid[:], kidBytes)

	peer, exists := aclIndex[kid]
	if !exists {
		return false
	}

	ciphertext, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	plaintext, err := crypto.DecryptPayload(peer.SharedKey, ciphertext)
	if err != nil {
		if rt.cb.OnError != nil {
			rt.cb.OnError(peer.DID, fmt.Errorf("descifrando: %w", err))
		}
		return false
	}

	var inner InnerPayload
	if json.Unmarshal(plaintext, &inner) != nil {
		return false
	}

	innerForVerify := inner
	innerForVerify.Sig = ""
	verifyJSON, _ := json.Marshal(innerForVerify)
	sigBytes, err := base64.StdEncoding.DecodeString(inner.Sig)
	if err != nil {
		return false
	}

	if !crypto.VerifyMessage(peer.PubKeyEd, verifyJSON, sigBytes) {
		if rt.cb.OnError != nil {
			rt.cb.OnError(peer.DID, fmt.Errorf("firma inválida"))
		}
		return false
	}

	if time.Now().Unix()-inner.TS > 60 {
		return false
	}

	displayName := crypto.ResolveDID(peer.DID)

	if rt.cb.OnMessage != nil {
		rt.cb.OnMessage(peer.DID, displayName, inner.Cmd)
	}

	msg := IncomingMessage{
		PeerDID:     peer.DID,
		DisplayName: displayName,
		Command:     inner.Cmd,
		Timestamp:   inner.TS,
	}
	select {
	case rt.incomingChan <- msg:
	default:
	}

	return true
}

func (rt *RelayTransport) Receive(timeout time.Duration) (*IncomingMessage, error) {
	select {
	case msg := <-rt.incomingChan:
		return &msg, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout esperando mensaje relay")
	}
}

func (rt *RelayTransport) UpdateACL(aclIndex map[[4]byte]PeerKeys) {
	aclByDID := make(map[string]PeerKeys, len(aclIndex))
	for _, pk := range aclIndex {
		aclByDID[pk.DID] = pk
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.aclIndex = aclIndex
	rt.aclByDID = aclByDID
}

func (rt *RelayTransport) IsClosed() bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.closed
}

func (rt *RelayTransport) Close() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.closed = true
}

func addRelayPadding(payload string) string {
	randSize := make([]byte, 1)
	rand.Read(randSize)
	size := 50 + int(randSize[0])%151

	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	padding := make([]byte, size)
	for i := range padding {
		randBuf := make([]byte, 1)
		rand.Read(randBuf)
		padding[i] = charset[int(randBuf[0])%len(charset)]
	}

	return fmt.Sprintf("%s|%s", payload, string(padding))
}
