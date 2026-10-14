package settings

import (
	"fmt"
	"strings"
	"time"
)

var defaultSpecs = []*Spec{
	{
		Key:         "SETTINGS_WRITE_ENV",
		Group:       GroupGeneral,
		Description: "Update matching KEY=value lines in .env on change",
		Type:        "bool",
		Default:     "false",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "SOURCE_ADDRESS",
		Group:       GroupGeneral,
		Description: "Source IP address to bind outbound sockets to",
		Type:        "string",
		Default:     "",
		Apply:       ApplyRestart,
	},
	{
		Key:         "FORCE_IPV6",
		Group:       GroupGeneral,
		Description: "Force IPv6 resolution and connections",
		Type:        "bool",
		Default:     "false",
		Apply:       ApplyRestart,
		Validate:    ValidateBoolValue,
	},

	{
		Key:         "TG_API_ID",
		Group:       GroupTelegram,
		Description: "Telegram API ID from my.telegram.org",
		Type:        "int",
		Default:     "0",
		Apply:       ApplyReadOnly,
		Validate:    ValidateIntRange(0, 0),
	},
	{
		Key:         "TG_API_HASH",
		Group:       GroupTelegram,
		Description: "Telegram API hash from my.telegram.org",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyReadOnly,
	},
	{
		Key:         "TG_BOT_TOKEN",
		Group:       GroupTelegram,
		Description: "Telegram Bot API token from BotFather",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyReadOnly,
	},
	{
		Key:         "TG_BOT_API_URL",
		Group:       GroupTelegram,
		Description: "Custom Telegram Bot API server endpoint",
		Type:        "string",
		Default:     "",
		Apply:       ApplyReadOnly,
	},
	{
		Key:         "TG_BOT_API_DATA_DIR",
		Group:       GroupTelegram,
		Description: "Shared data dir used by self-hosted Bot API server",
		Type:        "path",
		Default:     "/var/lib/telegram-bot-api",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "TG_BOT_API_LOCAL_DIR_MAP",
		Group:       GroupTelegram,
		Description: "Host-to-container dir map (hostprefix=containerprefix)",
		Type:        "string",
		Default:     "",
		Apply:       ApplyReadOnly,
	},
	{
		Key:         "TG_ALBUM_MAX_BYTES",
		Group:       GroupTelegram,
		Description: "Max total bytes per Telegram media album batch",
		Type:        "size",
		Default:     "64M",
		Min:         1,
		Max:         2 * 1024 * 1024 * 1024,
		Apply:       ApplyLive,
		Validate:    ValidateSizeRange(1, 2*1024*1024*1024),
	},
	{
		Key:         "PIN_STATUS_MESSAGE",
		Group:       GroupTelegram,
		Description: "Pin active job status card in chat",
		Type:        "bool",
		Default:     "true",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "TG_UPLOAD_DELAY_MIN",
		Group:       GroupTelegram,
		Description: "Minimum randomized delay between Telegram uploads",
		Type:        "duration",
		Default:     "3s",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(0, 60*time.Second),
	},
	{
		Key:         "TG_UPLOAD_DELAY_MAX",
		Group:       GroupTelegram,
		Description: "Maximum randomized delay between Telegram uploads",
		Type:        "duration",
		Default:     "5s",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(0, 120*time.Second),
	},
	{
		Key:         "TG_BATCH_SIZE",
		Group:       GroupTelegram,
		Description: "Max uploads sent before cooldown delay is triggered",
		Type:        "int",
		Default:     "15",
		Min:         1,
		Max:         100,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 100),
	},
	{
		Key:         "TG_BATCH_COOLDOWN_S",
		Group:       GroupTelegram,
		Description: "Cooldown duration between upload batches",
		Type:        "duration",
		Default:     "20s",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(0, 600*time.Second),
	},
	{
		Key:         "TG_UPLOAD_MAX_RETRIES",
		Group:       GroupTelegram,
		Description: "Max retries for transient Telegram upload failures",
		Type:        "int",
		Default:     "3",
		Min:         0,
		Max:         10,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 10),
	},
	{
		Key:         "SHOW_SYSTEM_STATS_ON_JOB_CARD",
		Group:       GroupTelegram,
		Description: "Display CPU, RAM and disk usage on active job status cards",
		Type:        "bool",
		Default:     "true",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "RENAME_MAX_BATCH",
		Group:       GroupTelegram,
		Description: "Maximum files processed in a single /rename batch",
		Type:        "int",
		Default:     "200",
		Min:         1,
		Max:         1000,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 1000),
	},
	{
		Key:         "RENAME_SESSION_TTL",
		Group:       GroupTelegram,
		Description: "Inactivity timeout for interactive rename sessions",
		Type:        "duration",
		Default:     "30m",
		Min:         1,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(time.Minute, 24*time.Hour),
	},

	{
		Key:         "TG_MAX_CONCURRENT_DOWNLOADS",
		Group:       GroupLimits,
		Description: "Concurrent active download jobs semaphore limit",
		Type:        "int",
		Default:     "1",
		Min:         1,
		Max:         32,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 32),
	},
	{
		Key:         "TG_MAX_CONCURRENT_UPLOADS",
		Group:       GroupLimits,
		Description: "Concurrent active upload jobs semaphore limit",
		Type:        "int",
		Default:     "1",
		Min:         1,
		Max:         32,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 32),
	},
	{
		Key:         "TG_MAX_CONCURRENT_HEAVY",
		Group:       GroupLimits,
		Description: "Semaphore limit for heavy operations (ffmpeg, archives)",
		Type:        "int",
		Default:     "1",
		Min:         1,
		Max:         16,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 16),
	},
	{
		Key:         "MAX_JOBS_PER_CHAT",
		Group:       GroupLimits,
		Description: "Maximum concurrent active jobs permitted per chat",
		Type:        "int",
		Default:     "3",
		Min:         1,
		Max:         50,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 50),
	},
	{
		Key:         "ARIA_MAX_CONCURRENT_DOWNLOADS",
		Group:       GroupLimits,
		Description: "Aria2 concurrent downloads limit",
		Type:        "int",
		Default:     "1",
		Min:         1,
		Max:         16,
		Apply:       ApplyRestart,
		Validate:    ValidateIntRange(1, 16),
	},
	{
		Key:         "TGDL_MEMORY_LIMIT_MB",
		Group:       GroupLimits,
		Description: "Go runtime memory limit in megabytes (0 = cgroup auto)",
		Type:        "int64",
		Default:     "0",
		Min:         0,
		Apply:       ApplyRestart,
		Validate:    ValidateIntRange(0, 1<<20),
	},

	{
		Key:         "DATA_DIR",
		Group:       GroupStorage,
		Description: "Base data directory for sqlite database and downloads",
		Type:        "path",
		Default:     "./data",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "AUTH_DIR",
		Group:       GroupStorage,
		Description: "Directory storing per-user authentication and keys",
		Type:        "path",
		Default:     "./auth",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "MIN_FREE_DISK_BYTES",
		Group:       GroupStorage,
		Description: "Minimum disk free space required before starting writes",
		Type:        "size",
		Default:     "2G",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateSizeRange(0, 0),
	},
	{
		Key:         "MAX_TOTAL_DOWNLOADS_BYTES",
		Group:       GroupStorage,
		Description: "Disk guard cap for total active downloads size",
		Type:        "size",
		Default:     "",
		Min:         0,
		Apply:       ApplyLive,
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return nil
			}
			return ValidateSizeRange(0, 0)(s)
		},
	},

	{
		Key:         "EXTRACT_THREADS",
		Group:       GroupArchives,
		Description: "CPU thread count allocated for archive extraction",
		Type:        "int",
		Default:     "2",
		Min:         1,
		Max:         64,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 64),
	},
	{
		Key:         "ARCHIVE_THREADS",
		Group:       GroupArchives,
		Description: "CPU thread count allocated for 7z archiving",
		Type:        "int",
		Default:     "2",
		Min:         1,
		Max:         64,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 64),
	},
	{
		Key:         "ARCHIVE_ZIP_LEVEL",
		Group:       GroupArchives,
		Description: "Compression level for 7z archives (0 to 9)",
		Type:        "int",
		Default:     "1",
		Min:         0,
		Max:         9,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 9),
	},
	{
		Key:         "MAX_EXTRACT_BYTES",
		Group:       GroupArchives,
		Description: "Maximum uncompressed total size allowed when extracting",
		Type:        "size",
		Default:     "20G",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateSizeRange(0, 0),
	},
	{
		Key:         "MAX_EXTRACT_FILES",
		Group:       GroupArchives,
		Description: "Maximum file count allowed inside an extracted archive",
		Type:        "int",
		Default:     "200000",
		Min:         1,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 10000000),
	},

	{
		Key:         "GLOBAL_DOWNLOAD_SPEED_LIMIT",
		Group:       GroupDownloaders,
		Description: "Global speed limit applied to aria2 and download tools",
		Type:        "size",
		Default:     "20M",
		Apply:       ApplyLive,
		Validate: func(s string) error {
			s = strings.TrimSpace(s)
			if s == "0" || s == "" {
				return nil
			}
			_, err := ParseBytes(s)
			return err
		},
	},
	{
		Key:         "HLS_DOWNLOAD_TIMEOUT",
		Group:       GroupDownloaders,
		Description: "Timeout for HLS media stream segment downloads",
		Type:        "duration",
		Default:     "300s",
		Min:         1,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(time.Second, 2*time.Hour),
	},
	{
		Key:         "GDL_SLEEP_MIN",
		Group:       GroupDownloaders,
		Description: "Minimum sleep delay between gallery-dl requests",
		Type:        "float",
		Default:     "1.5",
		Apply:       ApplyLive,
		Validate:    ValidateFloatRange(0, 300),
	},
	{
		Key:         "GDL_SLEEP_MAX",
		Group:       GroupDownloaders,
		Description: "Maximum sleep delay between gallery-dl requests",
		Type:        "float",
		Default:     "4.0",
		Apply:       ApplyLive,
		Validate:    ValidateFloatRange(0, 300),
	},
	{
		Key:         "GDL_SLEEP_REQUEST",
		Group:       GroupDownloaders,
		Description: "Per-request sleep interval for gallery-dl (e.g. 1-3)",
		Type:        "string",
		Default:     "1-3",
		Apply:       ApplyLive,
	},
	{
		Key:         "GDL_RETRIES",
		Group:       GroupDownloaders,
		Description: "Network retry count for gallery-dl items",
		Type:        "int",
		Default:     "4",
		Min:         0,
		Max:         20,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 20),
	},
	{
		Key:         "GDL_MAX_RUN_RETRIES",
		Group:       GroupDownloaders,
		Description: "Subprocess retry attempts on gallery-dl rate-limit fail",
		Type:        "int",
		Default:     "3",
		Min:         0,
		Max:         20,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 20),
	},
	{
		Key:         "GDL_BACKOFF_BASE_S",
		Group:       GroupDownloaders,
		Description: "Base exponential backoff delay for gallery-dl",
		Type:        "duration",
		Default:     "30s",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(0, 3600*time.Second),
	},
	{
		Key:         "GDL_BACKOFF_MULTIPLIER",
		Group:       GroupDownloaders,
		Description: "Multiplier for gallery-dl exponential backoff retry",
		Type:        "float",
		Default:     "2.5",
		Apply:       ApplyLive,
		Validate:    ValidateFloatRange(1.0, 10.0),
	},
	{
		Key:         "CDL_RETRIES",
		Group:       GroupDownloaders,
		Description: "Download retry attempts for cyberdrop-dl files",
		Type:        "int",
		Default:     "3",
		Min:         0,
		Max:         20,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 20),
	},
	{
		Key:         "CDL_MAX_RUN_RETRIES",
		Group:       GroupDownloaders,
		Description: "Subprocess retry attempts for cyberdrop-dl runs",
		Type:        "int",
		Default:     "3",
		Min:         0,
		Max:         20,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 20),
	},
	{
		Key:         "CDL_BACKOFF_BASE_S",
		Group:       GroupDownloaders,
		Description: "Base exponential backoff delay for cyberdrop-dl",
		Type:        "duration",
		Default:     "30s",
		Min:         0,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(0, 3600*time.Second),
	},
	{
		Key:         "CDL_BACKOFF_MULTIPLIER",
		Group:       GroupDownloaders,
		Description: "Multiplier for cyberdrop-dl exponential backoff retry",
		Type:        "float",
		Default:     "2.5",
		Apply:       ApplyLive,
		Validate:    ValidateFloatRange(1.0, 10.0),
	},

	{
		Key:         "PIXELDRAIN_API_KEY",
		Group:       GroupUploadHosts,
		Description: "API key for Pixeldrain uploads",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyLive,
	},
	{
		Key:         "PIXELDRAIN_DOMAIN",
		Group:       GroupUploadHosts,
		Description: "Domain endpoint for Pixeldrain links",
		Type:        "enum",
		Default:     "pixeldrain.com",
		Enum:        []string{"pixeldrain.com", "pixeldra.in"},
		Apply:       ApplyLive,
		Validate:    ValidateEnumValues([]string{"pixeldrain.com", "pixeldra.in"}),
	},
	{
		Key:         "GOFILE_API_KEY",
		Group:       GroupUploadHosts,
		Description: "API key for GoFile uploads",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyLive,
	},
	{
		Key:         "GOFILE_BYPASS_HOST",
		Group:       GroupUploadHosts,
		Description: "Bypass proxy host for GoFile direct downloads",
		Type:        "string",
		Default:     "gf.1drv.eu.org",
		Apply:       ApplyLive,
	},
	{
		Key:         "MIRROR_HOSTS",
		Group:       GroupUploadHosts,
		Description: "Enabled mirror upload hosts (comma-separated: gofile, fileditch, pixeldrain)",
		Type:        "string",
		Default:     "gofile,fileditch,pixeldrain",
		Apply:       ApplyLive,
		Validate:    ValidateMirrorHosts,
	},
	{
		Key:         "MIRROR_RETRIES",
		Group:       GroupUploadHosts,
		Description: "Max retries per host for mirror uploads on network/5xx/429 errors",
		Type:        "int",
		Default:     "3",
		Min:         0,
		Max:         10,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(0, 10),
	},

	{
		Key:         "GDRIVE_TOKEN_PATH",
		Group:       GroupCloud,
		Description: "OAuth token JSON file path for Google Drive",
		Type:        "path",
		Default:     "./auth/token.json",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "GDRIVE_ACCOUNTS_DIR",
		Group:       GroupCloud,
		Description: "Service account JSON files folder for Google Drive",
		Type:        "path",
		Default:     "./auth/accounts",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "USE_SERVICE_ACCOUNTS",
		Group:       GroupCloud,
		Description: "Prefer Google service accounts if available in dir",
		Type:        "bool",
		Default:     "true",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "MEGA_EMAIL",
		Group:       GroupCloud,
		Description: "MEGA account login email",
		Type:        "string",
		Default:     "",
		Apply:       ApplyLive,
	},
	{
		Key:         "MEGA_PASSWORD",
		Group:       GroupCloud,
		Description: "MEGA account login password",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyLive,
	},

	{
		Key:         "TORRENT_TIMEOUT",
		Group:       GroupTorrents,
		Description: "Timeout before aborting stalled torrent metadata retrieval",
		Type:        "duration",
		Default:     "120s",
		Min:         1,
		Apply:       ApplyLive,
		Validate:    ValidateDurationRange(5*time.Second, 2*time.Hour),
	},
	{
		Key:         "SEARCH_LIMIT",
		Group:       GroupTorrents,
		Description: "Maximum search results returned by /ts",
		Type:        "int",
		Default:     "300",
		Min:         1,
		Max:         1000,
		Apply:       ApplyLive,
		Validate:    ValidateIntRange(1, 1000),
	},
	{
		Key:         "MAGNETIO_RPC_URL",
		Group:       GroupTorrents,
		Description: "Magnetio sidecar HTTP RPC URL endpoint",
		Type:        "string",
		Default:     "http://magnetio-scraper:8080/rpc",
		Apply:       ApplyRestart,
	},
	{
		Key:         "MAGNETIO_RPC_SECRET",
		Group:       GroupTorrents,
		Description: "Shared authentication secret for Magnetio RPC sidecar",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyRestart,
	},

	{
		Key:         "LOG_DIR",
		Group:       GroupLogging,
		Description: "Directory where application log files are stored",
		Type:        "path",
		Default:     "./logs",
		Apply:       ApplyReadOnly,
		Validate:    ValidatePath,
	},
	{
		Key:         "LOG_LEVEL",
		Group:       GroupLogging,
		Description: "Active log level threshold",
		Type:        "enum",
		Default:     "INFO",
		Enum:        []string{"DEBUG", "INFO", "WARNING", "ERROR"},
		Apply:       ApplyLive,
		Validate:    ValidateEnumValues([]string{"DEBUG", "INFO", "WARNING", "ERROR"}),
	},
	{
		Key:         "LOG_FORMAT",
		Group:       GroupLogging,
		Description: "Log output formatting (text or json)",
		Type:        "enum",
		Default:     "text",
		Enum:        []string{"text", "json"},
		Apply:       ApplyRestart,
		Validate:    ValidateEnumValues([]string{"text", "json"}),
	},

	{
		Key:         "OWNER_ID",
		Group:       GroupSecurity,
		Description: "Telegram user ID of the bot owner",
		Type:        "int64",
		Default:     "",
		Apply:       ApplyReadOnly,
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return nil
			}
			return ValidateIntRange(1, 0)(s)
		},
	},
	{
		Key:         "AUTHORIZED_USER_IDS",
		Group:       GroupSecurity,
		Description: "Comma-separated Telegram user IDs authorized to use bot",
		Type:        "idlist",
		Default:     "",
		Apply:       ApplyLive,
		Validate:    ValidateIDListValue,
	},
	{
		Key:         "ALLOW_SHARED_UPLOAD_KEYS",
		Group:       GroupSecurity,
		Description: "Allow regular users to use owner host upload keys",
		Type:        "bool",
		Default:     "false",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "ALLOW_PRIVATE_NETWORK_URLS",
		Group:       GroupSecurity,
		Description: "Allow downloading from private network IP addresses (SSRF)",
		Type:        "bool",
		Default:     "false",
		Apply:       ApplyLive,
		Validate:    ValidateBoolValue,
	},
	{
		Key:         "SETTINGS_ENCRYPTION_KEY",
		Group:       GroupSecurity,
		Description: "Passphrase used to encrypt secret values in settings.json",
		Type:        "secret",
		Default:     "",
		Secret:      true,
		Apply:       ApplyReadOnly,
	},
}

