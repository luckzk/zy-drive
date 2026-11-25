package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Account is the stored credential reference for one cloud account.
// It holds no secrets: secrets live encrypted in the secrets table and
// are only decrypted in memory at use time.
type Account struct {
	ID          string              `json:"id"`
	Provider    domain.ProviderKind `json:"provider"`
	DisplayName string              `json:"displayName"`
	Status      domain.MountStatus  `json:"status"`
}

// AddMountInput is the wizard's payload for registering one mount
// with credentials. It is flat on purpose: mygo.Bind generates the
// TypeScript client from it, and every field is one form input.
// Secrets travel here only in memory, from the user's own Mac UI;
// they are encrypted before anything is written to SQLite.
type AddMountInput struct {
	DisplayName  string              `json:"displayName"`
	Provider     domain.ProviderKind `json:"provider"`
	Group        string              `json:"group,omitempty"`
	Endpoint     string              `json:"endpoint,omitempty"`     // S3-compatible endpoint (R2/MinIO/...)
	Region       string              `json:"region,omitempty"`       // S3 region
	Bucket       string              `json:"bucket,omitempty"`       // S3 bucket
	Prefix       string              `json:"prefix,omitempty"`       // S3 key prefix inside the bucket
	PathStyle    bool                `json:"pathStyle,omitempty"`    // S3 path-style addressing (MinIO/...)
	AccessKey    string              `json:"accessKey,omitempty"`    // S3 AccessKey (secret)
	SecretKey    string              `json:"secretKey,omitempty"`    // S3 SecretKey (secret)
	AccessToken  string              `json:"accessToken,omitempty"`  // OAuth access token (secret)
	RefreshToken string              `json:"refreshToken,omitempty"` // OAuth refresh token (secret)
	ClientID     string              `json:"clientId,omitempty"`     // OAuth app client id (not secret)
	ClientSecret string              `json:"clientSecret,omitempty"` // OAuth app client secret (secret)
	TokenURL     string              `json:"tokenUrl,omitempty"`     // OAuth token endpoint override (tests)
	APIBase      string              `json:"apiBase,omitempty"`      // API base override for Dropbox/Drive (tests / proxied deployments; normally empty)
	RootID       string              `json:"rootId,omitempty"`       // Drive root folder id, default "root"
	WorkspaceID  string              `json:"workspaceId,omitempty"`  // Drime workspace id, default "" = personal workspace (0)
	LocalPath    string              `json:"localPath,omitempty"`    // local mount: absolute folder path
}

