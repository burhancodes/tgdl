from __future__ import annotations

import html
import logging
import urllib.parse
from typing import Any

from app.config import settings

from .magnetio_client import (
    check_health_rpc,
    fetch_providers_rpc,
    search_torrents_rpc,
)

log = logging.getLogger(__name__)

SITES: dict[str, str] | None = None
TELEGRAPH_LIMIT = 300


async def initiate_search_tools() -> None:
    """Initializes Magnetio JSON-RPC search provider list and checks sidecar liveness."""
    global SITES
    SITES = {}

    healthy = await check_health_rpc()
    if not healthy:
        log.warning("Magnetio JSON-RPC search backend is not reachable at startup.")

    try:
        providers = await fetch_providers_rpc()
        for p in providers:
            p_id = p.get("id")
            p_name = p.get("name")
            if p_id and p_name:
                SITES[p_id] = p_name
        if SITES:
            SITES["all"] = "All Providers"
            log.info("Loaded %d torrent providers from Magnetio JSON-RPC", len(SITES) - 1)
    except Exception as e:
        log.warning("Failed to fetch provider list from Magnetio search backend: %s", e)


async def search_torrents(
    key: str,
    site: str | list[str] = "all",
    method: str = "apisearch",
) -> list[dict[str, Any]]:
    """Performs torrent search using the Magnetio JSON-RPC sidecar backend."""
    limit = settings.search_limit or 300
    providers = None
    if isinstance(site, list):
        providers = [str(p) for p in site if p]
    elif isinstance(site, str) and site and site not in ("all", "public"):
        providers = [site]

    return await search_torrents_rpc(
        query=key,
        limit=limit,
        providers=providers,
        strict=True,
    )


def format_search_results_html(results: list[dict[str, Any]], query: str, site: str) -> str:
    """Formats search results into clean HTML for Telegram messages."""
    if not results:
        return f"<b>No torrent results found</b> for <i>{html.escape(query)}</i>."

    msg = f"<b>Search Results for:</b> <code>{html.escape(query)}</code>\n"
    msg += f"<b>Source:</b> {html.escape(site.capitalize())} | <b>Total:</b> {len(results)}\n\n"

    for idx, item in enumerate(results[:15], start=1):
        name = item.get("name") or item.get("title") or "Unknown"
        size = item.get("size") or "N/A"
        seeders = item.get("seeders", 0)
        leechers = item.get("leechers", 0)
        torrent_link = item.get("torrent") or item.get("url")
        magnet_link = item.get("magnet")

        msg += f"<b>{idx}. {html.escape(str(name))}</b>\n"
        msg += f"├ <b>Size:</b> {size} | <b>S:</b> {seeders} | <b>L:</b> {leechers}\n"

        links = []
        if magnet_link:
            encoded_mag = urllib.parse.quote(magnet_link)
            links.append(f"<a href='http://t.me/share/url?url={encoded_mag}'>Share Magnet</a>")
        if torrent_link and not torrent_link.startswith("magnet:"):
            links.append(f"<a href='{torrent_link}'>Direct Link</a>")

        if links:
            msg += f"└ {' | '.join(links)}\n\n"
        else:
            msg += "\n"

    return msg
