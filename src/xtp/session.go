package xtp

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"xionia-xtp/src/crypto"
)

// ============================================================================
// SESSION MANAGER
// ============================================================================
// Maneja el ciclo de vida de una sesión Noise IK entre dos nodos:
//
//   1. Creación: NewSession(initiator, myIdentity, peerPubX)
//   2. Handshake: el iniciador llama InitiatorMessage() → envía al peer.
//      El respondedor llama HandleMessage(msg) → devuelve la respuesta.
//      El iniciador llama HandleMessage(respuesta) → handshake completo.
//   3. Comunicación: Encrypt(plaintext) / Decrypt(ciphertext)
//   4. Rekey: cada 100 mensajes o cada 5 minutos (lo que llegue primero).
//   5. Cierre: Close()
//
// El SessionManager es thread-safe.

const (
	// RekeyAfterMessages: rotar claves después de N mensajes.
	// Previene el agotamiento del nonce de ChaCha20 (2^64 mensajes,
	// pero rotamos mucho antes por seguridad).
	RekeyAfterMessages = 100

	// RekeyAfterDuration: rotar claves después de T tiempo.
	RekeyAfterDuration = 5 * time.Minute

	// HandshakeTimeout: timeout para completar el handshake Noise IK.
	HandshakeTimeout = 10 * time.Second
)

// SessionState representa el estado de una sesión individual.
type SessionState int

const (
	SessionNew          SessionState = iota // Creada, sin handshake
	SessionHandshaking                      // Handshake en progreso
	SessionActive                           // Handshake completo, sesión activa
	SessionClosed                           // Sesión cerrada
)

func (s SessionState) String() string {
	switch s {
	case SessionNew:
		return "NEW"
	case SessionHandshaking:
		return "HANDSHAKING"
	case SessionActive:
		return "ACTIVE"
	case SessionClosed:
		return "CLOSED"
	default:
		return "UNKNOWN"
	}
}

// Session representa una sesión Noise IK con un peer específico.
type Session struct {
	mu sync.Mutex

	// Identidad
	myDID    string
	peerDID  string
	peerPubX *[32]byte // Clave pública X25519 del peer (del ACL)

	// Noise IK
	noise       *crypto.HandshakeState
	isInitiator bool

	// Estado
	state       SessionState
	createdAt   time.Time
	activatedAt time.Time
	lastRekeyAt time.Time

	// Contadores para rekey
	sendCount int
	recvCount int

	// Callbacks (opcionales)
	onActivate func(peerDID string)
	onClose    func(peerDID string)
}

// NewSession crea una nueva sesión Noise IK con un peer.
//
// Para el INICIADOR: peerPubX es la clave pública X25519 del peer
// (se obtiene del ACL con acl.GetPeerKeys(peerDID)).
//
// Para el RESPONDEDOR: peerPubX puede ser nil (Noise IK la recibe
// del iniciador durante el handshake).
func NewSession(isInitiator bool, myIdentity *crypto.Identity, peerDID string, peerPubX *[32]byte) (*Session, error) {
	var myPrivX *[32]byte
	var myPubX *[32]byte

	// Extraer claves X25519 de la identidad
	privX := new([32]byte)
	copy(privX[:], myIdentity.PrivKeyX[:])
	myPrivX = privX

	pubX := new([32]byte)
	copy(pubX[:], myIdentity.PubKeyX[:])
	myPubX = pubX

	noise, err := crypto.NewHandshakeIK(isInitiator, myPrivX, myPubX, peerPubX)
	if err != nil {
		return nil, fmt.Errorf("creando Noise IK: %w", err)
	}

	return &Session{
		myDID:       myIdentity.DID,
		peerDID:     peerDID,
		peerPubX:    peerPubX,
		noise:       noise,
		isInitiator: isInitiator,
		state:       SessionNew,
		createdAt:   time.Now(),
	}, nil
}

// ============================================================================
// HANDSHAKE
// ============================================================================

// InitiatorMessage genera el primer mensaje del handshake (solo para el iniciador).
// Este mensaje se envía al peer a través del canal directo (hole punching)
// o a través del faro (relay fallback).
//
// Flujo del patrón IK (2 mensajes):
//   1. Iniciador → Respondedor: e, es, s, ss  (este método)
//   2. Respondedor → Iniciador: e, ee, se     (HandleMessage del iniciador)
func (s *Session) InitiatorMessage() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isInitiator {
		return nil, fmt.Errorf("solo el iniciador puede generar el primer mensaje")
	}
	if s.state != SessionNew {
		return nil, fmt.Errorf("sesión en estado %s, esperado NEW", s.state)
	}

	// Payload del primer mensaje: metadata de la sesión
	meta := sessionMeta{
		FromDID:   s.myDID,
		Timestamp: time.Now().Unix(),
		Version:   "xtp/1.0",
	}
	metaJSON, _ := json.Marshal(meta)

	msg, completed, err := s.noise.WriteHandshake(metaJSON)
	if err != nil {
		return nil, fmt.Errorf("escribiendo handshake: %w", err)
	}

	s.state = SessionHandshaking

	// En IK, el handshake termina después del mensaje 2 (del respondedor).
	// El mensaje 1 del iniciador NO completa el handshake.
	_ = completed

	return msg, nil
}

