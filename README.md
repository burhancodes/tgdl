# tgdl (Go)

A Telegram bot that downloads from direct links, HLS, torrents/magnets, Google Drive, MEGA, GoFile,
XenForo forums, gallery-dl / cyberdrop-dl sites, and uploads to Telegram or mirrors to GoFile,
FileDitch and Pixeldrain. Go rewrite of the Python `tgdl` project.

## Architecture

```
cmd/tgdl            entrypoint, dependency wiring, graceful shutdown
internal/
  config            env → typed Config (validated once)
  store             SQLite job repository (pure-Go driver, WAL)
  tg                Telegram interface + Bot API adapter (swap MTProto in here only)
  bot               command/callback handlers (thin: parse → enqueue)
  jobs              Manager (queue, per-job State, status card), pipeline, pluggable Sources
  dl                engines: direct HTTP, HLS, GoFile, gallery-dl/cyberdrop-dl runner, XenForo
  torrent           aria2 RPC daemon supervisor, tracker list, Magnetio search client
  gdrive, mega      cloud downloaders (MEGA public-link protocol implemented natively)
  upload            GoFile / FileDitch / Pixeldrain uploads, per-user API keys
  archive, media    extraction/creation, split-archive detection, ffmpeg wrappers
  apk               APKEditor + tgpatcher + uber-apk-signer pipeline
  netguard          SSRF-safe HTTP transport (checks the connected IP, not a pre-resolve)
  pacing, status, fsutil, auth, logging
```

Key design changes from the Python version:

* **Sources are plugins.** `jobs.Source{Name, Match, Fetch}` replaces the 900-line `if/elif` download
  function; add a provider by implementing the interface and registering it in `DefaultSources`.
* **No global mutable registries.** Interactive prompts (archive choice, audio conversion,
  passwords) live in the job's own `State`, and disappear with it.
* **One cancellation model.** Every job has a `context.Context`; cancel kills subprocesses,
  aria2 GIDs and HTTP streams through it.
* **No engine recursion.** The gallery-dl ⇄ cyberdrop-dl fallback is an ordered chain per URL.
* **SSRF protection at dial time**, covering redirects, DNS rebinding, HLS segments.
* **Zip-slip / tar traversal protection** in extraction; passwords never echoed to chat.
* Status card rendering is a pure function of a `Snapshot` (`internal/status`), unit-tested.

## Telegram transport (important)

The Bot API cloud limits uploads to 50 MB. For files up to ~2 GB run a self-hosted
`telegram-bot-api --local` server (included in `docker-compose.yml`) and set `TG_BOT_API_URL`.
Without it the bot works but splits/skips anything above ~49 MB.

## Build & run

```
go mod tidy          # resolves dependencies and writes go.sum
go vet ./... && go test ./...
go build -o tgdl ./cmd/tgdl
```

or `docker compose up -d --build`. Configuration keys are unchanged from the Python version
(`.env.example`), plus `TG_BOT_API_URL`.

External tools used at runtime: `ffmpeg`, `aria2c`, `7z`/`unrar`/`unzip`, `gallery-dl`,
`cyberdrop-dl`, `java` + `python3` (APK patching), `node` (torrent-search / forum sidecar in `scraper/`).

## Behaviour notes / known differences

* The "split large file?" prompt was dropped; oversized files split automatically (video-aware,
  binary fallback), as the previous default did.
* Pinning of the status message and the `/settings`-style split toggle are not ported.
* Interactive multi-part archive upload sessions (`/unzip` with manually uploaded parts) are not
  ported; use a URL, or a single archive.
