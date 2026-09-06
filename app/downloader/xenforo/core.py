from __future__ import annotations

import asyncio
import logging
import os
import re
import shutil
import time
from collections.abc import Callable, Coroutine
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.parse import unquote, urlparse

import aiofiles
import aiohttp

from ...config import settings
from ...utils.sorting import natural_path_sort_key
from ..aria2c.torrent.magnetio_client import _rpc_call
from ..aria2c.torrent.magnetio_daemon import start_magnetio_daemon
from ..direct.core import get_aiohttp_connector, get_filename_from_url, is_url_private_ip
from .device_profile import (
    DEFAULT_FALLBACK_UA,
    extract_user_agent_from_cookies,
    get_device_headers,
    get_user_agent_path,
    parse_user_agent,
    resolve_user_device_agent,
)

log = logging.getLogger(__name__)

_CHUNK_SIZE = 1024 * 1024  # 1MB chunks


class XenForoDownloadError(Exception):
    pass


@dataclass
class DownloadResult:
    ok: bool
    files: list[Path] = field(default_factory=list)
    error_tail: str = ""
    attempts: int = 0


def get_user_cookies_path(user_id: int | str) -> Path:
    """Returns path to user-specific cookies.txt."""
    return settings.auth_dir / str(user_id) / "cookies.txt"


def get_cookies_path(user_id: int | str | None = None) -> Path | None:
    """Resolves cookies.txt path for user_id or global default fallback."""
    if user_id:
        user_cookies = get_user_cookies_path(user_id)
        if user_cookies.exists() and user_cookies.is_file():
            return user_cookies
    auth_global_cookies = settings.auth_dir / "cookies.txt"
    if auth_global_cookies.exists() and auth_global_cookies.is_file():
        return auth_global_cookies
    root_cookies = Path("./cookies.txt")
    if root_cookies.exists() and root_cookies.is_file():
        return root_cookies
    return None


def load_user_cookies_text(user_id: int | str | None = None) -> str | None:
    """Reads cookies.txt content as string for the user or global fallback."""
    cookie_path = get_cookies_path(user_id=user_id)
    if cookie_path and cookie_path.exists() and cookie_path.is_file():
        try:
            return cookie_path.read_text(encoding="utf-8", errors="replace")
        except Exception as e:
            log.warning("Failed to read cookies file %s: %s", cookie_path, e)
    return None


def is_xenforo_url(url: str) -> bool:
    """Returns True if the URL is a XenForo / SimpCity forum thread or post link."""
    if not url:
        return False
    u = url.strip().lower()
    if u.startswith(("xenforo:", "simpcity:", "forum:", "fpd:")):
        return True

    # Common XenForo forum domains and URL patterns
    if re.search(r"simpcity\.(cr|is|cz|hk|rs|ax|su|st|top|to)\b", u):
        return True
    if re.search(r"/threads/[a-zA-Z0-9._-]+\.\d+", u):
        return True
    if re.search(r"/posts/\d+", u):
        return True

    return False


def is_simpcity_url(url: str) -> bool:
    """Returns True if the URL is specifically for SimpCity forums."""
    if not url:
        return False
    u = url.strip().lower()
    if u.startswith("simpcity:"):
        return True
    return bool(re.search(r"simpcity\.(cr|is|cz|hk|rs|ax|su|st|top|to)\b", u))


