# Resource Limits & Runtime Tuning

This document details the memory, CPU, disk, and concurrency guards implemented in `tgdl`, as well as tuning instructions for constrained host environments.

---

## 1. Architecture Overview

To eliminate OOM crashes and resource storms:
1. **Streaming Uploads**: All Telegram uploads stream directly from disk using chunked multipart HTTP requests (`io.MultiReader`). Files are never buffered in memory.
2. **Streaming Decryption & Subprocess Pipes**: HLS AES-128-CBC downloads decrypt stream-by-stream directly to disk. Subprocess output is captured in a bounded tail buffer (~64 KiB), avoiding memory leaks from verbose tool outputs.
3. **Heavy Work Concurrency Gate**: A centralized weighted semaphore restricts concurrent CPU- and RAM-intensive jobs (extraction, archiving, remuxing, file splitting, and APK patching).
4. **Volume Creation & Disk Guards**: Directory archiving creates multi-part volumes in a single pass (`7z a -v<N>m`) without full intermediate archives. Free disk space is verified before and during operations.

---

## 2. Environment Variables & Defaults

| Variable | Default | Description |
|---|---|---|
| `TG_ALBUM_MAX_BYTES` | `67108864` (64 MiB) | Maximum cumulative size of files to upload together in a media group album. Oversized batches are split or sent individually. |
| `TG_MAX_CONCURRENT_HEAVY` | `1` | Global concurrency gate for heavy operations (extract, archive, video/audio convert, large file split, apk patch). Never held during user prompts. |
| `EXTRACT_THREADS` | `2` | Number of threads passed to 7-Zip (`-mmt=`) during archive extraction. |
| `ARCHIVE_THREADS` | `2` | Number of threads passed to 7-Zip during folder compression. |
| `ARCHIVE_ZIP_LEVEL` | `1` | Compression level for zip archives (fast compression, low CPU/RAM). |
| `MAX_EXTRACT_BYTES` | `21474836480` (20 GiB) | Maximum allowable total size of an extracted archive directory before extraction is aborted. |
| `MAX_EXTRACT_FILES` | `200000` | Maximum file count permitted inside an extracted archive. |
| `MIN_FREE_DISK_BYTES` | `2147483648` (2 GiB) | Minimum free disk space on `DownloadsDir()` required before and during downloads and extractions. |
| `TG_BOT_API_DATA_DIR` | `/var/lib/telegram-bot-api` | Path to local Telegram Bot API data volume. Files are safely deleted/moved from this directory upon download completion. |
| `TGDL_MEMORY_LIMIT_MB` | `0` (auto: 75% cgroup) | Explicit memory limit passed to Go runtime (`debug.SetMemoryLimit`). If unset, automatically reads cgroup limits. |
| `TGDL_PPROF` | `0` | If set to `1`, exposes Go's `net/http/pprof` profiling server on `127.0.0.1:6060`. |
| `ARIA_MAX_CONCURRENT_DOWNLOADS` | `1` | Maximum concurrent active downloads in the aria2c daemon. |

---

## 3. Container Resource Limits (`docker-compose.yml`)

The provided `docker-compose.yml` configures hard container limits and kernel OOM scoring:

### `tgdl`
- **Memory**: `mem_limit: ${TGDL_MEM:-3g}`, `memswap_limit: ${TGDL_MEM:-3g}` (disables swap thrashing).
- **CPUs**: `cpus: ${TGDL_CPUS:-2}`.
- **PIDs**: `pids_limit: 1024`.
- **OOM Score**: `oom_score_adj: 500` ensures that under severe host memory exhaustion, the kernel kills the bot container before host-critical system processes.
- **Go Runtime Tuning**: `GOMEMLIMIT=2400MiB` (80% of container RAM) and `GOGC=50` prompt the Go garbage collector to run aggressively when approaching limits.
- **Logging**: Rotating `json-file` driver with `max-size: 10m`, `max-file: 3`.

### `telegram-bot-api`
- **Memory**: `mem_limit: ${BOTAPI_MEM:-3g}`, `memswap_limit: ${BOTAPI_MEM:-3g}`.
- **CPUs**: `cpus: ${BOTAPI_CPUS:-2}`.
- **PIDs**: `pids_limit: 512`.
- **Note**: The official Telegram Bot API server may legitimately use several hundred megabytes of RAM when spooling large 1.5–2 GB uploads or downloads.

### `magnetio-scraper`
- **Memory**: `mem_limit: 512m`.
- **Node GC**: `NODE_OPTIONS=--max-old-space-size=384`.

---

## 4. Diagnosing Memory and Performance with `docker stats`

To check real-time container resource usage:

```bash
docker stats
```

Example output:
```text
CONTAINER ID   NAME               CPU %     MEM USAGE / LIMIT     MEM %     NET I/O           BLOCK I/O         PIDS
a1b2c3d4e5f6   tgdl-tgdl-1        12.4%     185MiB / 3GiB         6.02%     1.2GB / 850MB     120MB / 1.1GB     28
f6e5d4c3b2a1   telegram-bot-api   4.2%      320MiB / 3GiB         10.42%    850MB / 1.2GB     2.1GB / 50MB      19
0123456789ab   magnetio-scraper   0.1%      68MiB / 512MiB        13.28%    12MB / 15MB       0B / 0B           11
```

### Investigating Memory Growth (Profiling with pprof)
If `tgdl` memory usage appears higher than expected:
1. Enable pprof in `.env`:
   ```bash
   TGDL_PPROF=1
   ```
2. Restart the container:
   ```bash
   docker compose restart tgdl
   ```
3. Capture a heap profile from within the container or host:
   ```bash
   curl -s http://127.0.0.1:6060/debug/pprof/heap > heap.pprof
   go tool pprof -top heap.pprof
   ```

### Log Rotation & Disk Truncation
Log files in `logs/` (`aria2c_daemon.log`, `magnetio_scraper.log`, and `uploads.log`) are checked at startup and every hour. Any file exceeding 10 MiB is automatically truncated, keeping only the most recent entries.
