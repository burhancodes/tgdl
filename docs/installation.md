# System Requirements & Installation Guide

This guide covers system prerequisites, manual local installation, environment variables, and Docker deployment.

---

## System Prerequisites

Before running TGDL Bot, ensure the following system dependencies are installed on your host OS:

- **Python**: 3.12 or newer
- **Node.js**: 18.0 or newer (required for running the Magnetio JSON-RPC scraper sidecar).
- **uv**: Modern, fast Python package and project manager.
- **FFmpeg & FFprobe**: Required for video metadata extraction, thumbnail generation, and audio/video transcoding.
- **aria2c**: Required for direct multi-connection HTTP downloads and torrent/magnet link handling.
- **System Archive Utilities** (for `patool` archive support):
  - Linux: `unzip`, `unrar` / `rar`, `p7zip-full` / `7z`, `tar`, `gzip`, `bzip2`, `xz-utils`

### Installing Prerequisites on Ubuntu / Debian
```bash
sudo apt update
sudo apt install -y python3 nodejs npm ffmpeg aria2 unzip p7zip-full tar gzip bzip2 xz-utils
curl -LsSf https://astral.sh/uv/install.sh | sh
```

---

## Local Installation

1. **Clone Repository**:
   ```bash
   git clone https://github.com/Burhanverse/tgdl.git
   cd tgdl
   ```

2. **Sync Python Dependencies**:
   ```bash
   uv sync
   ```

3. **Install Magnetio Scraper Node Dependencies & Start Sidecar**:
   ```bash
   cd scraper
   npm install
   RPC_SHARED_SECRET=your_rpc_secret_here PORT=8080 node index.js &
   cd ..
   ```

4. **Configure Environment Variables**:
   Copy `.env.example` to `.env` and fill in credentials:
   ```bash
   cp .env.example .env
   ```
   Required variables:
   - `TG_API_ID`: Telegram API ID obtained from [my.telegram.org](https://my.telegram.org).
   - `TG_API_HASH`: Telegram API Hash.
   - `TG_BOT_TOKEN`: Bot token from [@BotFather](https://t.me/BotFather).
   - `MAGNETIO_RPC_URL`: `http://localhost:8080/rpc` (or `http://magnetio-scraper:8080/rpc` when using Docker Compose).
   - `MAGNETIO_RPC_SECRET`: Shared secret matching the scraper service (optional).

5. **Start Everything with `./start.sh` (Recommended)**:
   Run the launcher script to automatically manage the Node.js scraper sidecar and Python bot together:
   ```bash
   ./start.sh
   ```
   *Alternatively, you can start the sidecar and bot manually as shown in step 3.*

---

## Docker Deployment

Deploy using Docker Compose or the pre-built GHCR images.

### Quick Start with GHCR Images (`./start-docker.sh`)
Run the Docker launcher script to pull the latest images from GHCR (`ghcr.io/burhancodes/tgdl`) and spin up the complete stack:

```bash
./start-docker.sh
```

### Manual Docker Compose Launch
1. Configure `.env` file as shown above (`MAGNETIO_RPC_URL=http://magnetio-scraper:8080/rpc`).
2. Pull latest images and start containers:
   ```bash
   docker compose pull
   docker compose up -d
   ```
3. View live container logs:
   ```bash
   docker compose logs -f
   ```
