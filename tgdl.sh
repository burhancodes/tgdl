#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Default GHCR repository name
REPO_PREFIX="${GHCR_REPO:-ghcr.io/burhancodes/tgdl}"
export BOT_IMAGE="${BOT_IMAGE:-${REPO_PREFIX}/tgdl-bot:latest}"
export SCRAPER_IMAGE="${SCRAPER_IMAGE:-${REPO_PREFIX}/magnetio-scraper:latest}"

ensure_env() {
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
    mkdir -p data logs
    chmod -R 777 data logs 2>/dev/null || true
}

do_start() {
    ensure_env
    echo "=========================================="
    echo " Starting TGDL Bot & Scraper              "
    echo "=========================================="
    echo "Pulling latest images from GHCR..."
    echo "  • Bot Image:     ${BOT_IMAGE}"
    echo "  • Scraper Image: ${SCRAPER_IMAGE}"

    docker pull "${SCRAPER_IMAGE}"
    docker pull "${BOT_IMAGE}"
    docker pull redis:7-alpine

    echo ""
    echo "Launching Docker Compose stack..."
    docker compose up -d

    echo ""
    echo "=========================================="
    echo " Service Status                           "
    echo "=========================================="
    docker compose ps

    echo ""
    echo "TGDL stack is running! View live logs using:"
    echo "  ./tgdl.sh logs"
}

do_stop() {
    echo "=========================================="
    echo " Stopping TGDL Services                   "
    echo "=========================================="
    docker compose down
    echo "Stopped cleanly."
}

do_restart() {
    echo "Restarting TGDL services..."
    do_stop
    do_start
}

do_update() {
    echo "=========================================="
    echo " Updating TGDL Bot & Scraper              "
    echo "=========================================="
    echo "1. Pulling latest git repository changes..."
    git pull || true

    echo ""
    echo "2. Stopping existing containers..."
    docker compose down || true

    echo ""
    echo "3. Pulling fresh Docker images & restarting..."
    do_start
}

do_status() {
    docker compose ps
}

do_logs() {
    docker compose logs -f
}

show_help() {
    echo "TGDL Unified Management Script"
    echo ""
    echo "Usage: ./tgdl.sh [command]"
    echo ""
    echo "Commands:"
    echo "  start     - Pull latest images & start all services (default)"
    echo "  stop      - Stop & remove running containers"
    echo "  restart   - Restart all services"
    echo "  update    - Pull git repo, fetch latest images, and restart"
    echo "  status    - View running container status"
    echo "  logs      - Tail live container logs"
    echo "  help      - Display this help message"
}

ACTION="${1:-start}"

case "$ACTION" in
    start)
        do_start
        ;;
    stop)
        do_stop
        ;;
    restart)
        do_restart
        ;;
    update)
        do_update
        ;;
    status)
        do_status
        ;;
    logs)
        do_logs
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        echo "Unknown command: $ACTION"
        show_help
        exit 1
        ;;
esac
