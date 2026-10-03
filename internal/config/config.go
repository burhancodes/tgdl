// Package config loads and validates process configuration from the
// environment (optionally seeded from a .env file).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds every runtime setting. Always construct through Load.
type Config struct {
	// Telegram credentials.
	APIID    int
	APIHash  string
	BotToken string
	// BotAPIURL points at a self-hosted telegram-bot-api server started with
	// --local. It is required for transfers above the 50MB cloud Bot API limit.
	BotAPIURL string

	// Upload hosts.
	PixeldrainAPIKey string
	PixeldrainDomain string
	GofileAPIKey     string
	GofileBypassHost string
	AllowSharedKeys  bool
	AllowPrivateURLs bool
	ShowSystemStats  bool
	ForceIPv6        bool
	SourceAddress    string

	// APK patcher keystore.
	KeystorePath string
	KeystorePass string
	KeyAlias     string
	KeyPass      string

	// Storage.
	DataDir string
	AuthDir string
	LogDir  string

	// Google Drive.
	GDriveTokenPath   string
	GDriveAccountsDir string
	UseServiceAccts   bool

	// MEGA.
	MegaEmail    string
	MegaPassword string

	// gallery-dl.
	GDLConfigPath      string
	GDLSleepMin        float64
	GDLSleepMax        float64
	GDLSleepRequest    string
	GDLRetries         int
	GDLMaxRunRetries   int
	GDLBackoffBase     time.Duration
	GDLBackoffMultiple float64

	// cyberdrop-dl.
	CDLConfigPath      string
	CDLRetries         int
	CDLMaxRunRetries   int
	CDLBackoffBase     time.Duration
	CDLBackoffMultiple float64

	GlobalSpeedLimit string
	HLSTimeout       time.Duration

	// Telegram upload pacing.
	TGUploadDelayMin   time.Duration
	TGUploadDelayMax   time.Duration
	TGBatchSize        int
	TGBatchCooldown    time.Duration
	TGUploadMaxRetries int
	MaxConcurrentDL    int
	MaxConcurrentUL    int

	// Torrents.
	TorrentTimeout time.Duration
	SearchLimit    int
	MagnetioURL    string
	MagnetioSecret string

	// Access control.
	OwnerID       int64
	HasOwner      bool
	AuthorizedIDs []int64

	// Limits.
	MaxJobsPerChat       int
	MaxTotalDownloadsB   int64
	HasMaxTotalDownloads bool

	LogLevel  string
	LogFormat string
}

const defaultOwnerID int64 = 1623457379

