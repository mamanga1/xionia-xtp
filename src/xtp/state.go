package xtp

import (
	"fmt"
	"sync"
	"time"
)

// ============================================================================
// ESTADOS DEL TRANSPORTE
// ============================================================================
// El ciclo de vida de una conexión de transporte XTP:
//
//   Offline → Connecting → Registered → Discovering → Punching →
//   NoiseHandshake → Direct → Keepalive → (Direct o RelayFallback) → Closed
//
// Si el hole punching falla en cualquier punto, cae a RelayFallback
// (el faro relayea como en Fase 1). Si el Noise handshake falla,
// también cae a RelayFallback (sin forward secrecy, pero funciona).

type State int

const (
	// Offline: sin conexión al faro. Estado inicial.
	Offline State = iota

	// Connecting: conectando al faro (UDP o WSS, con Gate DID handshake).
	Connecting

	// Registered: conectado al faro, ANNOUNCE enviado, nodo registrado.
	// Puede recibir y enviar mensajes via relay (fallback Fase 1).
	Registered

	// Discovering: buscando un peer específico (WHERE_IS al faro).
	Discovering

	// Punching: hole punching UDP en progreso. Ambos nodos envían
	// paquetes a los endpoints públicos del otro simultáneamente.
	Punching

	// NoiseHandshake: el hole punching funcionó, haciendo handshake
	// Noise IK directo (sin faro). 3 mensajes: e → ee,s → s,se.
	NoiseHandshake

	// Direct: sesión directa establecida con Noise IK. Los mensajes
	// van directo entre nodos (el faro NO los ve). Forward secrecy activo.
	Direct

	// Keepalive: sesión directa activa, enviando keepalives periódicos
	// para mantener el mapeo del NAT abierto.
	Keepalive

	// RelayFallback: el hole punching o el Noise handshake fallaron.
	// Los mensajes se relayean a través del faro (como Fase 1).
	// Sin forward secrecy, pero funcional.
	RelayFallback

	// Closed: sesión cerrada (por el usuario, por timeout, o por error).
	Closed
)

