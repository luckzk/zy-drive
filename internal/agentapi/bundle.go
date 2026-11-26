// SPDX-License-Identifier: MPL-2.0
package agentapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"golang.org/x/crypto/pbkdf2"
)

// Bundle is the encrypted config package the user carries from the
// desktop to their own server by hand (scp, USB stick, ...). It
// holds the mounts (metadata + non-secret config + credentials) and
// the sync jobs for one Agent.
//
// The commercial model (docs/monetization.md): Agent scheduled runs
// are a Pro feature sold as functionality, NOT a hosted service.
// There is no central server: the customer deploys zy-drive-agent
// to their own server, and the credentials move only by this bundle
// in the user's own hands — encrypted with a passphrase typed on
// both ends, never through any of our servers (there are none).
type Bundle struct {
	Version    int           `json:"version"` // 1
	ExportedAt time.Time     `json:"exportedAt"`
	AgentName  string        `json:"agentName,omitempty"`
	Mounts     []BundleMount `json:"mounts"`
	Jobs       []domain.SyncJob `json:"jobs"`
}

// BundleMount is one mount in the bundle: metadata + non-secret
// config (plaintext inside the encrypted bundle) + credentials
// (plaintext inside the encrypted bundle only — the bundle file
// itself is AES-256-GCM encrypted, and the Agent re-encrypts each
// credential with its own master key on import).
type BundleMount struct {
	Mount       domain.Mount       `json:"mount"`
	Config      map[string]string  `json:"config"`
	Credentials BundleCredentials  `json:"credentials"`
}

// BundleCredentials mirrors the app's encrypted credential blob
// (same JSON field names, so both sides agree without sharing an
// unexported type).
type BundleCredentials struct {
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// MEGA exported session (v0.9.1): the session ID + base64
	// master key, so an Agent import can log in with
	// LoginWithKeys. The password is never in a bundle.
	MegaSession   string `json:"megaSession,omitempty"`
	MegaMasterKey string `json:"megaMasterKey,omitempty"`
}

// bundleEnvelope is the on-disk format: versioned, salted,
// passphrase-encrypted (PBKDF2-SHA256, 100k iterations, same
// parameters as the master-key store in internal/auth).
type bundleEnvelope struct {
	Version int    `json:"version"`
	Salt    string `json:"salt"`  // base64, 16 bytes
	Nonce   string `json:"nonce"` // base64, 12 bytes
	Data    string `json:"data"`  // base64, AES-256-GCM ciphertext
}

const bundleVersion = 1

// EncryptBundle serializes and encrypts a bundle under passphrase.
// A passphrase shorter than 8 characters is refused: the bundle
// carries live cloud credentials, and a weak passphrase defeats
// the encryption.
func EncryptBundle(b *Bundle, passphrase string) ([]byte, error) {
	if len([]rune(passphrase)) < 8 {
		return nil, errors.New("agentapi: 配置包口令至少 8 个字符")
	}
	b.Version = bundleVersion
	b.ExportedAt = time.Now().UTC()
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("agentapi: bundle: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 100_000, 32, sha256.New)
	block, err := aes.NewCipher(key)
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
	ct := gcm.Seal(nil, nonce, plain, nil)
	env := bundleEnvelope{
		Version: bundleVersion,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Data:    base64.StdEncoding.EncodeToString(ct),
	}
	return json.MarshalIndent(env, "", "  ")
}

// DecryptBundle reverses EncryptBundle. A wrong passphrase (or a
// tampered file) fails authentication and returns an error — never
// partial plaintext.
func DecryptBundle(data []byte, passphrase string) (*Bundle, error) {
	var env bundleEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("agentapi: 这不是一个 ZyDrive 配置包（解析失败：%v）", err)
	}
	if env.Version != bundleVersion {
		return nil, fmt.Errorf("agentapi: 配置包版本 %d 不支持（当前 %d），请用新版桌面导出", env.Version, bundleVersion)
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil || len(salt) < 16 {
		return nil, fmt.Errorf("agentapi: 配置包已损坏（salt）")
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("agentapi: 配置包已损坏（nonce）")
	}
	ct, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("agentapi: 配置包已损坏（data）")
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 100_000, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("agentapi: 口令不对，或配置包被改过（解密失败）")
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, fmt.Errorf("agentapi: 配置包内容解析失败：%w", err)
	}
	return &b, nil
}
