# Custom Configuration, Cookies & Task Controls

This guide explains per-user `gallery-dl.conf` and `cookies.txt` management, live task monitoring, job cancellation, and runtime configuration limits.

---

## Gallery-dl Config & Cookies Manager (`/gdlconf`)

Configure custom authentication, headers, or settings for `gallery-dl`.

### Syntax
```text
/gdlconf
/gdl_config
```

### Uploading Cookies
1. Export cookies from your browser (e.g. using *Get cookies.txt* extension).
2. Upload `cookies.txt` to the Telegram bot chat.
3. Reply to the `cookies.txt` file with `/gdlconf`.

### Security & Postprocessor Sanitization
All uploaded `gallery-dl.conf` files are automatically scanned and validated. Any dangerous postprocessors (e.g. `exec`, `python`, `cmd`, `shell`) are denied by default to ensure host security. User cookie and config files exceeding 2 MiB are rejected.

---

## Live Task & Queue Dashboard (`/status`)

Monitor download/upload speeds, active tasks, queued items, and overall performance.

### Syntax
```text
/status
/status me
```

### Features
- **Live Progress Bars**: Real-time progress percentage, ETA, and transfer speed.
- **Pagination Controls**: `Prev` and `Next` buttons for managing large task queues.
- **Task Overview Mode**: Click `Overview` button to see job state counts (Downloads, Uploads, Archives, Conversions) and overall speed totals.
- **Inline Job Cancel Buttons**: Cancel any individual active task directly from the status display.

---

## Job Cancellation (`/cancel`)

Cancel active or queued jobs instantly.

### Syntax
```text
/cancel [job_id]
```

### Usage
- Cancel specific job ID:
  ```text
  /cancel abc12345
  ```
- Send `/cancel` without arguments to open an interactive selection menu of all your active/queued jobs.

---

## Authorization & Security Controls (`.env`)

Configure access control and security policies in `.env`:
- `AUTHORIZED_USER_IDS`: Comma-separated list of allowed Telegram user IDs (e.g. `12345678,98765432`).
- `MAX_JOBS_PER_CHAT`: Maximum concurrent active + queued jobs allowed per chat (default `3`).
- `MAX_TOTAL_DOWNLOADS_BYTES`: Optional cap on total `downloads_dir` storage usage. Aborts jobs exceeding this size.
- `MIN_FREE_DISK_BYTES`: Minimum free disk space required on `DownloadsDir()` before and during downloads and extractions (default `2147483648` = 2 GiB).
- `ALLOW_SHARED_UPLOAD_KEYS`: Fall back to owner's global API keys for uploaders (default `false`).
- `ALLOW_PRIVATE_NETWORK_URLS`: Allow downloading URLs resolving to private/reserved IP ranges (default `false` for SSRF protection).

---

## Heavy Task Gate & Archive Tool Settings (`.env`)

- `TG_MAX_CONCURRENT_HEAVY`: Global concurrency limit for heavy operations (archive extraction, folder archiving, MKV/MP3 conversion, large file splitting, APK patching) (default `1`).
- `EXTRACT_THREADS`: Threads allocated to 7z for archive extraction (default `2`).
- `ARCHIVE_THREADS`: Threads allocated to 7z for folder archiving (default `2`).
- `ARCHIVE_ZIP_LEVEL`: Compression level for zip archives (default `1`).
- `MAX_EXTRACT_BYTES`: Maximum extracted directory size allowed before aborting extraction (default `21474836480` = 20 GiB).
- `MAX_EXTRACT_FILES`: Maximum number of files permitted in an extracted archive (default `200000`).

---

## Upload Batching & Album Limits (`.env`)

- `TG_ALBUM_MAX_BYTES`: Maximum combined size of a batch to send as an album (default `67108864` = 64 MiB). Batches exceeding this threshold are split or sent individually.
- `TG_UPLOAD_DELAY_MIN` / `TG_UPLOAD_DELAY_MAX`: Random delay range between individual file uploads.
- `TG_BATCH_SIZE`: Number of files uploaded before triggering `TG_BATCH_COOLDOWN_S`.
- `PIN_STATUS_MESSAGE`: Automatically pin active job status cards in chat, and unpin upon completion/failure/cancellation (default `true`).

---

## Torrent & aria2 Settings (`.env`)

- `MAGNETIO_RPC_URL`: JSON-RPC 2.0 URL for the Magnetio scraper sidecar (default `http://magnetio-scraper:8080/rpc`).
- `MAGNETIO_RPC_SECRET`: Shared secret authentication token matching the scraper sidecar (`RPC_SHARED_SECRET`).
- `TORRENT_TIMEOUT`: Timeout in seconds for torrent downloads and search RPC requests (default `120`).
- `SEARCH_LIMIT`: Maximum search results to fetch for Telegraph pagination (default `300`).
- `ARIA_MAX_CONCURRENT_DOWNLOADS`: Maximum concurrent downloads in aria2 daemon (default `1`).

---

## Local Telegram Bot API & Zero-Copy Uploads (`.env`)

When running a self-hosted `telegram-bot-api` server in `--local` mode (enabled by default in `docker-compose.yml`):
- `TG_BOT_API_URL`: Points to the local server (e.g. `http://telegram-bot-api:8081`), lifting upload/download size limits up to 2000 MB.
- `TG_BOT_API_DATA_DIR`: Base directory where the local Bot API stores downloaded files (default `/var/lib/telegram-bot-api`). Files are moved or copied and safely cleaned up from this volume upon successful download.
- `TG_BOT_API_LOCAL_DIR_MAP`: Format `hostprefix=containerprefix` (e.g. `/app/data=/app/data`). When the Bot API server shares the downloads directory volume, the bot passes direct `file:///<path>` references to Telegram Bot API instead of transmitting file bytes over HTTP.

---

## Memory Limits & Profiling (`.env`)

- `TGDL_MEMORY_LIMIT_MB`: Set explicit Go memory limit (in MB) via `debug.SetMemoryLimit` (defaults to 75% of cgroup limit if unset).
- `TGDL_PPROF`: Set to `1` to expose `net/http/pprof` on `127.0.0.1:6060`.
