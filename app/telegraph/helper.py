from __future__ import annotations

import html
import logging
import urllib.parse
from asyncio import sleep
from secrets import token_urlsafe
from typing import Any

import aiohttp

from .client import Telegraph
from .parser import RetryAfterError

log = logging.getLogger(__name__)

FALLBACK_DOMAINS = ["graph.org", "telegra.ph"]


async def fetch_cinemeta_info(query: str) -> dict[str, Any] | None:
    """Fetches movie/series metadata (poster, description, rating, genres) from Cinemeta for query."""
    clean_query = query.strip().lower()
    if not clean_query or len(clean_query) < 2:
        return None

    encoded = urllib.parse.quote(query.strip())
    headers = {"User-Agent": "Mozilla/5.0"}
    timeout = aiohttp.ClientTimeout(total=3.5)
    best_candidate: dict[str, Any] | None = None

    try:
        async with aiohttp.ClientSession(timeout=timeout, headers=headers) as session:
            for mtype in ["movie", "series"]:
                try:
                    cat_url = f"https://v3-cinemeta.strem.io/catalog/{mtype}/top/search={encoded}.json"
                    async with session.get(cat_url) as resp:
                        if resp.status != 200:
                            continue
                        data = await resp.json()
                        metas = data.get("metas", []) if isinstance(data, dict) else []
                        for item in metas[:5]:
                            name = (item.get("name") or "").strip().lower()
                            if name == clean_query or clean_query in name or name in clean_query:
                                item_id = item.get("id")
                                if item_id:
                                    meta_url = f"https://v3-cinemeta.strem.io/meta/{mtype}/{item_id}.json"
                                    async with session.get(meta_url) as mresp:
                                        if mresp.status == 200:
                                            mdata = await mresp.json()
                                            meta = mdata.get("meta") if isinstance(mdata, dict) else None
                                            if meta and meta.get("name") and meta.get("poster"):
                                                res = {
                                                    "name": meta.get("name"),
                                                    "year": meta.get("year") or meta.get("releaseInfo"),
                                                    "poster": meta.get("poster"),
                                                    "description": meta.get("description"),
                                                    "rating": meta.get("imdbRating"),
                                                    "genres": meta.get("genres"),
                                                    "imdb_id": item_id,
                                                    "type": mtype,
                                                }
                                                if name == clean_query:
                                                    return res
                                                if not best_candidate:
                                                    best_candidate = res
                except Exception as exc:
                    log.debug("Cinemeta lookup error for %s (%s): %s", query, mtype, exc)
    except Exception as exc:
        log.warning("Cinemeta request failed for query %s: %s", query, exc)

    return best_candidate


