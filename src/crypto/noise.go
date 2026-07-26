package crypto

import (
	"errors"

	"github.com/flynn/noise"
)

type HandshakeState struct {
	cs     noise.CipherSuite
	hs     *noise.HandshakeState
	cipher *noise.CipherState
}

func NewHandshakeIK(isInitiator bool, myPrivX *[32]byte, myPubX *[32]byte, theirPubX *[32]byte) (*HandshakeState, error) {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

	var hs *noise.HandshakeState
	var err error

	if isInitiator {
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

	return &HandshakeState{cs: cs, hs: hs}, nil
}

func (h *HandshakeState) WriteHandshake(payload []byte) ([]byte, *noise.CipherState, error) {
	msg, _, cipher, err := h.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	if cipher != nil {
		h.cipher = cipher
	}
	return msg, cipher, nil
}

func (h *HandshakeState) ReadHandshake(msg []byte) ([]byte, *noise.CipherState, error) {
	payload, _, cipher, err := h.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, nil, err
	}
	if cipher != nil {
		h.cipher = cipher
	}
	return payload, cipher, nil
}

func (h *HandshakeState) Encrypt(plaintext []byte) ([]byte, error) {
	if h.cipher == nil {
		return nil, errors.New("handshake no completado")
	}
	return h.cipher.Encrypt(nil, nil, plaintext)
}

func (h *HandshakeState) Decrypt(ciphertext []byte) ([]byte, error) {
	if h.cipher == nil {
		return nil, errors.New("handshake no completado")
	}
	return h.cipher.Decrypt(nil, nil, ciphertext)
}

func (h *HandshakeState) Rekey() {
	if h.cipher != nil {
		h.cipher.Rekey()
	}
}
