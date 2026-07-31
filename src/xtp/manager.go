package xtp

import (
	"fmt"
	"net"
	"sync"

	"xionia-xtp/src/crypto"
)

type ManagerCallbacks struct {
	OnMessage             func(peerDID string, displayName string, command string)
	OnDirectSessionActive func(peerDID string)
	OnDirectSessionLost   func(peerDID string)
	OnFallbackToRelay     func(peerDID string)
	OnStateChange         func(from, to State, event Event)
	OnError               func(context string, err error)
}

type TransportManager struct {
	mu sync.RWMutex

	identity *crypto.Identity
	faro     FaroSender
	fsm      *FSM

	relay  *RelayTransport
	direct map[string]*DirectTransport

	aclIndex map[[4]byte]PeerKeys
	aclByDID map[string]PeerKeys

	cb ManagerCallbacks

	closed bool

	autoDirect bool
}

type ManagerConfig struct {
	AutoDirect bool
}

func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		AutoDirect: true,
	}
}

func NewTransportManager(
	identity *crypto.Identity,
	faro FaroSender,
	aclIndex map[[4]byte]PeerKeys,
	cb ManagerCallbacks,
	config ManagerConfig,
) *TransportManager {
	aclByDID := make(map[string]PeerKeys, len(aclIndex))
	for _, pk := range aclIndex {
		aclByDID[pk.DID] = pk
	}

	fsm := NewFSM()

	relayCb := RelayCallbacks{
		OnMessage: func(peerDID, displayName, command string) {
			if cb.OnMessage != nil {
				cb.OnMessage(peerDID, displayName, command)
			}
		},
		OnError: func(peerDID string, err error) {
			if cb.OnError != nil {
				cb.OnError("relay:"+peerDID[:15], err)
			}
		},
	}
	relay := NewRelayTransport(identity, faro, aclIndex, relayCb)

	tm := &TransportManager{
		identity:   identity,
		faro:       faro,
		fsm:        fsm,
		relay:      relay,
		direct:     make(map[string]*DirectTransport),
		aclIndex:   aclIndex,
		aclByDID:   aclByDID,
		cb:         cb,
		autoDirect: config.AutoDirect,
	}

	fsm.OnEnter(Direct, func(from, to State, event Event, meta map[string]interface{}) {
		if cb.OnStateChange != nil {
			cb.OnStateChange(from, to, event)
		}
	})
	fsm.OnEnter(RelayFallback, func(from, to State, event Event, meta map[string]interface{}) {
		if cb.OnStateChange != nil {
			cb.OnStateChange(from, to, event)
		}
	})

	return tm
}

func (tm *TransportManager) Send(peerDID string, command string) (transport string, err error) {
	tm.mu.RLock()
	if tm.closed {
		tm.mu.RUnlock()
		return "", fmt.Errorf("transport manager cerrado")
	}
	direct, hasDirect := tm.direct[peerDID]
	autoDirect := tm.autoDirect
	tm.mu.RUnlock()

	if hasDirect && direct.IsActive() {
		if err := direct.Send([]byte(command)); err != nil {
			if tm.cb.OnError != nil {
				tm.cb.OnError("direct:"+peerDID[:15], fmt.Errorf("envío directo falló, cayendo a relay: %w", err))
			}
		} else {
			return "direct", nil
		}
	}

	if autoDirect && !hasDirect {
		peer, hasPeer := tm.getPeerKeys(peerDID)
		if hasPeer {
			// ← FIX: Usar PubKeyX (X25519), NO PubKeyEd (Ed25519)
			if len(peer.PubKeyX) != 32 {
				if tm.cb.OnError != nil {
					tm.cb.OnError("xtp:"+peerDID[:15], fmt.Errorf("peer sin PubKeyX válida (len=%d)", len(peer.PubKeyX)))
				}
			} else {
				peerPubX := new([32]byte)
				copy(peerPubX[:], peer.PubKeyX[:32])

				go tm.tryDirectSession(peerDID, peerPubX)
			}

			if err := tm.relay.Send(peerDID, command); err != nil {
				return "", fmt.Errorf("enviando por relay: %w", err)
			}
			return "relay", nil
		}
	}

	if err := tm.relay.Send(peerDID, command); err != nil {
		return "", fmt.Errorf("enviando por relay: %w", err)
	}
	return "relay", nil
}

