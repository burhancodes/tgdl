# System Requirements & Installation Guide

This guide covers system prerequisites, manual local installation, environment variables, and Docker deployment for the refactored Go implementation of TGDL Bot.

---

## System Prerequisites

Before running TGDL Bot, ensure the following system dependencies are installed on your host OS:

- **Go**: 1.26 or newer
- **Node.js**: 18.0 or newer (required for running the torrent search sidecar in `scraper/`).
- **FFmpeg & FFprobe**: Required for video metadata extraction, thumbnail generation, and audio/video transcoding.
- **aria2c**: Required for direct multi-connection HTTP downloads and torrent/magnet link handling.
- **System Archive Utilities**:
  - Linux: `unzip`, `unrar` / `rar`, `p7zip-full` / `7z`, `tar`, `gzip`, `bzip2`, `xz-utils`
- **External CLI Tools**:
  - `gallery-dl` & `cyberdrop-dl-patched`: For image board and gallery downloads (`pip install gallery-dl cyberdrop-dl-patched`).

### Installing Prerequisites on Ubuntu / Debian
```bash
sudo apt update
sudo apt install -y golang nodejs npm ffmpeg aria2 unzip unrar p7zip-full tar gzip bzip2 xz-utils python3-pip
pip install --break-system-packages gallery-dl cyberdrop-dl-patched
```

---

## Local Installation

1. **Clone Repository**:
   ```bash
   git clone https://github.com/Burhanverse/tgdl.git
   cd tgdl
   ```

2. **Sync Node.js Dependencies for Scraper Sidecar**:
   ```bash
   cd scraper
   npm ci --omit=dev
   cd ..
   ```

3. **Configure Environment Variables**:
   Copy `.env.example` to `.env` and fill in credentials:
   ```bash
   cp .env.example .env
   ```
   Required variables:
   - `TG_API_ID`: Telegram API ID obtained from [my.telegram.org](https://my.telegram.org).
   - `TG_API_HASH`: Telegram API Hash.
   - `TG_BOT_TOKEN`: Bot token from [@BotFather](https://t.me/BotFather).

4. **Build and Run**:
   Compile the Go binary and start the bot:
   ```bash
   go mod tidy
   go build -o tgdl ./cmd/tgdl
   ./tgdl
   ```

   *Alternatively, use the launcher script which handles sidecar npm sync and build automatically:*
   ```bash
   ./start.sh
   ```

---

## Docker Deployment (Recommended)

Docker Compose runs `tgdl` with native MTProto support, communicating directly with Telegram and supporting up to 2000 MB (2 GiB) transfers without requiring a separate Bot API container.

### Launching with Docker Compose
```bash
# 1. Ensure .env is populated with TG_API_ID, TG_API_HASH, and TG_BOT_TOKEN
cp .env.example .env

# 2. Build and launch stack
docker compose up -d --build

# 3. View live logs
docker compose logs -f
```

### Using Management Script (`./tgdl.sh`)
```bash
./tgdl.sh start      # Starts and builds containers
./tgdl.sh logs       # Follow container logs
./tgdl.sh stop       # Gracefully shut down containers
./tgdl.sh clean      # Prune dangling images and build caches
```

### Container Permissions & Host Ownership
`tgdl` runs with dropped capabilities (`cap_drop: [ALL]`, `no-new-privileges:true`). Files are stored and managed directly within the mounted directories (`./data`, `./auth`, `./logs`).

**Host side effect:** Files created by the bot inside `./data`, `./auth`, and `./logs` will be owned by root on the host. If you need to edit or inspect these files by hand on the host, adjust ownership using:
```bash
sudo chown -R $USER ./data ./auth ./logs
```
