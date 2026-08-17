from __future__ import annotations

import asyncio
import logging
import os
import secrets
import shutil
from pathlib import Path
from typing import Any

import aiohttp

from app.config import settings

log = logging.getLogger(__name__)

MAGNETIO_PROC: asyncio.subprocess.Process | None = None
MAGNETIO_PORT: int | None = None
MAGNETIO_SECRET: str | None = None
MAGNETIO_URL: str | None = None


def get_free_port() -> int:
    """Finds an available TCP port on localhost."""
    import socket
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def get_active_rpc_url() -> str:
    """Returns current active Magnetio RPC endpoint URL."""
    global MAGNETIO_URL
    if MAGNETIO_URL:
        return MAGNETIO_URL
    return settings.magnetio_rpc_url or "http://127.0.0.1:8080/rpc"


def get_active_rpc_secret() -> str | None:
    """Returns current active Magnetio RPC secret."""
    global MAGNETIO_SECRET
    if MAGNETIO_SECRET is not None:
        return MAGNETIO_SECRET
    return settings.magnetio_rpc_secret


async def _probe_health(url: str, secret: str | None = None, timeout_sec: float = 2.0) -> bool:
    """Pings the /health endpoint of a target Magnetio base URL."""
    base_url = url.rstrip("/")
    if base_url.endswith("/rpc"):
        base_url = base_url[:-4]
    health_url = f"{base_url}/health"

    headers = {}
    if secret:
        headers["Authorization"] = f"Bearer {secret}"

    try:
        timeout = aiohttp.ClientTimeout(total=timeout_sec)
        async with aiohttp.ClientSession(timeout=timeout) as session:
            async with session.get(health_url, headers=headers) as resp:
                if resp.status == 200:
                    data = await resp.json()
                    if isinstance(data, dict) and data.get("status") == "ok":
                        return True
    except Exception:
        return False
    return False


async def start_magnetio_daemon() -> None:
    """Launch the Magnetio Node.js scraper sidecar daemon on a dynamic port if not already reachable."""
    global MAGNETIO_PORT, MAGNETIO_PROC, MAGNETIO_SECRET, MAGNETIO_URL

    if MAGNETIO_PROC is not None and MAGNETIO_PROC.returncode is None:
        return  # Already running locally

    # 1. Check if configured remote RPC endpoint is already alive and reachable
    if settings.magnetio_rpc_url:
        is_remote_healthy = await _probe_health(
            settings.magnetio_rpc_url,
            settings.magnetio_rpc_secret,
            timeout_sec=1.5,
        )
        if is_remote_healthy:
            MAGNETIO_URL = settings.magnetio_rpc_url
            MAGNETIO_SECRET = settings.magnetio_rpc_secret
            log.info("Connected to existing Magnetio RPC service at %s", MAGNETIO_URL)
            return

    # 2. Locate local scraper directory
    workspace_root = Path(__file__).resolve().parents[4]
    scraper_dir = workspace_root / "scraper"
    index_file = scraper_dir / "index.js"

    if not index_file.exists():
        log.warning("Scraper directory or index.js not found at %s. Torrent searches may fail.", scraper_dir)
        return

    if shutil.which("node") is None:
        log.warning("Node.js binary ('node') is not found in PATH. Cannot launch local Magnetio scraper.")
        return

    port = get_free_port()
    secret = settings.magnetio_rpc_secret or secrets.token_urlsafe(32)

    log_dir = settings.log_dir
    log_dir.mkdir(parents=True, exist_ok=True)
    log_file_path = log_dir / "magnetio_scraper.log"

    env = os.environ.copy()
    env["PORT"] = str(port)
    env["RPC_SHARED_SECRET"] = secret

    log.info("Launching local Magnetio RPC scraper sidecar on port %s...", port)
    try:
        log_file = open(log_file_path, "a", encoding="utf-8")
        proc = await asyncio.create_subprocess_exec(
            "node",
            str(index_file),
            cwd=str(scraper_dir),
            env=env,
            stdout=log_file,
            stderr=log_file,
        )
        MAGNETIO_PROC = proc
        MAGNETIO_PORT = port
        MAGNETIO_SECRET = secret
        MAGNETIO_URL = f"http://127.0.0.1:{port}/rpc"

        # 3. Wait for scraper sidecar to become healthy
        max_attempts = 30
        for _ in range(max_attempts):
            if proc.returncode is not None:
                log.error("Magnetio scraper exited unexpectedly with code %s", proc.returncode)
                break
            if await _probe_health(MAGNETIO_URL, secret=secret, timeout_sec=0.5):
                log.info("Magnetio RPC scraper sidecar is healthy on port %s", port)
                return
            await asyncio.sleep(0.5)

        log.warning("Magnetio RPC scraper health check timed out on port %s", port)
    except Exception as e:
        log.exception("Failed to start local Magnetio scraper daemon: %s", e)


async def stop_magnetio_daemon() -> None:
    """Shutdown local Magnetio scraper daemon process."""
    global MAGNETIO_PORT, MAGNETIO_PROC, MAGNETIO_SECRET, MAGNETIO_URL
    if MAGNETIO_PROC is not None:
        try:
            MAGNETIO_PROC.terminate()
            await asyncio.wait_for(MAGNETIO_PROC.wait(), timeout=5.0)
        except Exception:
            try:
                MAGNETIO_PROC.kill()
            except Exception:
                pass
    MAGNETIO_PROC = None
    MAGNETIO_PORT = None
    MAGNETIO_SECRET = None
    MAGNETIO_URL = None
