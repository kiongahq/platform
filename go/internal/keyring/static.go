package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// Static is AES-256-GCM with a 32-byte key held by the gateway. Its output is
// base64(nonce || ciphertext || tag), unchanged from the gateway's original
// credential encryption, so every record written before this package existed
// opens with it.
type Static struct {
	aead cipher.AEAD
}

// ErrStaticKey means KIONGA_CREDENTIAL_KEY is missing or not 32 bytes.
var ErrStaticKey = errors.New("KIONGA_CREDENTIAL_KEY must be a base64-encoded 32-byte key")

// NewStatic decodes a standard-base64 32-byte key.
func NewStatic(encodedKey string) (*Static, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, ErrStaticKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Static{aead: aead}, nil
}

// Seal encrypts with a random 96-bit nonce.
func (s *Static) Seal(_ context.Context, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, aad)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(sealed)))
	base64.StdEncoding.Encode(out, sealed)
	return out, nil
}

// Open reverses Seal; any malformed or unauthenticated value is ErrOpen.
func (s *Static) Open(_ context.Context, ciphertext, aad []byte) ([]byte, error) {
	data := make([]byte, base64.StdEncoding.DecodedLen(len(ciphertext)))
	n, err := base64.StdEncoding.Decode(data, ciphertext)
	if err != nil || n < s.aead.NonceSize() {
		return nil, ErrOpen
	}
	data = data[:n]
	plain, err := s.aead.Open(nil, data[:s.aead.NonceSize()], data[s.aead.NonceSize():], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
