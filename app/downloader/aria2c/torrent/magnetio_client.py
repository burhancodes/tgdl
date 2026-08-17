from __future__ import annotations

import logging
from typing import Any

import aiohttp

from app.config import settings

from .magnetio_daemon import get_active_rpc_secret, get_active_rpc_url

log = logging.getLogger(__name__)


class MagnetioRPCError(Exception):
    """Exception raised when an RPC call to Magnetio scraper fails or returns a JSON-RPC error."""
    def __init__(self, message: str, code: int | None = None) -> None:
        super().__init__(message)
        self.code = code


def format_bytes(size: float) -> str:
    """Formats bytes into human readable string."""
    try:
        size = float(size)
        for unit in ['B', 'KB', 'MB', 'GB', 'TB']:
            if abs(size) < 1024.0:
                return f"{size:.2f} {unit}"
            size /= 1024.0
        return f"{size:.2f} PB"
    except (ValueError, TypeError):
        return "N/A"


async def _rpc_call(method: str, params: dict[str, Any] | None = None) -> Any:
    """Sends a JSON-RPC 2.0 request to the Magnetio scraper sidecar."""
    rpc_url = get_active_rpc_url()
    rpc_secret = get_active_rpc_secret()

    if not rpc_url:
        raise MagnetioRPCError("MAGNETIO_RPC_URL is not configured.")

    payload = {
        "jsonrpc": "2.0",
        "method": method,
        "params": params or {},
        "id": 1,
    }

    headers = {"Content-Type": "application/json"}
    if rpc_secret:
        headers["Authorization"] = f"Bearer {rpc_secret}"

    timeout = aiohttp.ClientTimeout(total=settings.torrent_timeout)

    try:
        async with aiohttp.ClientSession(timeout=timeout) as session:
            async with session.post(rpc_url, json=payload, headers=headers) as resp:
                if resp.status != 200:
                    text = await resp.text()
                    log.warning("Magnetio RPC returned non-200 status %s: %s", resp.status, text)
                    raise MagnetioRPCError(f"RPC HTTP {resp.status}: {text}")

                data = await resp.json()
                if not isinstance(data, dict):
                    raise MagnetioRPCError("Invalid response format: expected JSON object.")

                if data.get("error"):
                    err = data["error"]
                    code = err.get("code")
                    msg = err.get("message", "Unknown RPC error")
                    log.warning("Magnetio RPC error %s: %s", code, msg)
                    raise MagnetioRPCError(f"RPC error ({code}): {msg}", code=code)

                if "result" not in data:
                    raise MagnetioRPCError("Invalid JSON-RPC response: missing 'result' field.")

                return data["result"]

    except MagnetioRPCError:
        raise
    except Exception as e:
        log.warning("Magnetio RPC request failed: %s", e)
        raise MagnetioRPCError(f"Connection to search backend failed: {e}") from e


async def search_torrents_rpc(
    query: str,
    limit: int | None = None,
    providers: list[str] | None = None,
    strict: bool = True,
    media_type: str = "movie",
) -> list[dict[str, Any]]:
    """Performs torrent search via Magnetio RPC torrent.search method."""
    eff_limit = limit or settings.search_limit or 300
    params: dict[str, Any] = {
        "query": query,
        "limit": eff_limit,
        "strict": strict,
        "type": media_type,
    }
    if providers:
        params["providers"] = providers

    result = await _rpc_call("torrent.search", params)

    torrents_raw: list[dict[str, Any]] = []
    if isinstance(result, dict) and "torrents" in result and isinstance(result["torrents"], list):
        torrents_raw = result["torrents"]
    elif isinstance(result, list):
        torrents_raw = result

    items: list[dict[str, Any]] = []
    for item in torrents_raw:
        title = item.get("title") or item.get("name") or "Unknown"
        raw_size = item.get("size", 0)
        size_str = format_bytes(raw_size)
        seeders = int(item.get("seeders", 0) or 0)
        leechers = int(item.get("leechers", 0) or 0)
        magnet = item.get("magnet")

        items.append({
            "name": title,
            "size": size_str,
            "seeders": seeders,
            "leechers": leechers,
            "magnet": magnet,
            "torrent": None,
            "url": magnet or "#",
            "provider": item.get("provider", "unknown"),
            "quality": item.get("quality"),
            "codec": item.get("codec"),
            "source": item.get("source"),
            "languages": item.get("languages", []),
        })

    return items


async def fetch_providers_rpc() -> list[dict[str, str]]:
    """Fetches available providers from Magnetio RPC torrent.providers method."""
    result = await _rpc_call("torrent.providers")
    if isinstance(result, dict) and "providers" in result and isinstance(result["providers"], list):
        return result["providers"]
    if isinstance(result, list):
        return result
    return []


async def check_health_rpc() -> bool:
    """Checks Magnetio RPC sidecar health."""
    try:
        result = await _rpc_call("torrent.health")
        if isinstance(result, dict) and result.get("status") == "ok":
            return True
    except Exception as e:
        log.warning("Magnetio sidecar health check failed: %s", e)
    return False