func (s State) String() string {
	switch s {
	case Offline:
		return "OFFLINE"
	case Connecting:
		return "CONNECTING"
	case Registered:
		return "REGISTERED"
	case Discovering:
		return "DISCOVERING"
	case Punching:
		return "PUNCHING"
	case NoiseHandshake:
		return "NOISE_HANDSHAKE"
	case Direct:
		return "DIRECT"
	case Keepalive:
		return "KEEPALIVE"
	case RelayFallback:
		return "RELAY_FALLBACK"
	case Closed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// ============================================================================
// EVENTOS (disparan transiciones de estado)
// ============================================================================

type Event int

const (
	// EvConnectFaro: el usuario o el sistema pide conectar al faro.
	EvConnectFaro Event = iota

	// EvFaroConnected: la conexión al faro se estableció (Gate OK).
	EvFaroConnected

	// EvFaroDisconnected: la conexión al faro se perdió.
	EvFaroDisconnected

	// EvAnnounceSent: el ANNOUNCE se envió y el faro confirmó (ACK_IP).
	EvAnnounceSent

	// EvDiscoverPeer: se pide buscar un peer específico (WHERE_IS).
	EvDiscoverPeer

	// EvPeerFound: el peer está registrado en el faro (READY).
	EvPeerFound

	// EvPeerNotFound: el peer NO está registrado (NOT_FOUND).
	EvPeerNotFound

	// EvStartPunch: iniciar hole punching (OPEN_SESSION enviado al faro).
	EvStartPunch

	// EvPunchComplete: el hole punching funcionó (recibimos paquete del peer).
	EvPunchComplete

	// EvPunchFailed: el hole punching falló (timeout, NAT simétrico).
	EvPunchFailed

	// EvStartNoise: iniciar handshake Noise IK (directo, sin faro).
	EvStartNoise

	// EvNoiseComplete: el handshake Noise IK se completó (sesión cifrada activa).
	EvNoiseComplete

	// EvNoiseFailed: el handshake Noise IK falló (clave inválida, timeout).
	EvNoiseFailed

	// EvKeepaliveTick: es hora de enviar un keepalive.
	EvKeepaliveTick

	// EvKeepaliveTimeout: el peer no respondió al keepalive (sesión muerta).
	EvKeepaliveTimeout

	// EvFallbackToRelay: caer a relay (el hole punching o Noise fallaron).
	EvFallbackToRelay

	// EvCloseSession: cerrar la sesión (usuario o sistema).
	EvCloseSession

	// EvError: error genérico (red, crypto, etc.).
	EvError
)

func (e Event) String() string {
	switch e {
	case EvConnectFaro:
		return "CONNECT_FARO"
	case EvFaroConnected:
		return "FARO_CONNECTED"
	case EvFaroDisconnected:
		return "FARO_DISCONNECTED"
	case EvAnnounceSent:
		return "ANNOUNCE_SENT"
	case EvDiscoverPeer:
		return "DISCOVER_PEER"
	case EvPeerFound:
		return "PEER_FOUND"
	case EvPeerNotFound:
		return "PEER_NOT_FOUND"
	case EvStartPunch:
		return "START_PUNCH"
	case EvPunchComplete:
		return "PUNCH_COMPLETE"
	case EvPunchFailed:
		return "PUNCH_FAILED"
	case EvStartNoise:
		return "START_NOISE"
	case EvNoiseComplete:
		return "NOISE_COMPLETE"
	case EvNoiseFailed:
		return "NOISE_FAILED"
	case EvKeepaliveTick:
		return "KEEPALIVE_TICK"
	case EvKeepaliveTimeout:
		return "KEEPALIVE_TIMEOUT"
	case EvFallbackToRelay:
		return "FALLBACK_TO_RELAY"
	case EvCloseSession:
		return "CLOSE_SESSION"
	case EvError:
		return "ERROR"
	default:
		return fmt.Sprintf("UNKNOWN_EVENT(%d)", int(e))
	}
}

// ============================================================================
// TABLA DE TRANSICIONES
// ============================================================================
// Cada entrada: (estado actual, evento) → nuevo estado.
// Si una transición no está en la tabla, es inválida (se loguea y se ignora).

type transitionKey struct {
	from  State
	event Event
}

var transitionTable = map[transitionKey]State{
	// Offline → Connecting (conectar al faro)
	{Offline, EvConnectFaro}: Connecting,

	// Connecting → Registered (faro conectado + ANNOUNCE confirmado)
	{Connecting, EvFaroConnected}:    Registered,
	{Connecting, EvFaroDisconnected}: Offline,
	{Connecting, EvError}:            Offline,

	// Registered → Discovering (buscar peer)
	{Registered, EvDiscoverPeer}:     Discovering,
	{Registered, EvFaroDisconnected}: Offline,

	// Discovering → Punching (peer encontrado, iniciar hole punching)
	{Discovering, EvPeerFound}:        Punching,
	{Discovering, EvPeerNotFound}:     Registered, // Peer no está, volver a Registered
	{Discovering, EvFaroDisconnected}: Offline,

	// Punching → NoiseHandshake (hole punching funcionó)
	{Punching, EvPunchComplete}: NoiseHandshake,
	// Punching → RelayFallback (hole punching falló)
	{Punching, EvPunchFailed}:      RelayFallback,
	{Punching, EvFaroDisconnected}: Offline,

	// NoiseHandshake → Direct (Noise IK completado)
	{NoiseHandshake, EvNoiseComplete}: Direct,
	// NoiseHandshake → RelayFallback (Noise falló)
	{NoiseHandshake, EvNoiseFailed}: RelayFallback,

	// Direct → Keepalive (sesión activa, enviar keepalives)
	{Direct, EvKeepaliveTick}: Keepalive,
	{Direct, EvCloseSession}:  Closed,
	{Direct, EvError}:         RelayFallback,

	// Keepalive → Direct (keepalive respondido, volver a Direct)
	{Keepalive, EvKeepaliveTick}: Direct,
	// Keepalive → Closed (peer no respondió, sesión muerta)
	{Keepalive, EvKeepaliveTimeout}: Closed,
	{Keepalive, EvCloseSession}:     Closed,

	// RelayFallback → Discovering (reintentar hole punching)
	{RelayFallback, EvDiscoverPeer}: Discovering,
	// RelayFallback → Closed (cerrar sesión)
	{RelayFallback, EvCloseSession}:     Closed,
	{RelayFallback, EvFaroDisconnected}: Offline,

	// Cualquier estado → Closed (cerrar)
	{Offline, EvCloseSession}:        Closed,
	{Connecting, EvCloseSession}:     Closed,
	{Registered, EvCloseSession}:     Closed,
	{Discovering, EvCloseSession}:    Closed,
	{Punching, EvCloseSession}:       Closed,
	{NoiseHandshake, EvCloseSession}: Closed,
}

// ============================================================================
// FSM (máquina de estados del transporte)
// ============================================================================

// StateCallback se llama al entrar o salir de un estado.
// Recibe el estado, el evento que disparó la transición, y metadata opcional.
type StateCallback func(from, to State, event Event, meta map[string]interface{})

type FSM struct {
	mu       sync.RWMutex
	current  State
	peerDID  string // DID del peer (vacío si no hay sesión)
	faroAddr string // Dirección del faro (vacío si no conectado)

	// Callbacks
	onEnter map[State]StateCallback
	onExit  map[State]StateCallback

	// Historial de transiciones (para diagnóstico, máximo 100)
	history []Transition
	maxHist int

	// Timestamps
	enteredAt time.Time
	createdAt time.Time
}

type Transition struct {
	From  State
	To    State
	Event Event
	At    time.Time
	Meta  map[string]interface{}
}

func NewFSM() *FSM {
	return &FSM{
		current:   Offline,
		onEnter:   make(map[State]StateCallback),
		onExit:    make(map[State]StateCallback),
		history:   make([]Transition, 0, 100),
		maxHist:   100,
		enteredAt: time.Now(),
		createdAt: time.Now(),
	}
}

// Current devuelve el estado actual (thread-safe).
func (f *FSM) Current() State {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.current
}

// PeerDID devuelve el DID del peer de la sesión actual.
func (f *FSM) PeerDID() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.peerDID
}

// SetPeerDID establece el DID del peer (al iniciar una sesión).
func (f *FSM) SetPeerDID(did string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peerDID = did
}

// FaroAddr devuelve la dirección del faro.
func (f *FSM) FaroAddr() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.faroAddr
}