class TelegraphHelper:
    """Helper to manage Telegraph accounts, page creation, flood-wait retries, and paginated HTML search results."""

    def __init__(
        self,
        author_name: str = "TGDL",
        author_url: str = "https://github.com/Burhanverse/tgdl",
        default_domain: str = "graph.org",
    ) -> None:
        self.author_name = author_name
        self.author_url = author_url
        self.default_domain = default_domain
        self._tokens: dict[str, str] = {}

    def _get_domain_candidates(self, domain: str | None = None) -> list[str]:
        primary = domain or self.default_domain
        candidates = [primary]
        for fallback in FALLBACK_DOMAINS:
            if fallback not in candidates:
                candidates.append(fallback)
        return candidates

    async def get_client(self, domain: str | None = None) -> Telegraph:
        """Gets or initializes a Telegraph client for a specific domain with a cached account token."""
        dom = domain or self.default_domain
        token = self._tokens.get(dom)
        client = Telegraph(access_token=token, domain=dom)
        if not token:
            try:
                res = await client.create_account(
                    short_name=token_urlsafe(8),
                    author_name=self.author_name,
                    author_url=self.author_url,
                )
                if isinstance(res, dict) and "access_token" in res:
                    self._tokens[dom] = res["access_token"]
            except Exception as exc:
                log.warning("Failed to create Telegraph account on domain %s: %s", dom, exc)
        return client

    async def create_page(
        self,
        title: str,
        content: str,
        domain: str | None = None,
        max_retry_wait: float = 60.0,
        max_retries: int = 3,
    ) -> dict[str, Any]:
        """Creates a page with flood-wait auto-retry handling."""
        dom = domain or self.default_domain
        retries = 0
        while True:
            client = await self.get_client(dom)
            try:
                return await client.create_page(
                    title=title,
                    html_content=content,
                    author_name=self.author_name,
                    author_url=self.author_url,
                )
            except RetryAfterError as st:
                retries += 1
                if st.retry_after > max_retry_wait or retries > max_retries:
                    log.warning(
                        "Telegraph flood control exceeded limits (%ds > %ds or retries %d/%d) on %s",
                        st.retry_after,
                        max_retry_wait,
                        retries,
                        max_retries,
                        dom,
                    )
                    raise
                log.warning("Telegraph flood control reached for domain %s. Waiting %d seconds.", dom, st.retry_after)
                await sleep(st.retry_after)
            finally:
                await client.close()

    async def edit_page(
        self,
        path: str,
        title: str,
        content: str,
        domain: str | None = None,
        max_retry_wait: float = 60.0,
        max_retries: int = 3,
    ) -> dict[str, Any]:
        """Edits a page with flood-wait auto-retry handling."""
        dom = domain or self.default_domain
        retries = 0
        while True:
            client = await self.get_client(dom)
            try:
                return await client.edit_page(
                    path=path,
                    title=title,
                    html_content=content,
                    author_name=self.author_name,
                    author_url=self.author_url,
                )
            except RetryAfterError as st:
                retries += 1
                if st.retry_after > max_retry_wait or retries > max_retries:
                    log.warning(
                        "Telegraph flood control exceeded limits (%ds > %ds or retries %d/%d) on %s",
                        st.retry_after,
                        max_retry_wait,
                        retries,
                        max_retries,
                        dom,
                    )
                    raise
                log.warning("Telegraph flood control reached for domain %s. Waiting %d seconds.", dom, st.retry_after)
                await sleep(st.retry_after)
            finally:
                await client.close()

    async def link_paginated_pages(
        self,
        paths: list[str],
        title: str,
        page_contents: list[str],
        domain: str | None = None,
    ) -> None:
        """Adds Prev and Next navigation links to multiple pages after initial creation."""
        dom = domain or self.default_domain
        num_pages = len(paths)
        if num_pages <= 1:
            return

        for idx, (path, content) in enumerate(zip(paths, page_contents)):
            nav_parts = []
            if idx > 0:
                prev_path = paths[idx - 1]
                nav_parts.append(f'<b><a href="https://{dom}/{prev_path}">‹ Prev</a></b>')
            if idx < num_pages - 1:
                next_path = paths[idx + 1]
                nav_parts.append(f'<b><a href="https://{dom}/{next_path}">Next ›</a></b>')

            if nav_parts:
                nav_bar = f"<hr><p align='center'>{' &nbsp;|&nbsp; '.join(nav_parts)}</p>"
                updated_content = content + nav_bar
                try:
                    await self.edit_page(path=path, title=title, content=updated_content, domain=dom)
                except Exception as exc:
                    log.warning("Failed to link navigation on page %s (%s): %s", path, dom, exc)

    async def generate_telegraph_page(
        self,
        results: list[dict[str, Any]],
        query: str,
        site: str,
    ) -> str | None:
        """Formats search results into modern HTML layout and publishes to Telegraph with domain fallback."""
        if not results:
            return None

        safe_query = html.escape(query)
        safe_site = html.escape(site.capitalize())

        # Attempt to fetch rich movie/series metadata from Cinemeta
        meta_info = await fetch_cinemeta_info(query)

        telegraph_content: list[str] = []
        header = f"<h3>Search Results: <code>{safe_query}</code></h3>"
        header += f"<blockquote><b>Source:</b> {safe_site} &nbsp;•&nbsp; <b>Count:</b> {len(results)}</blockquote>"

        if meta_info:
            c_name = html.escape(str(meta_info.get("name") or query))
            c_year = html.escape(str(meta_info.get("year") or ""))
            c_rating = html.escape(str(meta_info.get("rating") or ""))
            c_desc = html.escape(str(meta_info.get("description") or ""))
            c_poster = html.escape(str(meta_info.get("poster") or ""), quote=True)
            genres_list = meta_info.get("genres") or []
            c_genres = html.escape(", ".join(genres_list)) if isinstance(genres_list, list) else ""

            header += "<hr>"
            if c_poster:
                header += f"<img src='{c_poster}'><br>"

            title_line = f"<b>{c_name}</b>"
            if c_year:
                title_line += f" ({c_year})"
            header += f"<h3>{title_line}</h3>"

            meta_details = []
            if c_rating:
                meta_details.append(f"<b>IMDb Rating:</b> ⭐ {c_rating}/10")
            if c_genres:
                meta_details.append(f"<b>Genres:</b> {c_genres}")

            if meta_details:
                header += f"<p>{' &nbsp;•&nbsp; '.join(meta_details)}</p>"

            if c_desc:
                header += f"<blockquote>{c_desc}</blockquote>"

        header += "<hr>"
        current_msg = header

        for idx, result in enumerate(results, start=1):
            name = html.escape(str(result.get("name") or result.get("title") or "Unknown"))
            size = html.escape(str(result.get("size") or "N/A"))
            seeders = result.get("seeders", 0)
            leechers = result.get("leechers", 0)
            raw_torrent_link = str(result.get("torrent") or result.get("url") or "#")
            raw_magnet_link = str(result.get("magnet") or "")
            safe_torrent_link = html.escape(raw_torrent_link, quote=True)

            item_html = f"<h4>{idx}. <a href='{safe_torrent_link}'>{name}</a></h4>"
            item_html += f"<p><b>Size:</b> <code>{size}</code> &nbsp;•&nbsp; <b>Seeders:</b> {seeders} &nbsp;•&nbsp; <b>Leechers:</b> {leechers}</p>"

            links_html = []
            if raw_magnet_link:
                quoted_mag = html.escape(urllib.parse.quote(raw_magnet_link), quote=True)
                links_html.append(f"<a href='http://t.me/share/url?url={quoted_mag}'>Share Magnet</a>")

            # Determine provider label (e.g. "ThePirateBay")
            provider_val = result.get("provider") or result.get("site") or result.get("indexer")
            if provider_val and str(provider_val).strip().lower() not in ("unknown", "none", ""):
                site_label = str(provider_val).strip()
            elif raw_torrent_link and raw_torrent_link != "#" and not raw_torrent_link.startswith("magnet:"):
                parsed_url = urllib.parse.urlparse(raw_torrent_link)
                domain_text = parsed_url.hostname or parsed_url.netloc or "Direct Link"
                site_label = domain_text.removeprefix("www.")
            else:
                site_label = "Source"

            safe_site_label = html.escape(site_label)
            target_link = raw_torrent_link if (raw_torrent_link and raw_torrent_link != "#") else raw_magnet_link

            if target_link and target_link != "#":
                safe_target_link = html.escape(target_link, quote=True)
                links_html.append(f"<a href='{safe_target_link}'>{safe_site_label}</a>")
            else:
                links_html.append(f"<b>{safe_site_label}</b>")

            if links_html:
                item_html += f"<blockquote>{' &nbsp;•&nbsp; '.join(links_html)}</blockquote>"

            item_html += "<hr>"

            if len((current_msg + item_html).encode("utf-8")) > 38000:
                telegraph_content.append(current_msg)
                current_msg = header

            current_msg += item_html

            if idx >= 300:  # Telegraph max item limit
                break

        if current_msg and current_msg != header:
            telegraph_content.append(current_msg)

        if not telegraph_content:
            return None

        page_title = f"Torrent Search - {query[:25]}"

        # Domain fallback order
        domains = self._get_domain_candidates()
        for domain in domains:
            try:
                paths: list[str] = []
                for content in telegraph_content:
                    res = await self.create_page(title=page_title, content=content, domain=domain)
                    if res and "path" in res:
                        paths.append(res["path"])

                if paths:
                    if len(paths) > 1:
                        await self.link_paginated_pages(
                            paths=paths,
                            title=page_title,
                            page_contents=telegraph_content,
                            domain=domain,
                        )
                    return f"https://{domain}/{paths[0]}"
            except Exception as exc:
                log.warning("Failed to publish Telegraph page on %s: %s", domain, exc)

        return None


telegraph_helper = TelegraphHelper()