// HandleMessage procesa un mensaje de handshake del peer.
//
// Para el RESPONDEDOR: recibe el mensaje 1 del iniciador, devuelve el mensaje 2.
// Para el INICIADOR: recibe el mensaje 2 del respondedor, handshake completo.
//
// Retorna:
//   - response: el mensaje de respuesta (nil si el handshake se completó)
//   - completed: true si el handshake terminó (la sesión está activa)
//   - err: error si algo falló
func (s *Session) HandleMessage(msg []byte) (response []byte, completed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == SessionClosed {
		return nil, false, fmt.Errorf("sesión cerrada")
	}

	if s.isInitiator {
		// Iniciador: recibe el mensaje 2 del respondedor → handshake completo
		_, done, err := s.noise.ReadHandshake(msg)
		if err != nil {
			return nil, false, fmt.Errorf("leyendo handshake (iniciador): %w", err)
		}
		if done {
			s.state = SessionActive
			s.activatedAt = time.Now()
			s.lastRekeyAt = time.Now()
			s.sendCount = 0
			s.recvCount = 0
			if s.onActivate != nil {
				go s.onActivate(s.peerDID)
			}
		}
		return nil, done, nil
	}

	// Respondedor: recibe el mensaje 1 del iniciador → genera el mensaje 2
	_, _, err = s.noise.ReadHandshake(msg)
	if err != nil {
		return nil, false, fmt.Errorf("leyendo handshake (respondedor): %w", err)
	}

	// Generar respuesta (mensaje 2)
	resp, done, err := s.noise.WriteHandshake(nil)
	if err != nil {
		return nil, false, fmt.Errorf("escribiendo handshake (respondedor): %w", err)
	}

	s.state = SessionHandshaking

	if done {
		s.state = SessionActive
		s.activatedAt = time.Now()
		s.lastRekeyAt = time.Now()
		s.sendCount = 0
		s.recvCount = 0
		if s.onActivate != nil {
			go s.onActivate(s.peerDID)
		}
	}

	return resp, done, nil
}

// ============================================================================
// COMUNICACIÓN (post-handshake)
// ============================================================================

// Encrypt cifra un mensaje saliente usando Noise IK.
// Incluye rekey automático después de N mensajes o T tiempo.
func (s *Session) Encrypt(plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SessionActive {
		return nil, fmt.Errorf("sesión no activa (estado: %s)", s.state)
	}

	// Verificar si hay que hacer rekey
	s.maybeRekeyLocked()

	ciphertext, err := s.noise.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("cifrando: %w", err)
	}

	s.sendCount++
	return ciphertext, nil
}

// Decrypt descifra un mensaje entrante usando Noise IK.
func (s *Session) Decrypt(ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != SessionActive {
		return nil, fmt.Errorf("sesión no activa (estado: %s)", s.state)
	}

	plaintext, err := s.noise.Decrypt(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("descifrando: %w", err)
	}

	s.recvCount++
	return plaintext, nil
}

// maybeRekeyLocked rota las claves si se superó el límite de mensajes o tiempo.
// DEBE llamarse con s.mu tomado.
func (s *Session) maybeRekeyLocked() {
	needRekey := false

	if s.sendCount+s.recvCount >= RekeyAfterMessages {
		needRekey = true
	}
	if time.Since(s.lastRekeyAt) >= RekeyAfterDuration {
		needRekey = true
	}

	if needRekey {
		s.noise.Rekey()
		s.lastRekeyAt = time.Now()
		s.sendCount = 0
		s.recvCount = 0
		fmt.Printf("[XTP] 🔑 Rekey con %s (después de %d msg / %s)\n",
			s.peerDID[:20]+"...", s.sendCount+s.recvCount,
			time.Since(s.activatedAt).Round(time.Second))
	}
}

// ============================================================================
// CICLO DE VIDA
// ============================================================================

// State devuelve el estado actual de la sesión.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// IsActive devuelve true si la sesión está activa (handshake completo).
func (s *Session) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == SessionActive
}

// PeerDID devuelve el DID del peer.
func (s *Session) PeerDID() string {
	return s.peerDID
}

// IsInitiator devuelve true si este nodo es el iniciador de la sesión.
func (s *Session) IsInitiator() bool {
	return s.isInitiator
}

// Stats devuelve estadísticas de la sesión.
func (s *Session) Stats() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionStats{
		PeerDID:     s.peerDID,
		State:       s.state.String(),
		IsInitiator: s.isInitiator,
		SendCount:   s.sendCount,
		RecvCount:   s.recvCount,
		CreatedAt:   s.createdAt,
		ActivatedAt: s.activatedAt,
		LastRekeyAt: s.lastRekeyAt,
		Uptime:      time.Since(s.createdAt).Round(time.Second),
	}
}

