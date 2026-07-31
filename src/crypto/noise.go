package crypto

import (
	"errors"
	"sync"

	"github.com/flynn/noise"
)

// HandshakeState envuelve un handshake Noise IK (patrón IK:
// el iniciador conoce la clave estática del respondedor de antemano).
//
// Después del handshake, hay DOS CipherStates:
//   - sendCipher: para cifrar mensajes salientes (Encrypt)
//   - recvCipher: para descifrar mensajes entrantes (Decrypt)
//
// El orden depende del rol:
//   - Iniciador: sendCipher = c1, recvCipher = c2
//   - Respondedor: sendCipher = c2, recvCipher = c1
type HandshakeState struct {
	cs          noise.CipherSuite
	hs          *noise.HandshakeState
	sendCipher  *noise.CipherState // Para Encrypt (mensajes salientes)
	recvCipher  *noise.CipherState // Para Decrypt (mensajes entrantes)
	isInitiator bool
	completed   bool
	mu          sync.Mutex // Protege sendCipher/recvCipher/completed
}

// NewHandshakeIK crea un nuevo handshake Noise IK.
//
// Para el INICIADOR: myPrivX, myPubX, y theirPubX (la clave pública
// estática del peer, que se obtiene del ACL).
//
// Para el RESPONDEDOR: myPrivX, myPubX. theirPubX puede ser nil
// (IK la recibe del iniciador durante el handshake).
func NewHandshakeIK(isInitiator bool, myPrivX *[32]byte, myPubX *[32]byte, theirPubX *[32]byte) (*HandshakeState, error) {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

	var hs *noise.HandshakeState
	var err error

	if isInitiator {
		if theirPubX == nil {
			return nil, errors.New("iniciador IK necesita la clave pública del peer")
		}
		hs, err = noise.NewHandshakeState(noise.Config{
			CipherSuite:   cs,
			Pattern:       noise.HandshakeIK,
			Initiator:     true,
			StaticKeypair: noise.DHKey{Private: myPrivX[:], Public: myPubX[:]},
			PeerStatic:    theirPubX[:],
		})
	} else {
		hs, err = noise.NewHandshakeState(noise.Config{
			CipherSuite:   cs,
			Pattern:       noise.HandshakeIK,
			Initiator:     false,
			StaticKeypair: noise.DHKey{Private: myPrivX[:], Public: myPubX[:]},
		})
	}

	if err != nil {
		return nil, err
	}

	return &HandshakeState{
		cs:          cs,
		hs:          hs,
		isInitiator: isInitiator,
	}, nil
}

// WriteHandshake escribe un mensaje de handshake.
// Retorna el mensaje (para enviar al peer) y true si el handshake se completó.
func (h *HandshakeState) WriteHandshake(payload []byte) (msg []byte, completed bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	out, c1, c2, err := h.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, false, err
	}

	// Cuando el handshake termina, flynn/noise devuelve los dos CipherStates.
	if c1 != nil && c2 != nil {
		if h.isInitiator {
			h.sendCipher = c1 // Iniciador cifra con c1
			h.recvCipher = c2 // Iniciador descifra con c2
		} else {
			h.sendCipher = c2 // Respondedor cifra con c2
			h.recvCipher = c1 // Respondedor descifra con c1
		}
		h.completed = true
	}

	return out, h.completed, nil
}

// ReadHandshake lee un mensaje de handshake del peer.
// Retorna el payload extraído y true si el handshake se completó.
func (h *HandshakeState) ReadHandshake(msg []byte) (payload []byte, completed bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, c1, c2, err := h.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, false, err
	}

	if c1 != nil && c2 != nil {
		if h.isInitiator {
			h.sendCipher = c1
			h.recvCipher = c2
		} else {
			h.sendCipher = c2
			h.recvCipher = c1
		}
		h.completed = true
	}

	return p, h.completed, nil
}

// IsCompleted devuelve true si el handshake terminó y los CipherStates están listos.
func (h *HandshakeState) IsCompleted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.completed
}

// Encrypt cifra un mensaje saliente (usa sendCipher).
func (h *HandshakeState) Encrypt(plaintext []byte) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sendCipher == nil {
		return nil, errors.New("handshake no completado")
	}
	return h.sendCipher.Encrypt(nil, nil, plaintext)
}

// Decrypt descifra un mensaje entrante (usa recvCipher).
func (h *HandshakeState) Decrypt(ciphertext []byte) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.recvCipher == nil {
		return nil, errors.New("handshake no completado")
	}
	return h.recvCipher.Decrypt(nil, nil, ciphertext)
}

// Rekey rota las claves de AMBOS CipherStates (forward secrecy adicional).
// Se recomienda llamar cada N mensajes o cada T tiempo.
func (h *HandshakeState) Rekey() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sendCipher != nil {
		h.sendCipher.Rekey()
	}
	if h.recvCipher != nil {
		h.recvCipher.Rekey()
	}
}