// Load reads .env (if present) and the process environment.
func Load() (*Config, error) {
	_ = godotenv.Load() // an absent .env is fine

	c := &Config{
		APIID:     envInt("TG_API_ID", 0),
		APIHash:   envStr("TG_API_HASH", ""),
		BotToken:  envStr("TG_BOT_TOKEN", ""),
		BotAPIURL: envStr("TG_BOT_API_URL", ""),

		PixeldrainAPIKey: envStr("PIXELDRAIN_API_KEY", ""),
		PixeldrainDomain: pixeldrainDomain(envStr("PIXELDRAIN_DOMAIN", "pixeldrain.com")),
		GofileAPIKey:     envStr("GOFILE_API_KEY", ""),
		GofileBypassHost: envStr("GOFILE_BYPASS_HOST", "gf.1drv.eu.org"),
		AllowSharedKeys:  envBool("ALLOW_SHARED_UPLOAD_KEYS", false),
		AllowPrivateURLs: envBool("ALLOW_PRIVATE_NETWORK_URLS", false),
		ShowSystemStats:  envBool("SHOW_SYSTEM_STATS_ON_JOB_CARD", true),
		ForceIPv6:        envBool("FORCE_IPV6", false),
		SourceAddress:    envStr("SOURCE_ADDRESS", ""),

		KeystorePath: envStr("KEYSTORE_PATH", ""),
		KeystorePass: envStr("KEYSTORE_PASS", ""),
		KeyAlias:     envStr("KEY_ALIAS", ""),
		KeyPass:      envStr("KEY_PASS", ""),

		DataDir: envStr("DATA_DIR", "./data"),
		AuthDir: envStr("AUTH_DIR", "./auth"),
		LogDir:  envStr("LOG_DIR", "./logs"),

		GDriveTokenPath:   envStr("GDRIVE_TOKEN_PATH", "./auth/token.json"),
		GDriveAccountsDir: envStr("GDRIVE_ACCOUNTS_DIR", "./auth/accounts"),
		UseServiceAccts:   envBool("USE_SERVICE_ACCOUNTS", true),

		MegaEmail:    envStr("MEGA_EMAIL", ""),
		MegaPassword: envStr("MEGA_PASSWORD", ""),

		GDLConfigPath:      envStr("GDL_CONFIG_PATH", "./configs/gallery-dl.conf"),
		GDLSleepMin:        envFloat("GDL_SLEEP_MIN", 1.5),
		GDLSleepMax:        envFloat("GDL_SLEEP_MAX", 4.0),
		GDLSleepRequest:    envStr("GDL_SLEEP_REQUEST", "1-3"),
		GDLRetries:         envInt("GDL_RETRIES", 4),
		GDLMaxRunRetries:   envInt("GDL_MAX_RUN_RETRIES", 3),
		GDLBackoffBase:     envSeconds("GDL_BACKOFF_BASE_S", 30),
		GDLBackoffMultiple: envFloat("GDL_BACKOFF_MULTIPLIER", 2.5),

		CDLConfigPath:      envStr("CDL_CONFIG_PATH", "./configs/cyberdrop-dl.yaml"),
		CDLRetries:         envInt("CDL_RETRIES", 3),
		CDLMaxRunRetries:   envInt("CDL_MAX_RUN_RETRIES", 3),
		CDLBackoffBase:     envSeconds("CDL_BACKOFF_BASE_S", 30),
		CDLBackoffMultiple: envFloat("CDL_BACKOFF_MULTIPLIER", 2.5),

		GlobalSpeedLimit: envStr("GLOBAL_DOWNLOAD_SPEED_LIMIT", "20M"),
		HLSTimeout:       envSeconds("HLS_DOWNLOAD_TIMEOUT", 300),

		TGUploadDelayMin:   envSeconds("TG_UPLOAD_DELAY_MIN", 2.0),
		TGUploadDelayMax:   envSeconds("TG_UPLOAD_DELAY_MAX", 4.5),
		TGBatchSize:        envInt("TG_BATCH_SIZE", 30),
		TGBatchCooldown:    envSeconds("TG_BATCH_COOLDOWN_S", 25),
		TGUploadMaxRetries: envInt("TG_UPLOAD_MAX_RETRIES", 3),
		MaxConcurrentDL:    max(1, envInt("TG_MAX_CONCURRENT_DOWNLOADS", 1)),
		MaxConcurrentUL:    max(1, envInt("TG_MAX_CONCURRENT_UPLOADS", 1)),

		TorrentTimeout: envSeconds("TORRENT_TIMEOUT", 120),
		SearchLimit:    envInt("SEARCH_LIMIT", 300),
		MagnetioURL:    envStr("MAGNETIO_RPC_URL", "http://magnetio-scraper:8080/rpc"),
		MagnetioSecret: envStr("MAGNETIO_RPC_SECRET", ""),

		MaxJobsPerChat: envInt("MAX_JOBS_PER_CHAT", 3),

		LogLevel:  strings.ToUpper(envStr("LOG_LEVEL", "INFO")),
		LogFormat: strings.ToLower(envStr("LOG_FORMAT", "text")),
	}

	if raw, ok := os.LookupEnv("OWNER_ID"); !ok {
		c.OwnerID, c.HasOwner = defaultOwnerID, true
	} else if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		c.OwnerID, c.HasOwner = id, true
	}

	c.AuthorizedIDs = parseIDList(os.Getenv("AUTHORIZED_USER_IDS"))

	if raw := strings.TrimSpace(os.Getenv("MAX_TOTAL_DOWNLOADS_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MAX_TOTAL_DOWNLOADS_BYTES: %w", err)
		}
		c.MaxTotalDownloadsB, c.HasMaxTotalDownloads = n, true
	}

	switch c.LogLevel {
	case "DEBUG", "INFO", "WARNING", "ERROR":
	default:
		c.LogLevel = "INFO"
	}

	// Absolute paths matter: several tools run with a different working directory.
	for _, p := range []*string{&c.DataDir, &c.AuthDir, &c.LogDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", *p, err)
		}
		*p = abs
	}
	for _, d := range []string{c.DataDir, c.AuthDir, c.LogDir, c.DownloadsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", d, err)
		}
	}
	return c, nil
}