type SessionStats struct {
	PeerDID     string        `json:"peer_did"`
	State       string        `json:"state"`
	IsInitiator bool          `json:"is_initiator"`
	SendCount   int           `json:"send_count"`
	RecvCount   int           `json:"recv_count"`
	CreatedAt   time.Time     `json:"created_at"`
	ActivatedAt time.Time     `json:"activated_at"`
	LastRekeyAt time.Time     `json:"last_rekey_at"`
	Uptime      time.Duration `json:"uptime"`
}

// Close cierra la sesión.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == SessionClosed {
		return
	}

	s.state = SessionClosed
	if s.onClose != nil {
		go s.onClose(s.peerDID)
	}
	fmt.Printf("[XTP] 🔒 Sesión cerrada con %s\n", s.peerDID[:20]+"...")
}

// OnActivate registra un callback que se llama cuando la sesión se activa.
func (s *Session) OnActivate(cb func(peerDID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivate = cb
}

// OnClose registra un callback que se llama cuando la sesión se cierra.
func (s *Session) OnClose(cb func(peerDID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onClose = cb
}

// ============================================================================
// METADATA DEL HANDSHAKE
// ============================================================================

// sessionMeta se incluye como payload en el primer mensaje del handshake.
// Permite al respondedor saber quién está iniciando la sesión y cuándo.
type sessionMeta struct {
	FromDID   string `json:"from_did"`
	Timestamp int64  `json:"ts"`
	Version   string `json:"version"`
}

// ============================================================================
// SESSION MANAGER (gestiona múltiples sesiones)
// ============================================================================

// Manager gestiona todas las sesiones activas de un nodo.
// Un nodo puede tener múltiples sesiones simultáneas (una por peer).
type Manager struct {
	mu       sync.RWMutex
	identity *crypto.Identity
	sessions map[string]*Session // peerDID → Session
}

func NewManager(identity *crypto.Identity) *Manager {
	return &Manager{
		identity: identity,
		sessions: make(map[string]*Session),
	}
}

// CreateSession crea una nueva sesión con un peer.
// Si ya existe una sesión activa con ese peer, la cierra y crea una nueva.
func (m *Manager) CreateSession(isInitiator bool, peerDID string, peerPubX *[32]byte) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Si ya hay una sesión con este peer, cerrarla
	if existing, ok := m.sessions[peerDID]; ok {
		existing.Close()
	}

	session, err := NewSession(isInitiator, m.identity, peerDID, peerPubX)
	if err != nil {
		return nil, err
	}

	m.sessions[peerDID] = session
	return session, nil
}

// GetSession devuelve la sesión con un peer (nil si no existe).
func (m *Manager) GetSession(peerDID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[peerDID]
}

// GetOrCreateSession devuelve la sesión existente o crea una nueva.
func (m *Manager) GetOrCreateSession(isInitiator bool, peerDID string, peerPubX *[32]byte) (*Session, error) {
	m.mu.RLock()
	session, exists := m.sessions[peerDID]
	m.mu.RUnlock()

	if exists && session.IsActive() {
		return session, nil
	}

	return m.CreateSession(isInitiator, peerDID, peerPubX)
}

// CloseSession cierra y elimina la sesión con un peer.
func (m *Manager) CloseSession(peerDID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[peerDID]; ok {
		session.Close()
		delete(m.sessions, peerDID)
	}
}

// CloseAll cierra todas las sesiones.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for did, session := range m.sessions {
		session.Close()
		delete(m.sessions, did)
	}
}

// ActiveSessions devuelve el número de sesiones activas.
func (m *Manager) ActiveSessions() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, session := range m.sessions {
		if session.IsActive() {
			count++
		}
	}
	return count
}

// ListSessions devuelve estadísticas de todas las sesiones.
func (m *Manager) ListSessions() []SessionStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]SessionStats, 0, len(m.sessions))
	for _, session := range m.sessions {
		stats = append(stats, session.Stats())
	}
	return stats
}

// Encrypt cifra un mensaje para un peer específico.
// Busca la sesión activa con ese peer y cifra con Noise IK.
func (m *Manager) Encrypt(peerDID string, plaintext []byte) ([]byte, error) {
	session := m.GetSession(peerDID)
	if session == nil {
		return nil, fmt.Errorf("no hay sesión con %s", peerDID[:20]+"...")
	}
	return session.Encrypt(plaintext)
}

// Decrypt descifra un mensaje de un peer específico.
func (m *Manager) Decrypt(peerDID string, ciphertext []byte) ([]byte, error) {
	session := m.GetSession(peerDID)
	if session == nil {
		return nil, fmt.Errorf("no hay sesión con %s", peerDID[:20]+"...")
	}
	return session.Decrypt(ciphertext)
}