// SetFaroAddr establece la dirección del faro.
func (f *FSM) SetFaroAddr(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faroAddr = addr
}

// OnEnter registra un callback que se llama al ENTRAR a un estado.
func (f *FSM) OnEnter(state State, cb StateCallback) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onEnter[state] = cb
}

// OnExit registra un callback que se llama al SALIR de un estado.
func (f *FSM) OnExit(state State, cb StateCallback) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onExit[state] = cb
}

// Send procesa un evento y realiza la transición de estado si es válida.
// Devuelve true si la transición se realizó, false si es inválida.
func (f *FSM) Send(event Event, meta map[string]interface{}) bool {
	f.mu.Lock()
	from := f.current
	key := transitionKey{from: from, event: event}
	to, valid := transitionTable[key]

	if !valid {
		f.mu.Unlock()
		// Transición inválida: loguear y ignorar (no es un error fatal,
		// puede ser un evento duplicado o fuera de orden).
		Debugf("[FSM] ⚠️ Transición inválida: %s + %s (ignorada)\n", from, event)
		return false
	}

	// Callback de salida del estado actual
	exitCb := f.onExit[from]

	// Actualizar estado
	f.current = to
	f.enteredAt = time.Now()

	// Registrar en historial
	t := Transition{From: from, To: to, Event: event, At: time.Now(), Meta: meta}
	if len(f.history) >= f.maxHist {
		f.history = f.history[1:] // Sacar el más viejo
	}
	f.history = append(f.history, t)

	// Callback de entrada al nuevo estado
	enterCb := f.onEnter[to]

	f.mu.Unlock()

	// Ejecutar callbacks FUERA del lock (para evitar deadlocks si el
	// callback llama a Send() u otra función del FSM).
	if exitCb != nil {
		exitCb(from, to, event, meta)
	}
	if enterCb != nil {
		enterCb(from, to, event, meta)
	}

	Debugf("[FSM] %s → %s (%s)\n", from, to, event)
	return true
}

// History devuelve el historial de transiciones (copia, thread-safe).
func (f *FSM) History() []Transition {
	f.mu.RLock()
	defer f.mu.RUnlock()
	h := make([]Transition, len(f.history))
	copy(h, f.history)
	return h
}

// InState devuelve true si el estado actual es uno de los dados.
func (f *FSM) InState(states ...State) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, s := range states {
		if f.current == s {
			return true
		}
	}
	return false
}

// IsDirect devuelve true si la sesión es directa (Noise IK activo).
func (f *FSM) IsDirect() bool {
	return f.InState(Direct, Keepalive)
}

// IsRelay devuelve true si la sesión usa relay (fallback).
func (f *FSM) IsRelay() bool {
	return f.InState(RelayFallback)
}

// IsConnected devuelve true si hay conexión al faro (cualquier estado
// excepto Offline y Closed).
func (f *FSM) IsConnected() bool {
	return !f.InState(Offline, Closed)
}

// Reset vuelve al estado Offline (para reconexión).
func (f *FSM) Reset() {
	f.mu.Lock()
	from := f.current
	f.current = Offline
	f.peerDID = ""
	f.enteredAt = time.Now()
	f.mu.Unlock()
	Debugf("[FSM] %s → OFFLINE (reset)\n", from)
}

// ElapsedInState devuelve cuánto tiempo lleva en el estado actual.
func (f *FSM) ElapsedInState() time.Duration {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return time.Since(f.enteredAt)
}

// String devuelve una representación legible del FSM.
func (f *FSM) String() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return fmt.Sprintf("FSM{state=%s, peer=%s, faro=%s, elapsed=%s}",
		f.current, f.peerDID, f.faroAddr, time.Since(f.enteredAt).Round(time.Second))
}

// DebugMode controla la visibilidad de los logs internos de XTP.
var DebugMode bool

// Debugf imprime solo si DebugMode está activo.
func Debugf(format string, args ...interface{}) {
	if DebugMode {
		fmt.Printf(format, args...)
	}
}
