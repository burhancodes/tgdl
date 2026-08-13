#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================="
echo " Starting TGDL Bot & Scraper via Docker   "
echo "=========================================="

# 1. Check for .env file
if [ ! -f ".env" ]; then
    if [ -f ".env.example" ]; then
        echo ".env file not found. Copying from .env.example..."
        cp .env.example .env
        echo "--> Please edit your credentials in .env before proceeding."
    else
        echo "Error: .env file missing."
        exit 1
    fi
fi

# Default GHCR repository name
REPO_PREFIX="${GHCR_REPO:-ghcr.io/burhancodes/tgdl}"
export BOT_IMAGE="${BOT_IMAGE:-${REPO_PREFIX}/tgdl-bot:latest}"
export SCRAPER_IMAGE="${SCRAPER_IMAGE:-${REPO_PREFIX}/magnetio-scraper:latest}"

echo "1. Pulling latest images from GHCR..."
echo "  • Bot Image:     ${BOT_IMAGE}"
echo "  • Scraper Image: ${SCRAPER_IMAGE}"

docker pull "${SCRAPER_IMAGE}"
docker pull "${BOT_IMAGE}"
docker pull redis:7-alpine

echo ""
echo "2. Launching Docker Compose stack..."
docker compose up -d

echo ""
echo "=========================================="
echo " Service Status                           "
echo "=========================================="
docker compose ps

echo ""
echo "TGDL stack is now running! View live logs using:"
echo "  docker compose logs -f"
