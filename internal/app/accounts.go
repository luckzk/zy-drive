// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
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
	MegaEmail    string              `json:"megaEmail,omitempty"`    // MEGA account email
	MegaPassword string              `json:"megaPassword,omitempty"` // MEGA account password (never persisted; the exported session is stored instead)
	MegaTwoFactor string             `json:"megaTwoFactor,omitempty"` // MEGA 2FA code (optional, one login only)
	PCloudRegion string              `json:"pcloudRegion,omitempty"` // pCloud region: "auto" | "us" | "eu", default "auto"
	// SkipProbe (C, one-page form): save without a connection
	// probe. The mount is then stored Verified=false ("未验证")
	// and the user re-tests it later from Settings. When false
	// (the default, and the only path the OAuth wizard uses) the
	// probe still gates the save exactly as before.
	SkipProbe bool `json:"skipProbe,omitempty"`
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
	// MEGA exported session (v0.9.1): the session ID + base64
	// master key, exported after a password login and stored
	// encrypted at rest. Restarts log in with LoginWithKeys —
	// the password itself is NEVER stored.
	MegaSession   string `json:"megaSession,omitempty"`
	MegaMasterKey string `json:"megaMasterKey,omitempty"`
}

func (c mountCredentials) empty() bool {
	return c == mountCredentials{}
}

// secretIDFor returns the secrets-table id for a mount. It is also
// stored as Mount.AccountID, so the mount always names its own
// credential reference and never a secret value.
func secretIDFor(mountID string) string { return "mount:" + mountID }

