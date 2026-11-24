package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

// SecretsStore encrypts tokens at rest. The master key comes from the
// system keychain when available; otherwise it is derived from a user
// password with PBKDF2 (password itself is never stored).
// Ciphertext format: salt (16B) || nonce (12B) || AES-256-GCM ciphertext.
type SecretsStore struct {
	masterKey []byte
}

// NewSecretsStore derives a master key from a password and a persisted
// salt. The caller generates the salt once, stores it (e.g. in the
// secrets table next to key_version), and passes the same salt on every
// startup. In production the password comes from the keychain, not from
// a config file.
func NewSecretsStore(password string, salt []byte) (*SecretsStore, error) {
	if password == "" {
		return nil, errors.New("auth: master password/key is required")
	}
	if len(salt) < 16 {
		return nil, errors.New("auth: salt must be at least 16 bytes (generate once, persist it)")
	}
	key := pbkdf2.Key([]byte(password), salt, 100_000, 32, sha256.New)
	return &SecretsStore{masterKey: key}, nil
}

// Encrypt encrypts plaintext. Never pass a token through any log call.
func (s *SecretsStore) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt.
func (s *SecretsStore) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("auth: ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
