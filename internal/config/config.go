package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	APIID    int
	APIHash  string
	BotToken string

	BotAPIURL string

	BotAPILocalDirMap    string
	LocalHostPrefix      string
	LocalContainerPrefix string

	PixeldrainAPIKey string
	PixeldrainDomain string
	GofileAPIKey     string
	GofileBypassHost string
	AllowSharedKeys  bool
	AllowPrivateURLs bool
	ShowSystemStats  bool
	ForceIPv6        bool
	SourceAddress    string

	DataDir string
	AuthDir string
	LogDir  string

	GDriveTokenPath   string
	GDriveAccountsDir string
	UseServiceAccts   bool

	MegaEmail    string
	MegaPassword string

	GDLSleepMin        float64
	GDLSleepMax        float64
	GDLSleepRequest    string
	GDLRetries         int
	GDLMaxRunRetries   int
	GDLBackoffBase     time.Duration
	GDLBackoffMultiple float64

	CDLRetries         int
	CDLMaxRunRetries   int
	CDLBackoffBase     time.Duration
	CDLBackoffMultiple float64

	GlobalSpeedLimit string
	HLSTimeout       time.Duration

	TGUploadDelayMin   time.Duration
	TGUploadDelayMax   time.Duration
	TGBatchSize        int
	TGBatchCooldown    time.Duration
	TGUploadMaxRetries int
	MaxConcurrentDL    int
	MaxConcurrentUL    int
	MaxConcurrentHeavy int
	AlbumMaxTotalBytes int64

	ExtractThreads   int
	ArchiveThreads   int
	ArchiveZipLevel  int
	MaxExtractBytes  int64
	MaxExtractFiles  int
	MinFreeDiskBytes int64

	BotAPIDataDir string

	MemoryLimitMB int64

	TorrentTimeout      time.Duration
	SearchLimit         int
	MagnetioURL         string
	MagnetioSecret      string
	AriaMaxConcurrentDL int

	OwnerID          int64
	HasOwner         bool
	HasExplicitOwner bool
	AuthorizedIDs    []int64

	MaxJobsPerChat       int
	MaxTotalDownloadsB   int64
	HasMaxTotalDownloads bool

	LogLevel  string
	LogFormat string

	PinStatusMessage bool

	RenameMaxBatch   int
	RenameSessionTTL time.Duration

	SettingsWriteEnv      bool
	SettingsEncryptionKey string
	LogLevelVar           *slog.LevelVar
	mu                    sync.RWMutex
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	c := &Config{
		APIID:             envInt("TG_API_ID", 0),
		APIHash:           envStr("TG_API_HASH", ""),
		BotToken:          envStr("TG_BOT_TOKEN", ""),
		BotAPIURL:         envStr("TG_BOT_API_URL", ""),
		BotAPILocalDirMap: envStr("TG_BOT_API_LOCAL_DIR_MAP", ""),

		PixeldrainAPIKey: envStr("PIXELDRAIN_API_KEY", ""),
		PixeldrainDomain: pixeldrainDomain(envStr("PIXELDRAIN_DOMAIN", "pixeldrain.com")),
		GofileAPIKey:     envStr("GOFILE_API_KEY", ""),
		GofileBypassHost: envStr("GOFILE_BYPASS_HOST", "gf.1drv.eu.org"),
		AllowSharedKeys:  envBool("ALLOW_SHARED_UPLOAD_KEYS", false),
		AllowPrivateURLs: envBool("ALLOW_PRIVATE_NETWORK_URLS", false),
		ShowSystemStats:  envBool("SHOW_SYSTEM_STATS_ON_JOB_CARD", true),
		ForceIPv6:        envBool("FORCE_IPV6", false),
		SourceAddress:    envStr("SOURCE_ADDRESS", ""),

		DataDir: envStr("DATA_DIR", "./data"),
		AuthDir: envStr("AUTH_DIR", "./auth"),
		LogDir:  envStr("LOG_DIR", "./logs"),

		GDriveTokenPath:   envStr("GDRIVE_TOKEN_PATH", "./auth/token.json"),
		GDriveAccountsDir: envStr("GDRIVE_ACCOUNTS_DIR", "./auth/accounts"),
		UseServiceAccts:   envBool("USE_SERVICE_ACCOUNTS", true),

		MegaEmail:    envStr("MEGA_EMAIL", ""),
		MegaPassword: envStr("MEGA_PASSWORD", ""),

		GDLSleepMin:        envFloat("GDL_SLEEP_MIN", 1.5),
		GDLSleepMax:        envFloat("GDL_SLEEP_MAX", 4.0),
		GDLSleepRequest:    envStr("GDL_SLEEP_REQUEST", "1-3"),
		GDLRetries:         envInt("GDL_RETRIES", 4),
		GDLMaxRunRetries:   envInt("GDL_MAX_RUN_RETRIES", 3),
		GDLBackoffBase:     envSeconds("GDL_BACKOFF_BASE_S", 30),
		GDLBackoffMultiple: envFloat("GDL_BACKOFF_MULTIPLIER", 2.5),

		CDLRetries:         envInt("CDL_RETRIES", 3),
		CDLMaxRunRetries:   envInt("CDL_MAX_RUN_RETRIES", 3),
		CDLBackoffBase:     envSeconds("CDL_BACKOFF_BASE_S", 30),
		CDLBackoffMultiple: envFloat("CDL_BACKOFF_MULTIPLIER", 2.5),

		GlobalSpeedLimit: envStr("GLOBAL_DOWNLOAD_SPEED_LIMIT", "20M"),
		HLSTimeout:       envSeconds("HLS_DOWNLOAD_TIMEOUT", 300),

		TGUploadDelayMin:   envSeconds("TG_UPLOAD_DELAY_MIN", 3.0),
		TGUploadDelayMax:   envSeconds("TG_UPLOAD_DELAY_MAX", 5.0),
		TGBatchSize:        envInt("TG_BATCH_SIZE", 15),
		TGBatchCooldown:    envSeconds("TG_BATCH_COOLDOWN_S", 20),
		TGUploadMaxRetries: envInt("TG_UPLOAD_MAX_RETRIES", 3),
		MaxConcurrentDL:    max(1, envInt("TG_MAX_CONCURRENT_DOWNLOADS", 1)),
		MaxConcurrentUL:    max(1, envInt("TG_MAX_CONCURRENT_UPLOADS", 1)),
		MaxConcurrentHeavy: max(1, envInt("TG_MAX_CONCURRENT_HEAVY", 1)),
		AlbumMaxTotalBytes: envInt64("TG_ALBUM_MAX_BYTES", 64*1024*1024),

		ExtractThreads:   max(1, envInt("EXTRACT_THREADS", 2)),
		ArchiveThreads:   max(1, envInt("ARCHIVE_THREADS", 2)),
		ArchiveZipLevel:  envInt("ARCHIVE_ZIP_LEVEL", 1),
		MaxExtractBytes:  envInt64("MAX_EXTRACT_BYTES", 20*1024*1024*1024),
		MaxExtractFiles:  envInt("MAX_EXTRACT_FILES", 200000),
		MinFreeDiskBytes: envInt64("MIN_FREE_DISK_BYTES", 2*1024*1024*1024),

		BotAPIDataDir: envStr("TG_BOT_API_DATA_DIR", "/var/lib/telegram-bot-api"),
		MemoryLimitMB: envInt64("TGDL_MEMORY_LIMIT_MB", 0),

		TorrentTimeout:      envSeconds("TORRENT_TIMEOUT", 120),
		SearchLimit:         envInt("SEARCH_LIMIT", 300),
		MagnetioURL:         envStr("MAGNETIO_RPC_URL", "http://magnetio-scraper:8080/rpc"),
		MagnetioSecret:      envStr("MAGNETIO_RPC_SECRET", ""),
		AriaMaxConcurrentDL: envInt("ARIA_MAX_CONCURRENT_DOWNLOADS", 1),

		MaxJobsPerChat: envInt("MAX_JOBS_PER_CHAT", 3),

		LogLevel:  strings.ToUpper(envStr("LOG_LEVEL", "INFO")),
		LogFormat: strings.ToLower(envStr("LOG_FORMAT", "text")),

		PinStatusMessage: envBool("PIN_STATUS_MESSAGE", true),

		RenameMaxBatch:   max(1, envInt("RENAME_MAX_BATCH", 200)),
		RenameSessionTTL: envDuration("RENAME_SESSION_TTL", 30*time.Minute),

		SettingsWriteEnv:      envBool("SETTINGS_WRITE_ENV", false),
		SettingsEncryptionKey: envStr("SETTINGS_ENCRYPTION_KEY", ""),
		LogLevelVar:           &slog.LevelVar{},
	}

	if c.BotAPILocalDirMap != "" {
		h, cont, ok := ParseLocalDirMap(c.BotAPILocalDirMap)
		if !ok {
			return nil, fmt.Errorf("invalid TG_BOT_API_LOCAL_DIR_MAP %q: expected hostprefix=containerprefix", c.BotAPILocalDirMap)
		}
		c.LocalHostPrefix = h
		c.LocalContainerPrefix = cont
	}

	c.AuthorizedIDs = parseIDList(os.Getenv("AUTHORIZED_USER_IDS"))

	if raw, ok := os.LookupEnv("OWNER_ID"); ok && strings.TrimSpace(raw) != "" {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id != 0 {
			c.OwnerID, c.HasOwner, c.HasExplicitOwner = id, true, true
		}
	}
	if !c.HasOwner {
		if len(c.AuthorizedIDs) > 0 {
			c.OwnerID, c.HasOwner, c.HasExplicitOwner = c.AuthorizedIDs[0], true, false
		} else {
			c.OwnerID, c.HasOwner, c.HasExplicitOwner = 0, false, false
			slog.Warn("no owner configured: OWNER_ID is unset and AUTHORIZED_USER_IDS is empty; settings commands are disabled")
		}
	}

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