// mountRuntime fills the per-mount runtime fields of a driver
// Config: the mount id (resume sessions are keyed by it) and the
// SQLite store they persist in. Call it at every site that builds
// a provider for a SAVED mount (LoadMounts, TestMount,
// UpdateMountCredentials, UpdateMountRoot, ImportBundle, and the
// wizard's AddMountWithCredentials once the id is minted). Probe
// paths for not-yet-saved mounts (TestMountConfig) leave it
// unset — and so does the driver's resume, which is exactly
// right: a probe never uploads. A nil DB (tests) also disables
// resume, never a crash.
func (a *App) mountRuntime(cfg *storage.Config, mountID string) {
	cfg.MountID = mountID
	cfg.ResumeStore = a.DB
}

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
//
// SkipProbe (C, one-page form) changes only the last step: the
// mount is saved WITHOUT a probe — UNLESS the same config passed
// TestMountConfig in the last 5 minutes (the wizard's
// in-component test, step 2): then it is stored Verified=true
// without probing again, so the wizard never tests twice. Any
// other SkipProbe save is marked Verified=false and the user
// re-tests it later from Settings.
func (a *App) AddMountWithCredentials(ctx context.Context, in AddMountInput) (*domain.Mount, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, fmt.Errorf("app: 请给网盘起个名字")
	}
	switch in.Provider {
	case domain.ProviderS3, domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderOneDrive, domain.ProviderBox, domain.ProviderPCloud, domain.ProviderMega, domain.ProviderLocal:
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
	// Mint the id BEFORE building the provider: the driver needs
	// it (MountID + ResumeStore) to persist per-mount state such
	// as Drime resume sessions, and the probe builds the very
	// provider that becomes the live one after save.
	now := time.Now().UTC()
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, fmt.Errorf("app: add mount: %w", err)
	}
	id := "mnt-" + hex.EncodeToString(idBytes[:])
	a.mountRuntime(&cfg, id)
	// Probe first (unless the caller explicitly asked to save
	// unverified): one cheap List of the root. S3 lists the bucket
	// root, Dropbox/Drive list their root folder, local lists the
	// chosen directory. Any auth/network/path failure aborts here,
	// before anything is persisted.
	verified := false
	p, err := a.Registry.New(in.Provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	if in.SkipProbe {
		// Saved without a fresh probe on purpose: the provider
		// is built (bad config still fails here) but never
		// probed — unless the same config was just tested OK
		// in-component (see TestMountConfig): then the earlier
		// probe is what verifies it.
		verified = a.testedRecently(in.Provider, cfg, creds)
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := probeRoot(probeCtx, p); err != nil {
			return nil, humanProbeError(in.Provider, err)
		}
		verified = true
	}
	m := &domain.Mount{
		ID:           id,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		Provider:     in.Provider,
		AccountID:    secretIDFor(id),
		RootRef:      rootRefFor(in),
		RootLabel:    rootLabelFor(in),
		WorkspaceLabel: workspaceLabelFor(in),
		EndpointHost: endpointHost(in.Endpoint),
		Group:        strings.TrimSpace(in.Group),
		Caps:         p.Caps(),
		Status:       domain.MountOK,
		Verified:     verified,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	// New mounts go at the end of their group (sort_order =
	// max+1 there; the first mount in a group gets 0). The same
	// value is what upsertMount persists, so memory and SQLite
	// agree without a reload.
	sortOrder := -1
	for _, h := range a.mounts {
		if h.Mount.Group == m.Group && h.Mount.SortOrder > sortOrder {
			sortOrder = h.Mount.SortOrder
		}
	}
	m.SortOrder = sortOrder + 1
	// MEGA: the probe just logged in with the password — export
	// the session now and store THAT, never the password (see
	// exportMegaSession). A failed export aborts the save.
	if in.Provider == domain.ProviderMega {
		if err := exportMegaSession(ctx, p, &creds); err != nil {
			return nil, err
		}
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
	case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderOneDrive, domain.ProviderBox, domain.ProviderPCloud:
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
		if in.Provider == domain.ProviderPCloud {
			// The region is the user's choice ("auto" | "us" |
			// "eu"); the driver resolves the host from it
			// ("auto" = probe both hosts on first use).
			region := strings.ToLower(strings.TrimSpace(in.PCloudRegion))
			if region == "" {
				region = "auto"
			}
			cfg.Extra["pcloud_region"] = region
			switch region {
			case "us":
				cfg.Extra["pcloud_host"] = "api.pcloud.com"
			case "eu":
				cfg.Extra["pcloud_host"] = "eapi.pcloud.com"
			default:
				cfg.Extra["pcloud_host"] = ""
			}
		}
	case domain.ProviderMega:
		// MEGA logs in with email + password (optionally a 2FA
		// code). The password is in-memory only: after the
		// probe, AddMountWithCredentials / UpdateMountCredentials
		// export the session and store THAT (see
		// exportMegaSession) — the password never reaches disk.
		if strings.TrimSpace(in.MegaEmail) == "" || strings.TrimSpace(in.MegaPassword) == "" {
			return cfg, creds, fmt.Errorf("app: MEGA 需要填邮箱和密码")
		}
		cfg.Extra["mega_email"] = strings.TrimSpace(in.MegaEmail)
		cfg.Extra["mega_password"] = in.MegaPassword
		if strings.TrimSpace(in.MegaTwoFactor) != "" {
			cfg.Extra["mega_twofactor"] = strings.TrimSpace(in.MegaTwoFactor)
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

// exportMegaSession stores a MEGA provider's exported session
// into the credential blob (in place of the password, which is
// never persisted). It is called after a successful probe in
// AddMountWithCredentials and UpdateMountCredentials: the login
// just succeeded, so the export is a local read of the session
// ID + master key — if it fails, the mount is NOT saved, because
// without a stored session it could never survive a restart
// (the password is not stored, so there would be nothing to log
// in with next time).
func exportMegaSession(ctx context.Context, p storage.Provider, creds *mountCredentials) error {
	se, ok := p.(storage.SessionExporter)
	if !ok {
		return fmt.Errorf("app: MEGA 驱动不支持导出会话")
	}
	sid, secret, err := se.ExportSession(ctx)
	if err != nil {
		return fmt.Errorf("app: MEGA 登录成功了，但没能保存会话（%v），请重试", err)
	}
	creds.MegaSession = sid
	creds.MegaMasterKey = secret
	return nil
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
	case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderOneDrive, domain.ProviderBox, domain.ProviderPCloud:
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
		if in.Provider == domain.ProviderPCloud {
			// The region (the user's choice) and the resolved
			// host (what the driver reads) are both non-secret.
			region := strings.ToLower(strings.TrimSpace(in.PCloudRegion))
			if region == "" {
				region = "auto"
			}
			cfg["pcloud_region"] = region
			switch region {
			case "us":
				cfg["pcloud_host"] = "api.pcloud.com"
			case "eu":
				cfg["pcloud_host"] = "eapi.pcloud.com"
			default:
				cfg["pcloud_host"] = ""
			}
		}
	case domain.ProviderMega:
		// The email is an identifier, not a secret (like a
		// client_id): it is stored so the re-auth dialog can
		// prefill it. The password and the 2FA code are never
		// stored — only the exported session (in the secrets
		// blob) survives a restart.
		cfg["mega_email"] = strings.TrimSpace(in.MegaEmail)
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

// workspaceLabelFor is the display label of the Drime workspace an
// AddMountInput binds to: "个人空间" when no workspace id was given
// (workspace 0), "工作区 <id>" otherwise. "" for non-Drime mounts.
// The label is display only, but it is also the quota's scope —
// the Drime quota endpoint returns the usage of exactly this
// workspace, so the UI always shows the label next to the numbers.
func workspaceLabelFor(in AddMountInput) string {
	if in.Provider != domain.ProviderDrime {
		return ""
	}
	if id := strings.TrimSpace(in.WorkspaceID); id != "" {
		return "工作区 " + id
	}
	return "个人空间"
}

// workspaceLabelFromConfig is workspaceLabelFor over a persisted
// non-secret config map (LoadMounts path).
func workspaceLabelFromConfig(provider domain.ProviderKind, cfg map[string]string) string {
	if provider != domain.ProviderDrime {
		return ""
	}
	if id := strings.TrimSpace(cfg["workspace_id"]); id != "" {
		return "工作区 " + id
	}
	return "个人空间"
}

// endpointHost returns the host part of an endpoint URL ("" when
// empty or unparseable). Display only: the sidebar picks the R2
// (Cloudflare) or AWS S3 brand mark from it. The host is the public
// endpoint URL, never a credential.
func endpointHost(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
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
		// Display-only endpoint host, recomputed from the stored
		// non-secret config on every load (older rows never had
		// the field; nothing is persisted for it here).
		m.EndpointHost = endpointHost(pm.Config["endpoint"])
		// Workspace label (Drime only): recomputed from the stored
		// non-secret config on every load, so mounts saved before
		// this field existed still show their workspace.
		m.WorkspaceLabel = workspaceLabelFromConfig(m.Provider, pm.Config)
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
		a.mountRuntime(&cfg, m.ID)
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
				// MEGA: the stored session (never the password).
				// A mount saved before v0.9.1 has no session and
				// the driver then refuses to build — the user
				// re-authorizes it once from Settings.
				if creds.MegaSession != "" {
					cfg.Extra["mega_session_id"] = creds.MegaSession
				}
				if creds.MegaMasterKey != "" {
					cfg.Extra["mega_master_key"] = creds.MegaMasterKey
				}
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
			// Log the real reason: the UI only shows a status,
			// so without this line the cause (e.g. an expired
			// MEGA session) is invisible anywhere. Provider
			// errors never contain credential values, so this
			// is safe to log.
			log.Printf("mounts: mount=%s provider=%s: %v", m.ID, m.Provider, err)
			if errors.Is(err, storage.ErrSessionExpired) {
				// The stored session is dead; only a fresh
				// login revives the mount. The UI already
				// renders this status as "授权过期了，重新授权
				// 后就能继续浏览。"
				m.Status = domain.MountAuthExpired
			} else {
				m.Status = domain.MountError
			}
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
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// RemoveMount unregisters a mount and deletes its local records
// (mount row, cached listings, encrypted credential blob). It never
// deletes cloud data: the files stay where they are. The rest of
// the group closes ranks, in memory and in SQLite, so sort_order
// stays dense.
func (a *App) RemoveMount(id string) error {
	h, ok := a.mounts[id]
	if !ok {
		return fmt.Errorf("app: unknown mount %q", id)
	}
	group, sortOrder := h.Mount.Group, h.Mount.SortOrder
	delete(a.mounts, id)
	if a.DB != nil {
		if err := a.DB.DeleteMount(id); err != nil {
			return err
		}
		// Best effort: a local mount has no secret row.
		_ = a.DB.DeleteSecret(secretIDFor(id))
	}
	for _, h2 := range a.mounts {
		if h2.Mount.Group == group && h2.Mount.SortOrder > sortOrder {
			h2.Mount.SortOrder--
		}
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
	a.mountRuntime(&cfg, id)
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

// UpdateMountCredentials re-authorizes an existing mount IN PLACE
// (V0.9.0, item 3): when a token expires or the user switches
// accounts, the new credentials are validated and probed first; only
// on success are the live provider, the encrypted credential blob
// and the non-secret config swapped — in one SQLite transaction.
// The mount ID, sync jobs, group, cached baselines and (for the
// same account) cached listings all survive: the user never has to
// delete-and-re-add the mount, and no task silently re-points.
// A failed probe changes nothing: the old provider and the old
// credentials stay exactly as they were.
//
// The wizard prefills name/group from the mount, so a re-auth that
// only swaps the token round-trips them unchanged; an explicit new
// name/group in the input is applied. For S3 the bucket/prefix in
// the input also take effect (re-auth doubles as a root change);
// for Drime the workspace id in the input takes effect.
func (a *App) UpdateMountCredentials(ctx context.Context, id string, in AddMountInput) (*domain.Mount, error) {
	h, ok := a.mounts[id]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", id)
	}
	if in.Provider != h.Mount.Provider {
		return nil, fmt.Errorf("app: 这个网盘是 %s，不能用 %s 的凭证重新授权", h.Mount.Provider, in.Provider)
	}
	if in.Provider == domain.ProviderLocal {
		return nil, fmt.Errorf("app: 本地文件夹没有凭证可以更新")
	}
	if a.secrets == nil {
		return nil, fmt.Errorf("app: 凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	cfg, creds, err := a.mountConfig(in)
	if err != nil {
		return nil, err
	}
	a.mountRuntime(&cfg, id)
	p, err := a.Registry.New(in.Provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	verified := false
	if in.SkipProbe {
		verified = a.testedRecently(in.Provider, cfg, creds)
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := probeRoot(probeCtx, p); err != nil {
			return nil, humanProbeError(in.Provider, err)
		}
		verified = true
	}
	// MEGA: the probe just logged in with the password — export
	// the session now and store THAT, never the password (see
	// exportMegaSession). A failed export aborts the re-auth.
	if in.Provider == domain.ProviderMega {
		if err := exportMegaSession(ctx, p, &creds); err != nil {
			return nil, err
		}
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("app: 重新授权：%w", err)
	}
	ct, err := a.secrets.Encrypt(plain)
	if err != nil {
		return nil, fmt.Errorf("app: 加密凭证失败：%w", err)
	}
	m := *h.Mount
	if name := strings.TrimSpace(in.DisplayName); name != "" {
		m.DisplayName = name
	}
	m.Group = strings.TrimSpace(in.Group)
	m.RootRef = rootRefFor(in)
	m.RootLabel = rootLabelFor(in)
	m.WorkspaceLabel = workspaceLabelFor(in)
	m.EndpointHost = endpointHost(in.Endpoint)
	m.Caps = p.Caps()
	m.Status = domain.MountOK
	m.Verified = verified
	m.UpdatedAt = time.Now().UTC()
	if a.DB != nil {
		if err := a.DB.SaveMountAndSecret(&m, nonSecretConfig(in), secretIDFor(id), ct); err != nil {
			return nil, err
		}
	}
	a.mounts[id] = MountHandle{Mount: &m, Provider: p}
	return &m, nil
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
// The mount is appended at the end of the new group; moving to the
// same group is a no-op. (Drag-sort uses MoveMount, which places
// the mount at an exact index instead.)
func (a *App) UpdateMountGroup(id, group string) error {
	h, ok := a.mounts[id]
	if !ok {
		return fmt.Errorf("app: unknown mount %q", id)
	}
	if h.Mount.Group == group {
		return nil
	}
	sortOrder := -1
	for _, h2 := range a.mounts {
		if h2.Mount.Group == group && h2.Mount.SortOrder > sortOrder {
			sortOrder = h2.Mount.SortOrder
		}
	}
	h.Mount.Group = group
	h.Mount.SortOrder = sortOrder + 1
	h.Mount.UpdatedAt = time.Now().UTC()
	a.mounts[id] = h
	if a.DB != nil {
		return a.DB.UpdateMountGroup(id, group)
	}
	return nil
}

// MoveMount moves a mount to the index-th position of group
// (0-based, clamped to [0, len(group)]), in memory and in SQLite.
// Inside a group the displaced mounts shift down: dragging the
// 8th mount to the 4th position pushes the old 4th..7th to
// 5th..8th. Across groups the source group closes ranks and the
// target group makes room; moving to an empty group is index 0.
// Moving to the same position is a no-op: nothing is written and
// UpdatedAt is untouched.
func (a *App) MoveMount(ctx context.Context, id, group string, index int) (*domain.Mount, error) {
	_ = ctx
	h, ok := a.mounts[id]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", id)
	}
	// Current members per group, in sort_order order.
	members := map[string][]*domain.Mount{}
	for _, m := range a.ListMounts() {
		members[m.Group] = append(members[m.Group], m)
	}
	srcGroup := h.Mount.Group
	src := members[srcGroup]
	cur := -1
	for i, m := range src {
		if m.ID == id {
			cur = i
			break
		}
	}
	if cur < 0 {
		return nil, fmt.Errorf("app: mount %q not in its group", id)
	}
	dst := members[group]
	if group == srcGroup {
		dst = append(dst[:cur:cur], dst[cur+1:]...)
	}
	if index < 0 {
		index = 0
	}
	if index > len(dst) {
		index = len(dst)
	}
	if group == srcGroup && index == cur {
		return h.Mount, nil
	}
	newDst := make([]*domain.Mount, 0, len(dst)+1)
	newDst = append(newDst, dst[:index]...)
	newDst = append(newDst, h.Mount)
	newDst = append(newDst, dst[index:]...)
	// Renumber the affected groups densely 0..n-1; collect the
	// rows whose (group, sort_order) actually changed.
	now := time.Now().UTC()
	var orders []cache.MountOrder
	renumber := func(g string, list []*domain.Mount) {
		for i, m := range list {
			if m.Group != g || m.SortOrder != i {
				m.Group = g
				m.SortOrder = i
				m.UpdatedAt = now
				orders = append(orders, cache.MountOrder{ID: m.ID, Group: g, SortOrder: i})
			}
		}
	}
	if group == srcGroup {
		renumber(group, newDst)
	} else {
		newSrc := append(src[:cur:cur], src[cur+1:]...)
		renumber(srcGroup, newSrc)
		renumber(group, newDst)
	}
	if a.DB != nil {
		if err := a.DB.UpdateMountOrders(orders); err != nil {
			return nil, err
		}
	}
	return h.Mount, nil
}

// probeRoot is the one cheap connection probe every save/test
// path shares: a single List of the provider root. It never
// persists anything.
func probeRoot(ctx context.Context, p storage.Provider) error {
	_, err := p.List(ctx, "")
	return err
}

// TestMountConfig probes a not-yet-saved mount's config (C,
// one-page form "测试连接"): it validates, builds the provider
// and lists the root, but persists nothing. The caller shows the
// plain-language error and still offers "保存" — saving is the
// user's choice, the test is only information.
func (a *App) TestMountConfig(ctx context.Context, in AddMountInput) error {
	// No display-name check here: a connectivity probe does not
	// need a name — the wizard's in-component test (step 2) runs
	// right after the token exchange, possibly before the user
	// typed a name, and a name error there would read as a
	// connection failure. The name is still required at save
	// (AddMountWithCredentials) and by the wizard's Next gating.
	switch in.Provider {
	case domain.ProviderS3, domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderOneDrive, domain.ProviderBox, domain.ProviderPCloud, domain.ProviderMega, domain.ProviderLocal:
	default:
		return fmt.Errorf("app: %s 还在 V0.2 接入，现在加不了", in.Provider)
	}
	cfg, creds, err := a.mountConfig(in)
	if err != nil {
		return err
	}
	p, err := a.Registry.New(in.Provider, cfg)
	if err != nil {
		return fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := probeRoot(probeCtx, p); err != nil {
		return humanProbeError(in.Provider, err)
	}
	// Remember the pass: a save of this exact config within 5
	// minutes is stored Verified without probing again.
	a.rememberTested(in.Provider, cfg, creds)
	return nil
}

// testMemoKey hashes the probe inputs (provider + driver config
// incl. credentials). DisplayName/Group are deliberately NOT part
// of the key: renaming after a passed test must not void it.
func testMemoKey(provider domain.ProviderKind, cfg storage.Config, creds mountCredentials) string {
	canon, _ := json.Marshal(struct {
		Provider domain.ProviderKind `json:"provider"`
		Config   storage.Config      `json:"config"`
		Creds    mountCredentials    `json:"creds"`
	}{provider, cfg, creds})
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

func (a *App) rememberTested(provider domain.ProviderKind, cfg storage.Config, creds mountCredentials) {
	a.testedMu.Lock()
	defer a.testedMu.Unlock()
	if a.testedOK == nil {
		a.testedOK = map[string]time.Time{}
	}
	a.testedOK[testMemoKey(provider, cfg, creds)] = time.Now()
}

// testedRecently reports whether this exact config passed
// TestMountConfig within the last 5 minutes.
func (a *App) testedRecently(provider domain.ProviderKind, cfg storage.Config, creds mountCredentials) bool {
	a.testedMu.Lock()
	defer a.testedMu.Unlock()
	at, ok := a.testedOK[testMemoKey(provider, cfg, creds)]
	return ok && time.Since(at) <= 5*time.Minute
}

// TestMount re-probes a saved mount (C, Settings → "重新测试"):
// it rebuilds the provider from the stored config + credentials
// and lists the root. On success the mount is marked
// Verified=true and persisted; on failure nothing changes — the
// mount stays "未验证" and the caller gets the plain-language
// error. It never deletes the mount or its credentials.
func (a *App) TestMount(ctx context.Context, id string) (*domain.Mount, error) {
	h, ok := a.mounts[id]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", id)
	}
	if a.DB == nil {
		return nil, fmt.Errorf("app: 本地数据库还没初始化，测不了这个网盘")
	}
	cfgMap, err := a.persistedConfig(id)
	if err != nil {
		return nil, err
	}
	cfg := storage.Config{
		Endpoint: cfgMap["endpoint"],
		Region:   cfgMap["region"],
		Extra:    cfgMap,
	}
	a.mountRuntime(&cfg, id)
	if a.secrets != nil {
		creds, found, err := a.decryptMountCreds(h.Mount.AccountID)
		if err != nil {
			return nil, fmt.Errorf("app: 读保存的凭证失败：%w", err)
		}
		if found {
			cfg.AccessKey = creds.AccessKey
			cfg.SecretKey = creds.SecretKey
			cfg.Token = creds.AccessToken
			cfg.Extra["refresh_token"] = creds.RefreshToken
			cfg.Extra["client_secret"] = creds.ClientSecret
			// MEGA: the stored session (never the password).
			if creds.MegaSession != "" {
				cfg.Extra["mega_session_id"] = creds.MegaSession
			}
			if creds.MegaMasterKey != "" {
				cfg.Extra["mega_master_key"] = creds.MegaMasterKey
			}
		}
	}
	p, err := a.Registry.New(h.Mount.Provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("app: 创建驱动失败：%w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := probeRoot(probeCtx, p); err != nil {
		return nil, humanProbeError(h.Mount.Provider, err)
	}
	h.Provider = p
	h.Mount.Caps = p.Caps()
	h.Mount.Status = domain.MountOK
	h.Mount.Verified = true
	h.Mount.UpdatedAt = time.Now().UTC()
	a.mounts[id] = h
	if err := a.DB.SaveMount(h.Mount, cfgMap); err != nil {
		return nil, err
	}
	return h.Mount, nil
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
		strings.Contains(msg, "not_found"), strings.Contains(msg, "path/not_found"),
		strings.Contains(msg, "itemNotFound"):
		if provider == domain.ProviderS3 {
			return fmt.Errorf("app: 找不到这个桶：检查桶名和区域/Endpoint 是否写对")
		}
		return fmt.Errorf("app: 找不到这个位置：检查路径或文件夹是否写对")
	case strings.Contains(msg, "quotaLimitReached"), strings.Contains(msg, "storageQuotaExceeded"),
		strings.Contains(msg, "insufficientStorage"), strings.Contains(msg, "QuotaExceeded"):
		return fmt.Errorf("app: 空间不足：这个网盘的可用空间不够了，请先清理文件或扩容后再试")
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
