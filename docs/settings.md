# Bot Settings & Runtime Configuration

The bot features a centralized, typed settings registry allowing the bot owner to inspect and modify configuration variables at runtime via Telegram without manually editing `.env`.

---

## 1. Precedence Rules

Configuration values are resolved using four distinct layers in increasing order of priority:

1. **Built-in Defaults**: Hardcoded fallback values defined in `internal/settings`.
2. **`.env` File**: Key-value pairs loaded at startup from `.env`.
3. **Process Environment**: Environment variables set in the host OS or Docker container.
4. **Bot Overrides (`DATA_DIR/settings.json`)**: Overrides applied dynamically by the owner via Telegram.

Precedence resolution:
```
Built-in Default < .env < Process Environment < settings.json override
```

In the bot UI (`/settings` detail cards and `/get`), the active layer is explicitly displayed:
- `default`: using compiled fallback.
- `env`: loaded from environment or `.env`.
- `bot override`: active override stored in `settings.json`.

---

## 2. Apply Modes

Every configuration setting belongs to one of three application categories:

- **Live**: Changes take effect immediately without requiring a restart.
  - Concurrency semaphores (`TG_MAX_CONCURRENT_DOWNLOADS`, `TG_MAX_CONCURRENT_UPLOADS`, `TG_MAX_CONCURRENT_HEAVY`) are dynamically resized without dropping in-flight permits.
  - `LOG_LEVEL` shifts `slog.LevelVar` in real time.
  - `GLOBAL_DOWNLOAD_SPEED_LIMIT` applies to aria2 live via JSON-RPC (`aria2.changeGlobalOption`) and tunes new download tasks.
  - Album limits (`TG_ALBUM_MAX_BYTES`), disk space guards (`MIN_FREE_DISK_BYTES`), extraction caps (`MAX_EXTRACT_BYTES`, `MAX_EXTRACT_FILES`), `AUTHORIZED_USER_IDS`, upload host keys, search limits, and rename options are hot-applied.
- **Restart Required**: Overrides are saved immediately to `settings.json`, but require a bot restart to take effect.
  - Indicated in the bot UI with a `[restart required]` badge.
  - The owner can issue `/restart` for a clean shutdown (exit code 0); Docker's `restart: unless-stopped` policy restarts the container.
- **Read-Only**: Critical bootstrapping variables that cannot be altered within the bot:
  - `OWNER_ID`, `TG_BOT_TOKEN`, `TG_API_ID`, `TG_API_HASH`, `TG_BOT_API_URL`, `TG_BOT_API_DATA_DIR`, `TG_BOT_API_LOCAL_DIR_MAP`, `DATA_DIR`, `AUTH_DIR`, `LOG_DIR`, and `GDRIVE_*`.
  - Displayed in the bot for inspection with a `[read-only]` badge, but can only be changed via the host environment or `.env` file.

---

## 3. Persistence, Backups & Encryption

- **File Location**: Overrides are stored in `DATA_DIR/settings.json` with strict `0600` permissions.
- **Atomic Writes**: Saved atomically using a temporary file in the same directory (`settings.json.tmp.*`), synced to disk (`Sync()`), and renamed.
- **Automatic Backup**: A backup of the prior configuration is saved to `DATA_DIR/settings.json.bak` prior to renaming.
- **Optional `.env` Synchronization**: When `SETTINGS_WRITE_ENV=true` is enabled, changes also atomically update matching `KEY=value` lines in `.env` while preserving comments and ordering.
- **Secret Encryption**: When `SETTINGS_ENCRYPTION_KEY` is provided, secret values are encrypted with AES-256-GCM using an scrypt-derived key. If the encryption key is missing or incorrect, loading fails safely rather than falling back to defaults.
- **Resetting Configuration**: To wipe all bot overrides and restore environment defaults, delete `DATA_DIR/settings.json` and restart the bot, or use `/unset KEY` in Telegram.

---

## 4. Docker Volume Persistence

Ensure `DATA_DIR` (`./data:/app/data` in `docker-compose.yml`) is mounted as a persistent host directory or named volume. Overrides in `settings.json` will survive container rebuilds and image updates.

---

## 5. Owner Commands & UI Reference

Settings commands and callbacks are strictly restricted to the bot owner (`OWNER_ID`) in a private chat.

### Interactive Menu
- `/settings`: Opens the interactive settings menu categorized by group.
  - Group navigation with 8 keys per page, status badges, and masked secret values (`••••1a2b` or `••••••••`).
  - Detail cards with key type, allowed range, current value, source layer, and apply mode.
  - `[Edit]` button prompts for input with a 60-second timeout; private messages containing secrets are deleted immediately after reading.
  - Boolean and enum values toggle directly via inline buttons.
  - Confirmation dialogs protect dangerous modifications (Security group, `AUTHORIZED_USER_IDS`, `MIN_FREE_DISK_BYTES=0`, secrets).
  - Rate-limited to 30 edits per minute with audit logging.