class XenForoDownloader:
    """
    Downloader for XenForo forum threads and posts (SimpCity and others)
    powered by the Node.js scraper sidecar with user_id managed cookies.txt and
    strict device/browser spoofing support.
    """

    def __init__(
        self,
        dest_dir: Path,
        user_id: int | str | None = None,
        on_progress: Callable[[int, str | None, str | None], None] | None = None,
        on_byte_progress: Callable[[int, int, str], Coroutine[None, None, None]] | None = None,
        passwords: list[str] | None = None,
        max_pages: int = 1,
        user_agent: str | None = None,
    ) -> None:
        self.dest_dir = Path(dest_dir)
        self.user_id = user_id
        self.on_progress = on_progress
        self.on_byte_progress = on_byte_progress
        self.passwords = passwords or []
        self.max_pages = max_pages
        self.user_agent = user_agent
        self.effective_ua = resolve_user_device_agent(
            user_id=user_id,
            cookies_text=load_user_cookies_text(user_id),
            custom_ua=user_agent,
        )

        self.downloaded_files: list[Path] = []
        self.total_downloaded_bytes = 0
        self.is_cancelled = False

    def cancel(self) -> None:
        self.is_cancelled = True

    async def scrape(self, url: str) -> dict[str, Any]:
        """Calls the scraper sidecar via JSON-RPC xenforo.scrape with strict device spoofing."""
        # Ensure scraper daemon is running
        try:
            await start_magnetio_daemon()
        except Exception as e:
            log.debug("start_magnetio_daemon notice: %s", e)

        cookies_txt = load_user_cookies_text(self.user_id)
        self.effective_ua = resolve_user_device_agent(
            user_id=self.user_id,
            cookies_text=cookies_txt,
            custom_ua=self.user_agent,
        )

        params: dict[str, Any] = {
            "url": url,
            "user_id": str(self.user_id) if self.user_id else None,
            "cookies": cookies_txt,
            "userAgent": self.effective_ua,
            "passwords": self.passwords,
            "maxPages": self.max_pages,
        }

        try:
            res = await _rpc_call("xenforo.scrape", params)
            if not isinstance(res, dict):
                raise XenForoDownloadError("Invalid response from scraper service.")
            return res
        except Exception as e:
            log.warning("XenForo scraper RPC call failed for %s: %s", url, e)
            raise XenForoDownloadError(f"Scraper error: {e}") from e

    async def download(self, url: str) -> DownloadResult:
        """Scrapes the forum thread/post and downloads all resolved media files."""
        log.info("Starting XenForo forum download for %s (user_id: %s)", url, self.user_id)
        self.dest_dir.mkdir(parents=True, exist_ok=True)

        scrape_data = await self.scrape(url)
        thread_title = scrape_data.get("threadTitle") or "Thread"
        posts = scrape_data.get("posts") or []

        if not posts:
            return DownloadResult(ok=False, error_tail="No posts or downloadable media found.")

        # Collect all resource items
        all_items: list[dict[str, Any]] = []
        for post in posts:
            resources = post.get("resources") or []
            for res in resources:
                if res.get("resolvedUrl"):
                    all_items.append(res)

        if not all_items:
            return DownloadResult(ok=False, error_tail="No downloadable media resources resolved.")

        log.info(
            "XenForo scraper found %d resources across %d posts for '%s'",
            len(all_items),
            len(posts),
            thread_title,
        )

        connector = get_aiohttp_connector()
        timeout = aiohttp.ClientTimeout(total=1800, connect=30, sock_read=120)

        # Download each item
        download_count = 0
        async with aiohttp.ClientSession(connector=connector, timeout=timeout) as session:
            for item in all_items:
                if self.is_cancelled:
                    log.info("XenForo download cancelled by user.")
                    break

                res_url = item.get("resolvedUrl")
                if not res_url:
                    continue

                folder_name = item.get("folderName") or thread_title
                target_subfolder = self.dest_dir / folder_name
                target_subfolder.mkdir(parents=True, exist_ok=True)

                headers = item.get("headers") or {}
                fn_hint = item.get("filename")

                try:
                    downloaded_file = await self._download_single_file(
                        session=session,
                        url=res_url,
                        dest_dir=target_subfolder,
                        filename_hint=fn_hint,
                        headers=headers,
                    )

                    if downloaded_file and downloaded_file.exists():
                        self.downloaded_files.append(downloaded_file)
                        download_count += 1

                        if self.on_progress:
                            self.on_progress(download_count, downloaded_file.name, res_url)

                except Exception as e:
                    log.warning("Failed to download item %s: %s", res_url, e)

        # Naturally sort downloaded files
        self.downloaded_files.sort(key=natural_path_sort_key)

        if not self.downloaded_files:
            return DownloadResult(ok=False, error_tail="All media downloads failed.")

        return DownloadResult(ok=True, files=self.downloaded_files)

    async def _download_single_file(
        self,
        session: aiohttp.ClientSession,
        url: str,
        dest_dir: Path,
        filename_hint: str | None = None,
        headers: dict[str, str] | None = None,
    ) -> Path:
        """Downloads a single resolved media file with chunked streaming and strict device spoofing."""
        if not settings.allow_private_network_urls:
            if await is_url_private_ip(url):
                raise ValueError(f"Target URL points to a private/forbidden IP address: {url}")

        effective_ua = getattr(self, "effective_ua", None) or resolve_user_device_agent(
            user_id=self.user_id,
            custom_ua=self.user_agent,
        )
        base_device_headers = get_device_headers(
            effective_ua,
            dest_type="image",
            referer=headers.get("Referer") if headers else url,
        )
        req_headers = {
            **base_device_headers,
            **(headers or {}),
        }

        async with session.get(url, headers=req_headers, allow_redirects=True) as resp:
            if resp.status >= 400:
                raise ValueError(f"HTTP {resp.status} fetching media from {url}")

            filename = filename_hint
            if not filename:
                filename = get_filename_from_url(str(resp.url), dict(resp.headers))

            # Clean and sanitize filename
            filename = re.sub(r'[\\/:*?"<>|]', "-", filename).strip()
            if not filename:
                filename = f"media_{int(time.time())}.bin"

            target_path = dest_dir / filename

            # Handle duplicate filenames
            if target_path.exists():
                stem = target_path.stem
                suffix = target_path.suffix
                counter = 1
                while target_path.exists():
                    target_path = dest_dir / f"{stem} ({counter}){suffix}"
                    counter += 1

            total_length = int(resp.headers.get("Content-Length", 0))
            downloaded = 0

            async with aiofiles.open(target_path, "wb") as f:
                async for chunk in resp.content.iter_chunked(_CHUNK_SIZE):
                    if self.is_cancelled:
                        if target_path.exists():
                            target_path.unlink(missing_ok=True)
                        raise asyncio.CancelledError("Download cancelled.")

                    await f.write(chunk)
                    chunk_len = len(chunk)
                    downloaded += chunk_len
                    self.total_downloaded_bytes += chunk_len

                    if self.on_byte_progress:
                        try:
                            await self.on_byte_progress(downloaded, total_length, target_path.name)
                        except Exception:
                            pass

            return target_path