func (tm *TransportManager) tryDirectSession(peerDID string, peerPubX *[32]byte) {
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		return
	}
	if _, exists := tm.direct[peerDID]; exists {
		tm.mu.Unlock()
		return
	}

	dtCb := DirectCallbacks{
		OnPunchComplete: func(peerDID string, peerAddr *net.UDPAddr) {
			fmt.Printf("[XTP-MGR] 👊 Hole punching exitoso con %s\n", peerDID[:20]+"...")
		},
		OnSessionActive: func(peerDID string) {
			fmt.Printf("[XTP-MGR] 🔐 Sesión directa activa con %s\n", peerDID[:20]+"...")
			if tm.cb.OnDirectSessionActive != nil {
				tm.cb.OnDirectSessionActive(peerDID)
			}
		},
		OnMessage: func(peerDID string, plaintext []byte) {
			displayName := crypto.ResolveDID(peerDID)
			if tm.cb.OnMessage != nil {
				tm.cb.OnMessage(peerDID, displayName, string(plaintext))
			}
		},
		OnSessionLost: func(peerDID string) {
			fmt.Printf("[XTP-MGR] 💀 Sesión directa perdida con %s\n", peerDID[:20]+"...")
			if tm.cb.OnDirectSessionLost != nil {
				tm.cb.OnDirectSessionLost(peerDID)
			}
			tm.mu.Lock()
			delete(tm.direct, peerDID)
			tm.mu.Unlock()
		},
		OnFallbackToRelay: func(peerDID string) {
			fmt.Printf("[XTP-MGR] 🔄 Fallback a relay con %s\n", peerDID[:20]+"...")
			if tm.cb.OnFallbackToRelay != nil {
				tm.cb.OnFallbackToRelay(peerDID)
			}
			tm.mu.Lock()
			delete(tm.direct, peerDID)
			tm.mu.Unlock()
		},
		OnClose: func(peerDID string) {
			tm.mu.Lock()
			delete(tm.direct, peerDID)
			tm.mu.Unlock()
		},
	}

	dt := NewDirectTransport(tm.identity.DID, tm.fsm, tm.faro, dtCb)
	dt.SetIdentity(tm.identity)
	tm.direct[peerDID] = dt
	tm.mu.Unlock()

	if err := dt.OpenSession(peerDID, peerPubX); err != nil {
		fmt.Printf("[XTP-MGR] ❌ Sesión directa falló con %s: %v\n", peerDID[:20]+"...", err)
		tm.mu.Lock()
		delete(tm.direct, peerDID)
		tm.mu.Unlock()
	}
}

func (tm *TransportManager) HandleIncoming(raw string) bool {
	tm.mu.RLock()
	if tm.closed {
		tm.mu.RUnlock()
		return false
	}
	tm.mu.RUnlock()

	if tm.isFaroSignal(raw) {
		return tm.handleFaroSignal(raw)
	}

	return tm.relay.HandleIncoming(raw)
}

