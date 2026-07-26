package crypto

import (
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// DeriveKeyID toma los primeros 4 bytes de PubKeyX para indexación O(1)
func DeriveKeyID(pubKeyX []byte) [4]byte {
	var id [4]byte
	if len(pubKeyX) >= 4 {
		copy(id[:], pubKeyX[:4])
	}
	return id
}

func EncryptPayload(sharedKey []byte, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(sharedKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func DecryptPayload(sharedKey []byte, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(sharedKey)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize+aead.Overhead() {
		return nil, errors.New("payload corrupto o demasiado corto")
	}
	nonce := ciphertext[:nonceSize]
	actualCipher := ciphertext[nonceSize:]

	return aead.Open(nil, nonce, actualCipher, nil)
}