// mountCredentials is the encrypted-at-rest credential blob for one
// mount. Only the fields the provider needs are present; everything
// else is omitted. It is JSON-marshal'd, AES-256-GCM encrypted with
// the master key (internal/auth) and stored in the secrets table.
type mountCredentials struct {
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

func (c mountCredentials) empty() bool {
	return c == mountCredentials{}
}

// secretIDFor returns the secrets-table id for a mount. It is also
// stored as Mount.AccountID, so the mount always names its own
// credential reference and never a secret value.
func secretIDFor(mountID string) string { return "mount:" + mountID }

// AddMount registers a Mount and builds its provider from the registry.
// The provider is constructed from a storage.Config assembled by the
// caller (which decrypted the credentials in memory). The wizard UI
// collects provider-specific fields; this function stays generic.
// In-memory only: AddMountWithCredentials is the persisted entry
// point used by the UI.
func (a *App) AddMount(ctx context.Context, m *domain.Mount, cfg storage.Config) error {
	if m.ID == "" {
		return fmt.Errorf("app: mount id is required")
	}
	p, err := a.Registry.New(m.Provider, cfg)
	if err != nil {
		return fmt.Errorf("app: add mount %s: %w", m.DisplayName, err)
	}
	// A cheap capability probe: Caps must not panic and must be sane.
	caps := p.Caps()
	_ = caps
	m.Caps = caps
	m.Status = domain.MountOK
	a.mounts[m.ID] = MountHandle{Mount: m, Provider: p}
	return nil
}

// AddMountWithCredentials is the wizard's save path: validate, build
// the provider, probe the root listing, and only then persist the
// mount (metadata + non-secret config in mounts, encrypted
// credentials in secrets). A failed probe saves nothing and returns
// a plain-language error; the caller shows it and offers a retry.
func (a *App) AddMountWithCredentials(ctx context.Context, in AddMountInput) (*domain.Mount, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, fmt.Errorf("app: 请给网盘起个名字")
	}
	switch in.Provider {
	case domain.ProviderS3, domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderLocal:
	default:
		return nil, fmt.Errorf("app: %s 还在 V0.2 接入，现在加不了", in.Provider)
	}
	// Local mounts carry no credentials; cloud mounts need the master
	// key store to encrypt theirs.
	if in.Provider != domain.ProviderLocal && a.secrets == nil {
		return nil, fmt.Errorf("app: 凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	cfg, creds, err := a.mountConfig(in)
	if err != nil {
		return nil, err
	}
	// Probe first: one cheap List of the root. S3 lists the bucket
	// root, Dropbox/Drive list their root folder, local lists the
	// chosen directory. Any auth/network/path failure aborts here,
	// before anything is persisted.
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p, err := a.Registry.New(in.Provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	if _, err := p.List(probeCtx, ""); err != nil {
		return nil, humanProbeError(in.Provider, err)
	}
	now := time.Now().UTC()
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, fmt.Errorf("app: add mount: %w", err)
	}
	id := "mnt-" + hex.EncodeToString(idBytes[:])
	m := &domain.Mount{
		ID:          id,
		DisplayName: strings.TrimSpace(in.DisplayName),
		Provider:    in.Provider,
		AccountID:   secretIDFor(id),
		RootRef:     rootRefFor(in),
		RootLabel:   rootLabelFor(in),
		Group:       strings.TrimSpace(in.Group),
		Caps:        p.Caps(),
		Status:      domain.MountOK,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// Persist: mount row first, then the encrypted credential blob.
	// A mount without its secret can never be rebuilt, so if the
	// secret write fails the mount row is removed again.
	if a.DB != nil {
		nonSecret := nonSecretConfig(in)
		if err := a.DB.SaveMount(m, nonSecret); err != nil {
			return nil, err
		}
		if !creds.empty() {
			plain, err := json.Marshal(creds)
			if err != nil {
				_ = a.DB.DeleteMount(id)
				return nil, fmt.Errorf("app: add mount: %w", err)
			}
			ct, err := a.secrets.Encrypt(plain)
			if err != nil {
				_ = a.DB.DeleteMount(id)
				return nil, fmt.Errorf("app: 加密凭证失败：%w", err)
			}
			if err := a.DB.SaveSecret(secretIDFor(id), ct); err != nil {
				_ = a.DB.DeleteMount(id)
				return nil, err
			}
		}
	}
	a.mounts[id] = MountHandle{Mount: m, Provider: p}
	return m, nil
}

// mountConfig turns wizard input into a driver Config (in-memory
// secrets) plus the credential blob to encrypt at rest. Validation
// errors are plain-language: they are shown verbatim in the wizard.
func (a *App) mountConfig(in AddMountInput) (storage.Config, mountCredentials, error) {
	var creds mountCredentials
	cfg := storage.Config{Extra: map[string]string{}}
	switch in.Provider {
	case domain.ProviderS3:
		bucket := strings.TrimSpace(in.Bucket)
		prefix := strings.Trim(strings.TrimSpace(in.Prefix), "/")
		if err := validateS3BucketPrefix(bucket, prefix); err != nil {
			return cfg, creds, err
		}
		if strings.TrimSpace(in.AccessKey) == "" || strings.TrimSpace(in.SecretKey) == "" {
			return cfg, creds, fmt.Errorf("app: S3 需要填 AccessKey 和 SecretKey")
		}
		cfg.Endpoint = strings.TrimSpace(in.Endpoint)
		cfg.Region = strings.TrimSpace(in.Region)
		cfg.AccessKey = in.AccessKey
		cfg.SecretKey = in.SecretKey
		creds.AccessKey = in.AccessKey
		creds.SecretKey = in.SecretKey
		cfg.Extra["bucket"] = bucket
		cfg.Extra["prefix"] = prefix
		if in.PathStyle {
			cfg.Extra["pathStyle"] = "true"
		}
	case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime:
		if strings.TrimSpace(in.AccessToken) == "" && strings.TrimSpace(in.RefreshToken) == "" {
			return cfg, creds, fmt.Errorf("app: 需要填 Access Token 或 Refresh Token（用授权码换取，或直接粘贴）")
		}
		cfg.Token = in.AccessToken
		creds.AccessToken = in.AccessToken
		creds.RefreshToken = in.RefreshToken
		creds.ClientSecret = in.ClientSecret
		cfg.Extra["refresh_token"] = in.RefreshToken
		cfg.Extra["client_id"] = strings.TrimSpace(in.ClientID)
		cfg.Extra["client_secret"] = in.ClientSecret
		if strings.TrimSpace(in.APIBase) != "" {
			cfg.Extra["api_base"] = strings.TrimSpace(in.APIBase)
		}
		if strings.TrimSpace(in.TokenURL) != "" {
			cfg.Extra["token_url"] = strings.TrimSpace(in.TokenURL)
		}
		if in.Provider == domain.ProviderDrive && strings.TrimSpace(in.RootID) != "" {
			cfg.Extra["rootId"] = strings.TrimSpace(in.RootID)
		}
		if in.Provider == domain.ProviderDrime && strings.TrimSpace(in.WorkspaceID) != "" {
			cfg.Extra["workspace_id"] = strings.TrimSpace(in.WorkspaceID)
		}
	case domain.ProviderLocal:
		abs, err := filepath.Abs(strings.TrimSpace(in.LocalPath))
		if err != nil || strings.TrimSpace(in.LocalPath) == "" {
			return cfg, creds, fmt.Errorf("app: 本地文件夹需要填一个绝对路径，例如 /Users/zy/照片")
		}
		info, err := os.Stat(abs)
		if err != nil {
			return cfg, creds, fmt.Errorf("app: 本地文件夹不存在：%s", abs)
		}
		if !info.IsDir() {
			return cfg, creds, fmt.Errorf("app: 本地路径不是文件夹：%s", abs)
		}
		cfg.Extra["root"] = abs
	}
	return cfg, creds, nil
}

// nonSecretConfig is the per-mount driver config that is safe to
// store in plaintext in the mounts table. It never contains a token,
// key or client secret.
func nonSecretConfig(in AddMountInput) map[string]string {
	cfg := map[string]string{}
	switch in.Provider {
	case domain.ProviderS3:
		cfg["endpoint"] = strings.TrimSpace(in.Endpoint)
		cfg["region"] = strings.TrimSpace(in.Region)
		cfg["bucket"] = strings.TrimSpace(in.Bucket)
		cfg["prefix"] = strings.Trim(strings.TrimSpace(in.Prefix), "/")
		if in.PathStyle {
			cfg["pathStyle"] = "true"
		}
	case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime:
		cfg["client_id"] = strings.TrimSpace(in.ClientID)
		if strings.TrimSpace(in.TokenURL) != "" {
			cfg["token_url"] = strings.TrimSpace(in.TokenURL)
		}
		if strings.TrimSpace(in.APIBase) != "" {
			cfg["api_base"] = strings.TrimSpace(in.APIBase)
		}
		if in.Provider == domain.ProviderDrive && strings.TrimSpace(in.RootID) != "" {
			cfg["rootId"] = strings.TrimSpace(in.RootID)
		}
		if in.Provider == domain.ProviderDrime && strings.TrimSpace(in.WorkspaceID) != "" {
			cfg["workspace_id"] = strings.TrimSpace(in.WorkspaceID)
		}
	case domain.ProviderLocal:
		abs, _ := filepath.Abs(strings.TrimSpace(in.LocalPath))
		cfg["root"] = abs
	}
	return cfg
}

// validateS3BucketPrefix rejects the two bucket/prefix mistakes
// that both produce the same symptom (an empty-looking bucket root):
// a bucket name containing "/" and a prefix that repeats the bucket
// name ("mybucket" or "mybucket/..."). Both the wizard (frontend)
// and mountConfig / UpdateMountRoot (backend) call this, so a bad
// value cannot slip in from either side.
func validateS3BucketPrefix(bucket, prefix string) error {
	if bucket == "" {
		return fmt.Errorf("app: S3 需要填桶名（Bucket）")
	}
	if strings.Contains(bucket, "/") {
		return fmt.Errorf("app: 桶名里不能有 /：桶名就是 %q 这样一个名字，桶里面的子文件夹请填到“桶内前缀”里", bucket)
	}
	if prefix == bucket {
		return fmt.Errorf("app: 桶内前缀不能就是桶名本身：前缀是桶里面的子文件夹，例如 photos/2024；想挂整个桶就把前缀留空")
	}
	if strings.HasPrefix(prefix, bucket+"/") {
		return fmt.Errorf("app: 桶内前缀不要把桶名再填一遍：前缀是桶里面的子文件夹，例如 photos/2024；想挂整个桶就把前缀留空")
	}
	return nil
}

// rootRefFor is the driver ref of the mount root. S3, Dropbox and
// Drive all use "" (provider root); local uses the absolute path.
// S3 used to return the bucket (or bucket/prefix) here, which the
// browse/sync/upload code then passed to p.List as a key prefix —
// the bucket root showed an empty "bucket" folder instead of its
// real files, and uploads landed under bucket/bucket/ (2026-11-25).
func rootRefFor(in AddMountInput) string {
	switch in.Provider {
	case domain.ProviderLocal:
		abs, _ := filepath.Abs(strings.TrimSpace(in.LocalPath))
		return abs
	default:
		return ""
	}
}

// rootLabelFor is the display root shown in Settings: S3 shows the
// bucket (or bucket/prefix), local shows the absolute path,
// Dropbox/Drive show nothing. Display only — never pass it to a
// driver as a ref.
func rootLabelFor(in AddMountInput) string {
	switch in.Provider {
	case domain.ProviderS3:
		bucket := strings.TrimSpace(in.Bucket)
		if prefix := strings.Trim(strings.TrimSpace(in.Prefix), "/"); prefix != "" {
			return bucket + "/" + prefix
		}
		return bucket
	case domain.ProviderLocal:
		abs, _ := filepath.Abs(strings.TrimSpace(in.LocalPath))
		return abs
	default:
		return ""
	}
}

// healedRoot recomputes the (RootRef, RootLabel) a persisted mount
// should have from its stored non-secret config. ok is false when
// the config does not carry enough to recompute (e.g. an S3 row
// without a bucket): the caller then leaves the row untouched.
func healedRoot(provider domain.ProviderKind, cfg map[string]string) (rootRef, rootLabel string, ok bool) {
	switch provider {
	case domain.ProviderS3:
		bucket := strings.TrimSpace(cfg["bucket"])
		if bucket == "" {
			return "", "", false
		}
		prefix := strings.Trim(strings.TrimSpace(cfg["prefix"]), "/")
		label := bucket
		if prefix != "" {
			label = bucket + "/" + prefix
		}
		return "", label, true
	case domain.ProviderLocal:
		root := strings.TrimSpace(cfg["root"])
		if root == "" {
			return "", "", false
		}
		return root, root, true
	default:
		return "", "", true
	}
}

// LoadMounts rebuilds every persisted mount after a restart: the
// mount row gives metadata + non-secret config, the secrets table
// gives the encrypted credentials (decrypted in memory only). A
// mount whose provider cannot be rebuilt (bad/expired credentials,
// missing master key) is kept in the list with status "error" and no
// provider, so the user still sees it and can remove or re-add it —
// it is never silently dropped. Call once at startup, after the
// master key store is attached.
func (a *App) LoadMounts() error {
	if a.DB == nil {
		return nil
	}
	persisted, err := a.DB.ListMounts()
	if err != nil {
		return err
	}
	for _, pm := range persisted {
		m := pm.Mount
		// Self-heal (2026-11-25, S3 root fix): mounts saved before
		// the fix carry RootRef "bucket" or "bucket/prefix", which
		// the browse/sync/upload code treats as a key prefix — the
		// bucket root then looks empty and uploads land under
		// bucket/bucket/. Recompute from the stored config
		// (bucket/prefix are untouched by the bug) and write the
		// corrected row back, so the user's existing R2 mount just
		// works on the next start with no delete-and-re-add. A row
		// whose own prefix repeats the bucket name cannot be healed
		// this way — that needs the Settings "修改桶 / 前缀" entry.
		if wantRef, wantLabel, ok := healedRoot(m.Provider, pm.Config); ok && (m.RootRef != wantRef || m.RootLabel != wantLabel) {
			m.RootRef = wantRef
			m.RootLabel = wantLabel
			m.UpdatedAt = time.Now().UTC()
			// Best effort: a failed write-back only means the heal
			// retries on the next start; the in-memory mount is
			// already corrected.
			_ = a.DB.SaveMount(m, pm.Config)
		}
		cfg := storage.Config{
			Endpoint: pm.Config["endpoint"],
			Region:   pm.Config["region"],
			Extra:    pm.Config,
		}
		if a.secrets != nil {
			if ct, err := a.DB.LoadSecret(m.AccountID); err == nil {
				plain, err := a.secrets.Decrypt(ct)
				if err != nil {
					m.Status = domain.MountError
					a.mounts[m.ID] = MountHandle{Mount: m}
					continue
				}
				var creds mountCredentials
				if err := json.Unmarshal(plain, &creds); err != nil {
					m.Status = domain.MountError
					a.mounts[m.ID] = MountHandle{Mount: m}
					continue
				}
				cfg.AccessKey = creds.AccessKey
				cfg.SecretKey = creds.SecretKey
				cfg.Token = creds.AccessToken
				cfg.Extra["refresh_token"] = creds.RefreshToken
				cfg.Extra["client_secret"] = creds.ClientSecret
			} else if !errors.Is(err, sql.ErrNoRows) {
				m.Status = domain.MountError
				a.mounts[m.ID] = MountHandle{Mount: m}
				continue
			}
			// No secret row: a local mount (no credentials). Fine.
		} else if m.Provider != domain.ProviderLocal {
			m.Status = domain.MountError
			a.mounts[m.ID] = MountHandle{Mount: m}
			continue
		}
		p, err := a.Registry.New(m.Provider, cfg)
		if err != nil {
			m.Status = domain.MountError
			a.mounts[m.ID] = MountHandle{Mount: m}
			continue
		}
		m.Caps = p.Caps()
		if m.Status != domain.MountOK {
			m.Status = domain.MountOK
		}
		a.mounts[m.ID] = MountHandle{Mount: m, Provider: p}
	}
	return nil
}

// ListMounts returns the registered mounts in creation order.
func (a *App) ListMounts() []*domain.Mount {
	out := make([]*domain.Mount, 0, len(a.mounts))
	for _, h := range a.mounts {
		out = append(out, h.Mount)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// RemoveMount unregisters a mount and deletes its local records
// (mount row, cached listings, encrypted credential blob). It never
// deletes cloud data: the files stay where they are.
func (a *App) RemoveMount(id string) error {
	if _, ok := a.mounts[id]; !ok {
		return fmt.Errorf("app: unknown mount %q", id)
	}
	delete(a.mounts, id)
	if a.DB != nil {
		if err := a.DB.DeleteMount(id); err != nil {
			return err
		}
		// Best effort: a local mount has no secret row.
		_ = a.DB.DeleteSecret(secretIDFor(id))
	}
	return nil
}

// UpdateMountRoot changes only an S3 mount's bucket/prefix — never
// its credentials. It rebuilds the driver from the stored endpoint /
// region / credentials plus the new bucket/prefix, probes List("")
// against the new root, and only then swaps the live provider and
// persists. A failed probe (or validation) changes nothing: the old
// bucket/prefix and the old provider stay in place, and the caller
// gets a plain-language error.
func (a *App) UpdateMountRoot(ctx context.Context, id, bucket, prefix string) (*domain.Mount, error) {
	h, ok := a.mounts[id]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", id)
	}
	if h.Mount.Provider != domain.ProviderS3 {
		return nil, fmt.Errorf("app: 只有 S3 网盘能改桶 / 前缀")
	}
	bucket = strings.TrimSpace(bucket)
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if err := validateS3BucketPrefix(bucket, prefix); err != nil {
		return nil, err
	}
	if a.DB == nil {
		return nil, fmt.Errorf("app: 本地数据库还没初始化，改不了桶 / 前缀")
	}
	cfgMap, err := a.persistedConfig(id)
	if err != nil {
		return nil, err
	}
	newCfg := map[string]string{}
	for k, v := range cfgMap {
		newCfg[k] = v
	}
	newCfg["bucket"] = bucket
	newCfg["prefix"] = prefix
	cfg := storage.Config{
		Endpoint: newCfg["endpoint"],
		Region:   newCfg["region"],
		Extra:    newCfg,
	}
	if a.secrets != nil {
		creds, found, err := a.decryptMountCreds(h.Mount.AccountID)
		if err != nil {
			return nil, fmt.Errorf("app: 读保存的凭证失败：%w", err)
		}
		if found {
			cfg.AccessKey = creds.AccessKey
			cfg.SecretKey = creds.SecretKey
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p, err := a.Registry.New(domain.ProviderS3, cfg)
	if err != nil {
		return nil, fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	if _, err := p.List(probeCtx, ""); err != nil {
		return nil, humanProbeError(domain.ProviderS3, err)
	}
	label := bucket
	if prefix != "" {
		label = bucket + "/" + prefix
	}
	h.Provider = p
	h.Mount.RootRef = ""
	h.Mount.RootLabel = label
	h.Mount.UpdatedAt = time.Now().UTC()
	a.mounts[id] = h
	if err := a.DB.SaveMount(h.Mount, newCfg); err != nil {
		return nil, err
	}
	return h.Mount, nil
}

// persistedConfig returns the stored non-secret config of one mount.
func (a *App) persistedConfig(id string) (map[string]string, error) {
	persisted, err := a.DB.ListMounts()
	if err != nil {
		return nil, err
	}
	for _, pm := range persisted {
		if pm.Mount.ID == id {
			cfg := pm.Config
			if cfg == nil {
				cfg = map[string]string{}
			}
			return cfg, nil
		}
	}
	return nil, fmt.Errorf("app: unknown mount %q", id)
}

// decryptMountCreds decrypts one mount's stored credential blob.
// found is false when the mount has no secret row (local mounts).
func (a *App) decryptMountCreds(accountID string) (mountCredentials, bool, error) {
	var creds mountCredentials
	ct, err := a.DB.LoadSecret(accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return creds, false, nil
		}
		return creds, false, err
	}
	plain, err := a.secrets.Decrypt(ct)
	if err != nil {
		return creds, false, err
	}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return creds, false, err
	}
	return creds, true, nil
}

// UpdateMountGroup changes a mount's sidebar group, in memory and in
// SQLite, so the group survives a restart together with the mount.
func (a *App) UpdateMountGroup(id, group string) error {
	h, ok := a.mounts[id]
	if !ok {
		return fmt.Errorf("app: unknown mount %q", id)
	}
	h.Mount.Group = group
	h.Mount.UpdatedAt = time.Now().UTC()
	a.mounts[id] = h
	if a.DB != nil {
		return a.DB.UpdateMountGroup(id, group)
	}
	return nil
}

// humanProbeError turns a probe failure into plain language for the
// wizard. It never includes a credential value: probe errors carry
// none, but the mapping only uses status/substring signals.
func humanProbeError(provider domain.ProviderKind, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "rate limited (429)"):
		return fmt.Errorf("app: 对方限流了（429）：请等一会儿再试，不要频繁重试——Drime 限流后可能封一段时间，越试越久")
	case strings.Contains(msg, "401"), strings.Contains(msg, "invalid_access_token"),
		strings.Contains(msg, "invalid_token"), strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "token expired"), strings.Contains(msg, "invalid_grant"):
		return fmt.Errorf("app: 授权失败：token 无效或已过期，请重新授权后再试")
	case strings.Contains(msg, "403"), strings.Contains(msg, "SignatureDoesNotMatch"),
		strings.Contains(msg, "InvalidAccessKeyId"), strings.Contains(msg, "AccessDenied"):
		if provider == domain.ProviderS3 {
			return fmt.Errorf("app: S3 拒绝访问：AccessKey/SecretKey 不对，或该账号没有这个桶的权限")
		}
		return fmt.Errorf("app: 没有权限：请检查账号授权范围是否包含读取文件")
	case strings.Contains(msg, "404"), strings.Contains(msg, "NoSuchBucket"),
		strings.Contains(msg, "not_found"), strings.Contains(msg, "path/not_found"):
		if provider == domain.ProviderS3 {
			return fmt.Errorf("app: 找不到这个桶：检查桶名和区域/Endpoint 是否写对")
		}
		return fmt.Errorf("app: 找不到这个位置：检查路径或文件夹是否写对")
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "no such file or directory"),
		strings.Contains(msg, "does not exist"):
		if provider == domain.ProviderLocal {
			return fmt.Errorf("app: 本地文件夹不存在，请检查路径")
		}
		return fmt.Errorf("app: 找不到这个主机：检查 Endpoint 地址是否写对")
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("app: 连接被拒绝：对方服务没在运行，或地址/端口不对")
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "Client.Timeout"),
		strings.Contains(msg, "deadline exceeded"):
		return fmt.Errorf("app: 连接超时：网络不通或对方没有响应")
	default:
		return fmt.Errorf("app: 连接测试失败：%s", msg)
	}
}

// SetSecrets attaches the master-key store used to encrypt mount
// credentials at rest. Called once at startup from the config dir's
// master.key; tests attach a throwaway store instead.
func (a *App) SetSecrets(s *auth.SecretsStore) { a.secrets = s }