func (tm *TransportManager) isFaroSignal(raw string) bool {
	signalPrefixes := []string{
		"SESSION_INFO ",
		"SESSION_INCOMING ",
		"PUNCH_NOW ",
		"SESSION_REDIRECT ",
		"SESSION_ERROR ",
	}
	for _, prefix := range signalPrefixes {
		if len(raw) >= len(prefix) && raw[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (tm *TransportManager) handleFaroSignal(raw string) bool {
	var signalType string
	if len(raw) >= 13 && raw[:13] == "SESSION_INFO " {
		signalType = "SESSION_INFO"
	} else if len(raw) >= 17 && raw[:17] == "SESSION_INCOMING " {
		signalType = "SESSION_INCOMING"
	} else if len(raw) >= 10 && raw[:10] == "PUNCH_NOW " {
		signalType = "PUNCH_NOW"
	} else if len(raw) >= 17 && raw[:17] == "SESSION_REDIRECT " {
		signalType = "SESSION_REDIRECT"
	} else if len(raw) >= 14 && raw[:14] == "SESSION_ERROR " {
		signalType = "SESSION_ERROR"
	} else {
		return false
	}

	if signalType == "SESSION_INCOMING" {
		parts := splitFields(raw)
		if len(parts) < 2 {
			return false
		}
		senderDID := parts[1]

		tm.mu.Lock()
		dt, exists := tm.direct[senderDID]
		if !exists {
			dtCb := DirectCallbacks{
				OnSessionActive: func(peerDID string) {
					if tm.cb.OnDirectSessionActive != nil {
						tm.cb.OnDirectSessionActive(peerDID)
					}
				},
				OnMessage: func(peerDID string, plaintext []byte) {
					displayName := crypto.ResolveDID(peerDID)
					if tm.cb.OnMessage != nil {
						tm.cb.OnMessage(peerDID, displayName, string(plaintext))
					}
				},
				OnSessionLost: func(peerDID string) {
					if tm.cb.OnDirectSessionLost != nil {
						tm.cb.OnDirectSessionLost(peerDID)
					}
					tm.mu.Lock()
					delete(tm.direct, peerDID)
					tm.mu.Unlock()
				},
				OnFallbackToRelay: func(peerDID string) {
					if tm.cb.OnFallbackToRelay != nil {
						tm.cb.OnFallbackToRelay(peerDID)
					}
					tm.mu.Lock()
					delete(tm.direct, peerDID)
					tm.mu.Unlock()
				},
				OnClose: func(peerDID string) {
					tm.mu.Lock()
					delete(tm.direct, peerDID)
					tm.mu.Unlock()
				},
			}
			dt = NewDirectTransport(tm.identity.DID, tm.fsm, tm.faro, dtCb)
			dt.SetIdentity(tm.identity)
			tm.direct[senderDID] = dt
		}
		tm.mu.Unlock()

		if err := dt.HandleIncomingSession(raw); err != nil {
			if tm.cb.OnError != nil {
				tm.cb.OnError("session_incoming:"+senderDID[:15], err)
			}
			tm.mu.Lock()
			delete(tm.direct, senderDID)
			tm.mu.Unlock()
		}
		return true
	}

	parts := splitFields(raw)
	if len(parts) < 2 {
		return false
	}
	peerDID := parts[1]

	tm.mu.RLock()
	dt, exists := tm.direct[peerDID]
	tm.mu.RUnlock()

	if exists {
		select {
		case dt.FaroMessages <- FaroSignal{Type: signalType, Raw: raw}:
		default:
		}
		return true
	}

	return false
}

func (tm *TransportManager) UpdateACL(aclIndex map[[4]byte]PeerKeys) {
	aclByDID := make(map[string]PeerKeys, len(aclIndex))
	for _, pk := range aclIndex {
		aclByDID[pk.DID] = pk
	}

	tm.mu.Lock()
	tm.aclIndex = aclIndex
	tm.aclByDID = aclByDID
	tm.mu.Unlock()

	tm.relay.UpdateACL(aclIndex)
}

func (tm *TransportManager) IsDirectActive(peerDID string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	dt, exists := tm.direct[peerDID]
	return exists && dt.IsActive()
}

func (tm *TransportManager) ActiveDirectSessions() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	count := 0
	for _, dt := range tm.direct {
		if dt.IsActive() {
			count++
		}
	}
	return count
}

func (tm *TransportManager) FSM() *FSM {
	return tm.fsm
}

type TransportStats struct {
	DirectSessions int    `json:"direct_sessions"`
	FSMState       string `json:"fsm_state"`
	RelayClosed    bool   `json:"relay_closed"`
}

func (tm *TransportManager) Stats() TransportStats {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return TransportStats{
		DirectSessions: len(tm.direct),
		FSMState:       tm.fsm.Current().String(),
		RelayClosed:    tm.relay.IsClosed(),
	}
}

func (tm *TransportManager) Close() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.closed {
		return
	}
	tm.closed = true

	for peerDID, dt := range tm.direct {
		dt.Close()
		delete(tm.direct, peerDID)
	}

	tm.relay.Close()

	fmt.Printf("[XTP-MGR] 🔒 TransportManager cerrado\n")
}

func (tm *TransportManager) getPeerKeys(peerDID string) (PeerKeys, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	pk, exists := tm.aclByDID[peerDID]
	return pk, exists
}

func splitFields(s string) []string {
	var fields []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			if start >= 0 {
				fields = append(fields, s[start:i])
				start = -1
			}
		} else {
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		fields = append(fields, s[start:])
	}
	return fields
}