async def download_xenforo_post(
    url: str,
    dest_dir: Path,
    user_id: int | str | None = None,
    on_progress: Callable[[int, str | None, str | None], None] | None = None,
    passwords: list[str] | None = None,
    max_pages: int = 1,
    user_agent: str | None = None,
) -> list[Path]:
    """Helper to download XenForo thread/post media and return list of file paths."""
    downloader = XenForoDownloader(
        dest_dir=dest_dir,
        user_id=user_id,
        on_progress=on_progress,
        passwords=passwords,
        max_pages=max_pages,
        user_agent=user_agent,
    )
    result = await downloader.download(url)
    if not result.ok:
        raise XenForoDownloadError(result.error_tail or "Failed to download XenForo media.")
    return result.files


async def run_with_progress(
    url: str,
    dest_dir: Path,
    on_progress: Callable[[int, str | None, str | None], None] | None = None,
    extra_args: list[str] | None = None,
    register_proc: Callable[[asyncio.subprocess.Process | None], None] | None = None,
    user_id: int | str | None = None,
    config_path: Path | None = None,
) -> DownloadResult:
    """
    Standard run_with_progress interface for tgdl-bot job manager.
    """
    passwords = []
    max_pages = 1
    user_agent = None

    if extra_args:
        i = 0
        while i < len(extra_args):
            arg = str(extra_args[i]).strip()
            if arg in ("-p", "--password", "-pass") and i + 1 < len(extra_args):
                passwords.append(str(extra_args[i + 1]).strip())
                i += 1
            elif arg.startswith(("--password=", "-p=", "--pass=")):
                passwords.append(arg.split("=", 1)[1].strip())
            elif arg in ("--max-pages", "-pages") and i + 1 < len(extra_args):
                try:
                    max_pages = int(extra_args[i + 1])
                except ValueError:
                    pass
                i += 1
            elif arg in ("-ua", "--ua", "--user-agent") and i + 1 < len(extra_args):
                user_agent = str(extra_args[i + 1]).strip()
                i += 1
            elif arg.startswith(("--ua=", "--user-agent=")):
                user_agent = arg.split("=", 1)[1].strip()
            i += 1

    downloader = XenForoDownloader(
        dest_dir=dest_dir,
        user_id=user_id,
        on_progress=on_progress,
        passwords=passwords,
        max_pages=max_pages,
        user_agent=user_agent,
    )

    return await downloader.download(url)