### Text Commands
- `/set KEY value`: Set or override a configuration key.
- `/get KEY`: Inspect a configuration key's value, source layer, and details.
- `/unset KEY`: Remove a configuration override and revert to the default/env value.
- `/settings search <text>`: Search settings by key name or description.
- `/settings changed`: View all currently active configuration overrides.
- `/settings history`: Display audit history of the last 50 configuration changes.
- `/settings undo`: Revert the most recent configuration change.
- `/settings export`: Export configuration as a JSON document (secrets redacted).
- `/settings import`: Import configuration from an uploaded JSON file with diff confirmation.
- `/restart`: Gracefully flush state and shut down with exit code 0 to trigger container restart.

---

## 6. Complete Settings Reference

### General

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `SETTINGS_WRITE_ENV` | `bool` | `false` | Live | Update matching KEY=value lines in .env on change |
| `SOURCE_ADDRESS` | `string` | `*(empty)*` | Restart Required | Source IP address to bind outbound sockets to |
| `FORCE_IPV6` | `bool` | `false` | Restart Required | Force IPv6 resolution and connections |

### Telegram

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `TG_API_ID` | `int` | `0` | Read-Only | Telegram API ID from my.telegram.org |
| `TG_API_HASH` | `secret` | `*(empty)*` | Read-Only | Telegram API hash from my.telegram.org |
| `TG_BOT_TOKEN` | `secret` | `*(empty)*` | Read-Only | Telegram Bot API token from BotFather |
| `TG_BOT_API_URL` | `string` | `*(empty)*` | Read-Only | Custom Telegram Bot API server endpoint |
| `TG_BOT_API_DATA_DIR` | `path` | `/var/lib/telegram-bot-api` | Read-Only | Shared data dir used by self-hosted Bot API server |
| `TG_BOT_API_LOCAL_DIR_MAP` | `string` | `*(empty)*` | Read-Only | Host-to-container dir map (hostprefix=containerprefix) |
| `TG_ALBUM_MAX_BYTES` | `size` | `64M` | Live | Max total bytes per Telegram media album batch |
| `PIN_STATUS_MESSAGE` | `bool` | `true` | Live | Pin active job status card in chat |
| `TG_UPLOAD_DELAY_MIN` | `duration` | `3s` | Live | Minimum randomized delay between Telegram uploads |
| `TG_UPLOAD_DELAY_MAX` | `duration` | `5s` | Live | Maximum randomized delay between Telegram uploads |
| `TG_BATCH_SIZE` | `int` | `15` | Live | Max uploads sent before cooldown delay is triggered |
| `TG_BATCH_COOLDOWN_S` | `duration` | `20s` | Live | Cooldown duration between upload batches |
| `TG_UPLOAD_MAX_RETRIES` | `int` | `3` | Live | Max retries for transient Telegram upload failures |
| `SHOW_SYSTEM_STATS_ON_JOB_CARD` | `bool` | `true` | Live | Display CPU, RAM and disk usage on active job status cards |
| `RENAME_MAX_BATCH` | `int` | `200` | Live | Maximum files processed in a single /rename batch |
| `RENAME_SESSION_TTL` | `duration` | `30m` | Live | Inactivity timeout for interactive rename sessions |

### Limits & concurrency

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `TG_MAX_CONCURRENT_DOWNLOADS` | `int` | `1` | Live | Concurrent active download jobs semaphore limit |
| `TG_MAX_CONCURRENT_UPLOADS` | `int` | `1` | Live | Concurrent active upload jobs semaphore limit |
| `TG_MAX_CONCURRENT_HEAVY` | `int` | `1` | Live | Semaphore limit for heavy operations (ffmpeg, archives) |
| `MAX_JOBS_PER_CHAT` | `int` | `3` | Live | Maximum concurrent active jobs permitted per chat |
| `ARIA_MAX_CONCURRENT_DOWNLOADS` | `int` | `1` | Restart Required | Aria2 concurrent downloads limit |
| `TGDL_MEMORY_LIMIT_MB` | `int64` | `0` | Restart Required | Go runtime memory limit in megabytes (0 = cgroup auto) |

### Storage & disk

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `DATA_DIR` | `path` | `./data` | Read-Only | Base data directory for sqlite database and downloads |
| `AUTH_DIR` | `path` | `./auth` | Read-Only | Directory storing per-user authentication and keys |
| `MIN_FREE_DISK_BYTES` | `size` | `2G` | Live | Minimum disk free space required before starting writes |
| `MAX_TOTAL_DOWNLOADS_BYTES` | `size` | `*(empty)*` | Live | Disk guard cap for total active downloads size |

### Archives

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `EXTRACT_THREADS` | `int` | `2` | Live | CPU thread count allocated for archive extraction |
| `ARCHIVE_THREADS` | `int` | `2` | Live | CPU thread count allocated for 7z archiving |
| `ARCHIVE_ZIP_LEVEL` | `int` | `1` | Live | Compression level for 7z archives (0 to 9) |
| `MAX_EXTRACT_BYTES` | `size` | `20G` | Live | Maximum uncompressed total size allowed when extracting |
| `MAX_EXTRACT_FILES` | `int` | `200000` | Live | Maximum file count allowed inside an extracted archive |

