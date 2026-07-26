package crypto

import (
	"sync"
	"time"
)

type CipherSession struct {
	Handshake  *HandshakeState
	LastRekey  time.Time
	RekeyEvery time.Duration
	mu         sync.Mutex
}

func NewCipherSession(hs *HandshakeState) *CipherSession {
	return &CipherSession{
		Handshake:  hs,
		LastRekey:  time.Now(),
		RekeyEvery: 5 * time.Minute,
	}
}

func (s *CipherSession) Encrypt(plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.LastRekey) > s.RekeyEvery {
		s.Handshake.Rekey()
		s.LastRekey = time.Now()
	}

	return s.Handshake.Encrypt(plaintext)
}

func (s *CipherSession) Decrypt(ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.Handshake.Decrypt(ciphertext)
}

func (s *CipherSession) ForceRekey() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Handshake.Rekey()
	s.LastRekey = time.Now()
}