func ParseLocalDirMap(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	parts := strings.SplitN(raw, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	hp := strings.TrimSpace(parts[0])
	cp := strings.TrimSpace(parts[1])
	if hp == "" || cp == "" {
		return "", "", false
	}
	return hp, cp, true
}

func (c *Config) LocalDirMap() (hostPrefix, containerPrefix string, ok bool) {
	return ParseLocalDirMap(c.BotAPILocalDirMap)
}

func (c *Config) UploadLimit() int64 {
	if c.BotAPIURL != "" {
		return 195 * 1024 * 1024 * 1024 / 100
	}
	return 49 * 1024 * 1024
}

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

func (c *Config) UserDir(userID int64) (string, error) {
	dir := filepath.Join(c.AuthDir, strconv.FormatInt(userID, 10))
	return dir, os.MkdirAll(dir, 0o700)
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

func envInt64(key string, def int64) int64 {
	if v, err := strconv.ParseInt(envStr(key, ""), 10, 64); err == nil && v > 0 {
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

func envDuration(key string, def time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
		if s, err := strconv.ParseFloat(raw, 64); err == nil {
			return time.Duration(s * float64(time.Second))
		}
	}
	return def
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

func (c *Config) Lock()    { c.mu.Lock() }
func (c *Config) Unlock()  { c.mu.Unlock() }
func (c *Config) RLock()   { c.mu.RLock() }
func (c *Config) RUnlock() { c.mu.RUnlock() }

func (c *Config) GetAuthorizedIDs() []int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]int64, len(c.AuthorizedIDs))
	copy(out, c.AuthorizedIDs)
	return out
}

func (c *Config) SetAuthorizedIDs(ids []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AuthorizedIDs = make([]int64, len(ids))
	copy(c.AuthorizedIDs, ids)
}

func (c *Config) GetGlobalSpeedLimit() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.GlobalSpeedLimit
}

func (c *Config) SetGlobalSpeedLimit(val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.GlobalSpeedLimit = val
}
