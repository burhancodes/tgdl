#!/usr/bin/env bash
set -e

# TGDL Bot & Magnetio Scraper Launcher Script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================="
echo " Starting TGDL Bot & Magnetio RPC Scraper "
echo "=========================================="

# 1. Check for .env file
if [ ! -f ".env" ]; then
    if [ -f ".env.example" ]; then
        echo ".env file not found. Copying from .env.example..."
        cp .env.example .env
        echo "Please configure your credentials in .env before starting."
    else
        echo "Error: .env file missing."
        exit 1
    fi
fi

# Load variables from .env
if [ -f ".env" ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

# Ensure required directories exist
mkdir -p data logs auth scratch

SCRAPER_PORT="${PORT:-8080}"
MAGNETIO_URL="${MAGNETIO_RPC_URL:-http://localhost:${SCRAPER_PORT}/rpc}"

# 2. Ensure Node modules are installed and synced for scraper sidecar
if command -v npm >/dev/null 2>&1; then
    echo "Syncing Node.js dependencies for scraper sidecar..."
    (cd scraper && npm install --silent)
else
    if [ ! -d "scraper/node_modules" ]; then
        echo "Warning: 'npm' not found and scraper/node_modules is missing."
    fi
fi

# 3. Ensure Python environment is synced
if command -v uv >/dev/null 2>&1; then
    echo "Syncing Python dependencies via uv..."
    uv sync
else
    echo "Warning: 'uv' tool not found in PATH. Using system Python environment."
fi

# Process cleanup handler
SCRAPER_PID=""
BOT_PID=""

cleanup() {
    echo ""
    echo "Shutting down TGDL services..."
    if [ -n "$BOT_PID" ] && kill -0 "$BOT_PID" 2>/dev/null; then
        echo "  Stopping Python bot (PID: $BOT_PID)..."
        kill -TERM "$BOT_PID" 2>/dev/null || true
    fi
    if [ -n "$SCRAPER_PID" ] && kill -0 "$SCRAPER_PID" 2>/dev/null; then
        echo "  Stopping Magnetio scraper sidecar (PID: $SCRAPER_PID)..."
        kill -TERM "$SCRAPER_PID" 2>/dev/null || true
    fi
    wait 2>/dev/null || true
    echo "Shutdown complete."
}

trap cleanup EXIT INT TERM

# 4. Start Magnetio Node.js scraper sidecar in background
echo "Launching Magnetio RPC scraper on port ${SCRAPER_PORT}..."
(
    cd scraper
    PORT="${SCRAPER_PORT}" RPC_SHARED_SECRET="${MAGNETIO_RPC_SECRET:-}" node index.js
) &
SCRAPER_PID=$!

# 5. Wait for scraper sidecar health check
echo "Waiting for Magnetio RPC scraper to become ready..."
MAX_RETRIES=15
RETRY_COUNT=0
HEALTH_URL="http://localhost:${SCRAPER_PORT}/health"

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if curl -s -f "$HEALTH_URL" >/dev/null 2>&1; then
        echo "Magnetio RPC scraper is live at http://localhost:${SCRAPER_PORT}/rpc"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT + 1))
    sleep 1
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo "Scraper health check timed out. Proceeding to launch bot anyway..."
fi

# 6. Start Python Bot
echo "Launching TGDL Bot..."
if command -v uv >/dev/null 2>&1; then
    MAGNETIO_RPC_URL="${MAGNETIO_URL}" uv run python -m app.bot &
else
    MAGNETIO_RPC_URL="${MAGNETIO_URL}" python3 -m app.bot &
fi
BOT_PID=$!

# Wait for background processes to exit
wait $BOT_PID $SCRAPER_PID