// UploadLimit is the largest single file (bytes) the configured Bot API
// endpoint accepts: ~2000MB via a self-hosted --local server, otherwise the
// 50MB cloud limit (with headroom).
func (c *Config) UploadLimit() int64 {
	if c.BotAPIURL != "" {
		return 195 * 1024 * 1024 * 1024 / 100
	}
	return 49 * 1024 * 1024
}

// Validate reports missing mandatory Telegram credentials.
func (c *Config) Validate() error {
	var missing []string
	if c.APIID == 0 {
		missing = append(missing, "TG_API_ID")
	}
	if c.APIHash == "" {
		missing = append(missing, "TG_API_HASH")
	}
	if c.BotToken == "" {
		missing = append(missing, "TG_BOT_TOKEN")
	}
	if len(missing) > 0 {
		return errors.New("missing required environment variables: " + strings.Join(missing, ", "))
	}
	return nil
}

func (c *Config) DBPath() string         { return filepath.Join(c.DataDir, "state.sqlite3") }
func (c *Config) GDLArchivePath() string { return filepath.Join(c.DataDir, "gdl_archive.sqlite3") }
func (c *Config) CDLArchivePath() string { return filepath.Join(c.DataDir, "cdl_archive.db") }
func (c *Config) DownloadsDir() string   { return filepath.Join(c.DataDir, "downloads") }

// UserDir returns the per-user auth directory, creating it on demand.
func (c *Config) UserDir(userID int64) (string, error) {
	dir := filepath.Join(c.AuthDir, strconv.FormatInt(userID, 10))
	return dir, os.MkdirAll(dir, 0o700)
}

// KeystoreInfo describes a JKS keystore used for APK signing.
type KeystoreInfo struct {
	Path      string
	StorePass string
	KeyAlias  string
	KeyPass   string
}

// UserKeystore resolves a per-user keystore, then the global one, then the
// conventional fallback locations. It returns nil when none is configured.
func (c *Config) UserKeystore(userID int64) *KeystoreInfo {
	if userID > 0 {
		dir := filepath.Join(c.AuthDir, strconv.FormatInt(userID, 10))
		if info := readUserKeystore(dir); info != nil {
			return info
		}
	}
	var candidates []string
	if c.KeystorePath != "" {
		candidates = append(candidates, c.KeystorePath)
	}
	candidates = append(candidates,
		filepath.Join(c.AuthDir, "keystore.jks"),
		filepath.Join(c.DataDir, "keystore.jks"))
	for _, p := range candidates {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.Mode().IsRegular() {
			kp := c.KeyPass
			if kp == "" {
				kp = c.KeystorePass
			}
			return &KeystoreInfo{Path: abs, StorePass: c.KeystorePass, KeyAlias: c.KeyAlias, KeyPass: kp}
		}
	}
	return nil
}

func parseIDList(raw string) []int64 {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func pixeldrainDomain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "pixeldrain.com" || v == "pixeldra.in" {
		return v
	}
	return "pixeldrain.com"
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(envStr(key, "")); err == nil {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(envStr(key, ""), 64); err == nil {
		return v
	}
	return def
}

func envSeconds(key string, def float64) time.Duration {
	return time.Duration(envFloat(key, def) * float64(time.Second))
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(envStr(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