### Downloaders

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `GLOBAL_DOWNLOAD_SPEED_LIMIT` | `size` | `20M` | Live | Global speed limit applied to aria2 and download tools |
| `HLS_DOWNLOAD_TIMEOUT` | `duration` | `300s` | Live | Timeout for HLS media stream segment downloads |
| `GDL_SLEEP_MIN` | `float` | `1.5` | Live | Minimum sleep delay between gallery-dl requests |
| `GDL_SLEEP_MAX` | `float` | `4.0` | Live | Maximum sleep delay between gallery-dl requests |
| `GDL_SLEEP_REQUEST` | `string` | `1-3` | Live | Per-request sleep interval for gallery-dl (e.g. 1-3) |
| `GDL_RETRIES` | `int` | `4` | Live | Network retry count for gallery-dl items |
| `GDL_MAX_RUN_RETRIES` | `int` | `3` | Live | Subprocess retry attempts on gallery-dl rate-limit fail |
| `GDL_BACKOFF_BASE_S` | `duration` | `30s` | Live | Base exponential backoff delay for gallery-dl |
| `GDL_BACKOFF_MULTIPLIER` | `float` | `2.5` | Live | Multiplier for gallery-dl exponential backoff retry |
| `CDL_RETRIES` | `int` | `3` | Live | Download retry attempts for cyberdrop-dl files |
| `CDL_MAX_RUN_RETRIES` | `int` | `3` | Live | Subprocess retry attempts for cyberdrop-dl runs |
| `CDL_BACKOFF_BASE_S` | `duration` | `30s` | Live | Base exponential backoff delay for cyberdrop-dl |
| `CDL_BACKOFF_MULTIPLIER` | `float` | `2.5` | Live | Multiplier for cyberdrop-dl exponential backoff retry |

### Upload hosts

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `PIXELDRAIN_API_KEY` | `secret` | `*(empty)*` | Live | API key for Pixeldrain uploads |
| `PIXELDRAIN_DOMAIN` | `enum` | `pixeldrain.com` | Live | Domain endpoint for Pixeldrain links |
| `GOFILE_API_KEY` | `secret` | `*(empty)*` | Live | API key for GoFile uploads |
| `GOFILE_BYPASS_HOST` | `string` | `gf.1drv.eu.org` | Live | Bypass proxy host for GoFile direct downloads |

### Cloud (Drive/MEGA)

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `GDRIVE_TOKEN_PATH` | `path` | `./auth/token.json` | Read-Only | OAuth token JSON file path for Google Drive |
| `GDRIVE_ACCOUNTS_DIR` | `path` | `./auth/accounts` | Read-Only | Service account JSON files folder for Google Drive |
| `USE_SERVICE_ACCOUNTS` | `bool` | `true` | Live | Prefer Google service accounts if available in dir |
| `MEGA_EMAIL` | `string` | `*(empty)*` | Live | MEGA account login email |
| `MEGA_PASSWORD` | `secret` | `*(empty)*` | Live | MEGA account login password |

### Torrents/Search

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `TORRENT_TIMEOUT` | `duration` | `120s` | Live | Timeout before aborting stalled torrent metadata retrieval |
| `SEARCH_LIMIT` | `int` | `300` | Live | Maximum search results returned by /ts |
| `MAGNETIO_RPC_URL` | `string` | `http://magnetio-scraper:8080/rpc` | Restart Required | Magnetio sidecar HTTP RPC URL endpoint |
| `MAGNETIO_RPC_SECRET` | `secret` | `*(empty)*` | Restart Required | Shared authentication secret for Magnetio RPC sidecar |

### Logging

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `LOG_DIR` | `path` | `./logs` | Read-Only | Directory where application log files are stored |
| `LOG_LEVEL` | `enum` | `INFO` | Live | Active log level threshold |
| `LOG_FORMAT` | `enum` | `text` | Restart Required | Log output formatting (text or json) |

### Security

| Key | Type | Default | Apply Mode | Description |
|---|---|---|---|---|
| `OWNER_ID` | `int64` | `*(empty)*` | Read-Only | Telegram user ID of the bot owner |
| `AUTHORIZED_USER_IDS` | `idlist` | `*(empty)*` | Live | Comma-separated Telegram user IDs authorized to use bot |
| `ALLOW_SHARED_UPLOAD_KEYS` | `bool` | `false` | Live | Allow regular users to use owner host upload keys |
| `ALLOW_PRIVATE_NETWORK_URLS` | `bool` | `false` | Live | Allow downloading from private network IP addresses (SSRF) |
| `SETTINGS_ENCRYPTION_KEY` | `secret` | `*(empty)*` | Read-Only | Passphrase used to encrypt secret values in settings.json |

