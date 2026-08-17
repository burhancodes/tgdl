#!/usr/bin/env bash
set -e

# TGDL Bot & Sidecar Services Launcher Script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================="
echo " Starting TGDL Bot Services               "
echo "=========================================="

# Check for .env file
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

mkdir -p data logs auth scratch

# Ensure Node modules are installed and synced for scraper sidecar
if command -v npm >/dev/null 2>&1; then
    if [ -d "scraper" ]; then
        # Install dependencies without mutating lockfile or working tree
        if [ ! -d "scraper/node_modules" ] || [ "scraper/package.json" -nt "scraper/node_modules" ] || [ "scraper/package-lock.json" -nt "scraper/node_modules" ]; then
            echo "Syncing Node.js dependencies for scraper sidecar..."
            (cd scraper && (npm ci --silent 2>/dev/null || npm install --no-save --silent))
            touch scraper/node_modules
        fi
    fi
else
    if [ ! -d "scraper/node_modules" ]; then
        echo "Warning: 'npm' not found and scraper/node_modules is missing."
    fi
fi

# Ensure Python environment is synced
if command -v uv >/dev/null 2>&1; then
    echo "Syncing Python dependencies via uv..."
    uv sync --frozen 2>/dev/null || uv sync
else
    echo "Warning: 'uv' tool not found in PATH. Using system Python environment."
fi

# Launch Bot
echo "Launching TGDL Bot..."
if command -v uv >/dev/null 2>&1; then
    exec uv run python -m app.bot
else
    exec python3 -m app.bot
fi