type Registry struct {
	specs   map[string]*Spec
	ordered []*Spec
	byGroup map[string][]*Spec
}

func NewRegistry() *Registry {
	r := &Registry{
		specs:   make(map[string]*Spec),
		ordered: make([]*Spec, 0, len(defaultSpecs)),
		byGroup: make(map[string][]*Spec),
	}
	for _, s := range defaultSpecs {
		cp := *s
		r.specs[cp.Key] = &cp
		r.ordered = append(r.ordered, &cp)
		r.byGroup[cp.Group] = append(r.byGroup[cp.Group], &cp)
	}
	return r
}

func (r *Registry) Get(key string) (*Spec, bool) {
	s, ok := r.specs[CleanKey(key)]
	return s, ok
}

func (r *Registry) All() []*Spec {
	out := make([]*Spec, len(r.ordered))
	copy(out, r.ordered)
	return out
}

func (r *Registry) InGroup(group string) []*Spec {
	list := r.byGroup[group]
	out := make([]*Spec, len(list))
	copy(out, list)
	return out
}

func (r *Registry) Validate(key, val string) error {
	spec, ok := r.Get(key)
	if !ok {
		return fmt.Errorf("unknown configuration key %q", key)
	}
	if spec.Validate != nil {
		if err := spec.Validate(val); err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
	}
	return nil
}

func (r *Registry) Search(query string) []*Spec {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return r.All()
	}
	var matches []*Spec
	for _, s := range r.ordered {
		if strings.Contains(strings.ToLower(s.Key), q) ||
			strings.Contains(strings.ToLower(s.Description), q) ||
			strings.Contains(strings.ToLower(s.Group), q) {
			matches = append(matches, s)
		}
	}
	return matches
}

func (r *Registry) Count() int {
	return len(r.ordered)
}
