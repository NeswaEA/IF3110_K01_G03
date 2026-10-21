package connections

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher seals connection configs with AES-256-GCM. The stored form is
// nonce || ciphertext. The row id is bound as additional data, so a blob copied
// to another row fails to open.
type Cipher struct{ aead cipher.AEAD }

// NewCipher takes CONNECTION_ENCRYPTION_KEY: base64 of exactly 32 bytes.
func NewCipher(b64Key string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, errors.New("connections: encryption key is not valid base64")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("connections: encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plain, aad), nil
}

func (c *Cipher) Open(blob, aad []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(blob) < n+c.aead.Overhead() {
		return nil, errors.New("connections: ciphertext too short")
	}
	plain, err := c.aead.Open(nil, blob[:n], blob[n:], aad)
	if err != nil {
		return nil, errors.New("connections: cannot decrypt config") // no detail on purpose
	}
	return plain, nil
}
