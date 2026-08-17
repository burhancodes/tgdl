from __future__ import annotations

import asyncio
import ipaddress
import json
import logging
import re
import socket
import time
from collections.abc import Callable, Coroutine
from pathlib import Path
from urllib.parse import unquote, urlparse

import aiohttp
from aiofiles import open as aiopen

from ...config import settings

log = logging.getLogger(__name__)

_CHUNK_SIZE = 1024 * 1024  # 1MB chunks


def get_aiohttp_connector() -> aiohttp.TCPConnector:
    """Returns an aiohttp TCPConnector configured with IPv6 family if force_ipv6 is enabled."""
    if getattr(settings, "force_ipv6", False):
        return aiohttp.TCPConnector(family=socket.AF_INET6)
    return aiohttp.TCPConnector()


class DirectDownloadError(Exception):
    pass


async def is_url_private_ip(url: str) -> bool:
    """Resolves URL hostname and checks if resolved IP address falls in private/reserved ranges."""
    try:
        parsed = urlparse(url)
        hostname = parsed.hostname
        if not hostname:
            return True

        def _resolve():
            return socket.getaddrinfo(hostname, None)

        addr_info = await asyncio.to_thread(_resolve)
        for family, socktype, proto, canonname, sockaddr in addr_info:
            ip_str = sockaddr[0]
            ip_obj = ipaddress.ip_address(ip_str)
            if (
                ip_obj.is_private
                or ip_obj.is_loopback
                or ip_obj.is_link_local
                or ip_obj.is_reserved
                or ip_obj.is_multicast
                or ip_obj.is_unspecified
            ):
                return True
        return False
    except Exception as e:
        log.warning("SSRF DNS resolution check failed for %s: %s", url, e)
        return True


DIRECT_FILE_EXTENSIONS = {
    # Video
    ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".wmv", ".3gp", ".m4v", ".ts", ".f4v", ".vob", ".m3u8",
    # Audio
    ".mp3", ".flac", ".m4a", ".aac", ".opus", ".ogg", ".wav", ".wma", ".alac", ".aiff",
    # Archives & Compressed
    ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".iso", ".tgz", ".tbz2", ".zst", ".cab", ".dmg",
    # Executables & Packages
    ".apk", ".exe", ".bin", ".msi", ".deb", ".rpm", ".appimage", ".app", ".ipa",
    # Documents & Ebooks
    ".pdf", ".epub", ".mobi", ".djvu", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
}


def is_direct_url(url: str) -> bool:
    """Determines if a URL is a direct file download link based on scheme or extension."""
    if not url:
        return False
    if url.startswith("direct:"):
        return True
    try:
        urls = []
        if url.startswith("[") and url.endswith("]"):
            try:
                parsed = json.loads(url)
                if isinstance(parsed, list):
                    urls = [str(u).strip() for u in parsed if str(u).strip()]
            except Exception as e:
                log.debug("Failed parsing JSON list URL in is_direct_url: %s", e)
        if not urls:
            urls = [u.strip() for u in url.split() if u.strip().startswith(("http://", "https://"))]

        for u in urls:
            clean_u = u.split("?", 1)[0].split("#", 1)[0]
            parsed = urlparse(clean_u)
            path_ext = Path(parsed.path).suffix.lower()
            if path_ext in DIRECT_FILE_EXTENSIONS:
                return True
    except Exception:
        # expected: non-standard URL structure
        pass
    return False


async def is_m3u8_url(url: str, session: aiohttp.ClientSession | None = None) -> bool:
    """Checks if a URL is an M3U8/HLS stream playlist by extension or content sniffing."""
    if not url:
        return False
    try:
        clean_u = url.split("?", 1)[0].split("#", 1)[0]
        parsed = urlparse(clean_u)
        path_ext = Path(parsed.path).suffix.lower()
        if path_ext == ".m3u8":
            return True

        if not settings.allow_private_network_urls:
            if await is_url_private_ip(url):
                return False

        req_headers = {
            "User-Agent": "Mozilla/5.0 (X11; Linux x86_64)",
            "Range": "bytes=0-511",
        }

        async def _check(sess: aiohttp.ClientSession) -> bool:
            try:
                async with sess.get(url, headers=req_headers, allow_redirects=True, timeout=aiohttp.ClientTimeout(total=10.0)) as resp:
                    if resp.status >= 400:
                        return False
                    ct = (resp.headers.get("Content-Type") or resp.headers.get("content-type") or "").lower()
                    if any(m in ct for m in ("mpegurl", "vnd.apple.mpegurl", "x-mpegurl")):
                        return True
                    chunk = await resp.content.read(512)
                    if b"#EXTM3U" in chunk:
                        return True
            except Exception as e:
                log.debug("is_m3u8_url network check failed for %s: %s", url, e)
                return False
            return False

        if session:
            return await _check(session)
        else:
            async with aiohttp.ClientSession(connector=get_aiohttp_connector()) as sess:
                return await _check(sess)
    except Exception as e:
        log.debug("Error in is_m3u8_url: %s", e)
        return False


def get_filename_from_url(url: str, headers: dict[str, str] | None = None) -> str:
    """Extract filename from Content-Disposition header or URL path."""
    if headers:
        cd = headers.get("Content-Disposition") or headers.get("content-disposition")
        if cd:
            filenames = re.findall(r'filename\*?=(?:["\']?([^"\';]+)["\']?|UTF-8\'\'([^"\';]+))', cd, re.IGNORECASE)
            if filenames:
                fn = filenames[0][1] or filenames[0][0]
                if fn:
                    return unquote(fn).strip()

    parsed = urlparse(url)
    filename = Path(parsed.path).name
    filename = unquote(filename).strip()
    if not filename or filename in ("/", "\\"):
        filename = f"direct_file_{int(time.time())}.bin"
    return filename


class DirectDownloader:
    """
    Direct link / HTTP downloader inspired by mirror-leech-telegram-bot's DirectListener & direct_downloader.py.
    """

    def __init__(
        self,
        dest_dir: Path,
        progress_cb: Callable[[int, int, str], Coroutine[None, None, None]] | None = None,
        headers: dict[str, str] | None = None,
    ) -> None:
        self.dest_dir = dest_dir
        self.progress_cb = progress_cb
        self.custom_headers = headers or {}

        self.processed_bytes = 0
        self.total_bytes = 0
        self.start_time = time.time()
        self.is_downloading = False
        self.is_cancelled = False
        self.failed_count = 0
        self.downloaded_files: list[Path] = []
        self.current_filename = ""

    @property
    def speed(self) -> float:
        elapsed = time.time() - self.start_time
        if elapsed > 0:
            return self.processed_bytes / elapsed
        return 0.0

    def cancel(self) -> None:
        self.is_cancelled = True

    async def _download_content_item(
        self,
        session: aiohttp.ClientSession,
        url: str,
        filename: str | None = None,
        subpath: str | None = None,
    ) -> Path:
        if self.is_cancelled:
            raise asyncio.CancelledError("Download cancelled before starting item.")

        if not settings.allow_private_network_urls:
            if await is_url_private_ip(url):
                log.warning("SSRF protection blocked URL %s (resolves to private/reserved IP)", url)
                raise DirectDownloadError(f"Access to private/internal network URL '{url}' is prohibited.")

        save_dir = self.dest_dir
        if subpath:
            save_dir = save_dir / subpath
        save_dir.mkdir(parents=True, exist_ok=True)

        clean_u = url.split("?", 1)[0].split("#", 1)[0]
        parsed_url = urlparse(clean_u)
        path_ext = Path(parsed_url.path).suffix.lower()

        if path_ext == ".m3u8":
            if not filename:
                base_fn = get_filename_from_url(url)
                if base_fn.lower().endswith(".m3u8"):
                    filename = base_fn[:-5] + ".mp4"
                else:
                    filename = Path(base_fn).stem + ".mp4"
            elif not filename.lower().endswith(".mp4"):
                if filename.lower().endswith(".m3u8"):
                    filename = filename[:-5] + ".mp4"
                else:
                    filename = Path(filename).stem + ".mp4"

            self.current_filename = filename
            out_file = save_dir / filename

            from .hls import download_hls

            async def progress_adapter(current: float, total: float, fn: str, u: str | None = None) -> None:
                if self.progress_cb:
                    pct_processed = int(current)
                    pct_total = int(total)
                    try:
                        await self.progress_cb(pct_processed, pct_total, fn, url)
                    except TypeError:
                        try:
                            await self.progress_cb(pct_processed, pct_total, fn)
                        except Exception as pe:
                            log.debug("Progress callback error in HLS download: %s", pe)
                    except Exception as pe:
                        log.debug("Progress callback error in HLS download: %s", pe)

            res_path = await download_hls(
                url=url,
                dest_path=out_file,
                headers=self.custom_headers,
                progress_cb=progress_adapter,
                is_cancelled=lambda: self.is_cancelled,
            )
            self.current_filename = res_path.name
            return res_path

        req_headers = {"User-Agent": "Mozilla/5.0 (X11; Linux x86_64)"}
        req_headers.update(self.custom_headers)

        async with session.get(url, headers=req_headers, allow_redirects=True) as resp:
            if resp.status >= 400:
                raise DirectDownloadError(f"HTTP {resp.status} - {resp.reason}")

            if not filename:
                filename = get_filename_from_url(url, dict(resp.headers))

            self.current_filename = filename
            out_file = save_dir / filename
            part_file = save_dir / f"{filename}.part"

            ext = Path(filename).suffix.lower()
            raw_content_type = resp.headers.get("Content-Type", "")
            content_type = raw_content_type.split(";")[0].strip().lower()
            is_text_ext = ext in (".html", ".htm", ".txt", ".json", ".xml", ".xhtml")

            # 1. Content-Type check
            if (content_type in ("text/html", "text/plain") or content_type.startswith(("text/html", "text/plain"))) and not is_text_ext:
                raise DirectDownloadError(
                    f"Expected a file but got an HTML/text response (Content-Type: {raw_content_type}) — the URL may be restricted, expired, or require authentication."
                )

            file_size = 0
            if "Content-Length" in resp.headers:
                try:
                    file_size = int(resp.headers["Content-Length"])
                except Exception:
                    # expected: invalid or non-integer Content-Length header
                    file_size = 0

            # 3. Size sanity flag (log warning only)
            from ...utils.media import VIDEO_EXT
            large_media_ext = VIDEO_EXT | {".zip", ".rar", ".7z", ".tar", ".gz", ".xz", ".iso", ".bin", ".dmg", ".apk", ".exe", ".mp3", ".flac", ".m4a", ".wav"}
            if 0 < file_size < 50 * 1024 and ext in large_media_ext:
                log.warning("File '%s' has a suspiciously small size (%d bytes) for media type '%s'", filename, file_size, ext)

            # 2. Magic-byte sniff (~512 bytes)
            peek_chunk = await resp.content.read(512)
            if peek_chunk and not is_text_ext:
                peek_text = peek_chunk.decode("utf-8", errors="ignore").lstrip()
                if peek_text.lower().startswith(("<!doctype html", "<html", "<?xml")):
                    raise DirectDownloadError(
                        f"Expected a file but got an HTML/text response (Content-Type: {raw_content_type}) — the URL may be restricted, expired, or require authentication."
                    )

            self.total_bytes += file_size
            item_processed = 0

            log.info("Downloading direct link %s to %s (size: %s bytes)", url, out_file, file_size)

            from ...pacing import DownloadThrottler
            throttler = DownloadThrottler(settings.global_download_speed_limit)

            try:
                async with aiopen(part_file, "wb") as f:
                    if peek_chunk:
                        if self.is_cancelled:
                            if part_file.exists():
                                part_file.unlink(missing_ok=True)
                            raise asyncio.CancelledError("Download cancelled during file stream.")

                        await f.write(peek_chunk)
                        chunk_len = len(peek_chunk)
                        await throttler.consume(chunk_len)
                        item_processed += chunk_len
                        self.processed_bytes += chunk_len

                    async for chunk in resp.content.iter_chunked(_CHUNK_SIZE):
                        if self.is_cancelled:
                            if part_file.exists():
                                part_file.unlink(missing_ok=True)
                            raise asyncio.CancelledError("Download cancelled during file stream.")

                        await f.write(chunk)
                        chunk_len = len(chunk)
                        await throttler.consume(chunk_len)
                        item_processed += chunk_len
                        self.processed_bytes += chunk_len

                        if self.progress_cb:
                            try:
                                await self.progress_cb(self.processed_bytes, self.total_bytes, filename, url)
                            except TypeError:
                                try:
                                    await self.progress_cb(self.processed_bytes, self.total_bytes, filename)
                                except Exception as e:
                                    log.debug("Progress callback error: %s", e)
                            except Exception as e:
                                log.debug("Progress callback error: %s", e)

                if part_file.exists():
                    part_file.replace(out_file)
                return out_file
            except Exception:
                if part_file.exists():
                    try:
                        part_file.unlink(missing_ok=True)
                    except Exception:
                        # expected: part_file already removed
                        pass
                raise

    async def download(
        self,
        contents: str | list[dict[str, str]],
    ) -> list[Path]:
        """
        Download direct link(s).
        `contents` can be a single URL string or a list of content dicts:
        `[{"url": "...", "filename": "...", "path": "..."}, ...]`
        """
        self.is_downloading = True
        self.start_time = time.time()
        self.dest_dir.mkdir(parents=True, exist_ok=True)

        items: list[dict[str, str]] = []
        if isinstance(contents, str):
            try:
                parsed = json.loads(contents)
                if isinstance(parsed, list):
                    for u in parsed:
                        if isinstance(u, str) and u.strip():
                            clean_u = u.strip().removeprefix("direct:").removeprefix("mirror:")
                            items.append({"url": clean_u, "filename": "", "path": ""})
            except Exception as e:
                log.debug("Failed parsing JSON contents in DirectDownloader: %s", e)

            if not items:
                lines = [u.strip() for u in contents.split() if u.strip().startswith(("http://", "https://", "direct:", "mirror:"))]
                if len(lines) > 1:
                    for u in lines:
                        clean_u = u.removeprefix("direct:").removeprefix("mirror:")
                        items.append({"url": clean_u, "filename": "", "path": ""})
                else:
                    clean_u = contents.strip().removeprefix("direct:").removeprefix("mirror:")
                    items.append({"url": clean_u, "filename": "", "path": ""})
        elif isinstance(contents, list):
            for c in contents:
                if isinstance(c, dict) and "url" in c:
                    clean_u = str(c["url"]).removeprefix("direct:").removeprefix("mirror:")
                    items.append({
                        "url": clean_u,
                        "filename": c.get("filename", ""),
                        "path": c.get("path", ""),
                    })
                elif isinstance(c, str) and c.strip():
                    clean_u = c.strip().removeprefix("direct:").removeprefix("mirror:")
                    items.append({"url": clean_u, "filename": "", "path": ""})

        if not items:
            raise DirectDownloadError("No direct URLs provided for download.")

        async with aiohttp.ClientSession(
            connector=get_aiohttp_connector(),
            timeout=aiohttp.ClientTimeout(total=None, connect=30.0)
        ) as session:
            for item in items:
                if self.is_cancelled:
                    break
                if self.progress_cb:
                    try:
                        await self.progress_cb(self.processed_bytes, self.total_bytes, "", item["url"])
                    except Exception as e:
                        log.debug("Progress callback error before item download: %s", e)
                try:
                    downloaded_file = await self._download_content_item(
                        session=session,
                        url=item["url"],
                        filename=item.get("filename"),
                        subpath=item.get("path"),
                    )
                    self.downloaded_files.append(downloaded_file)
                except asyncio.CancelledError:
                    log.info("Direct download cancelled by user.")
                    raise
                except Exception as e:
                    self.failed_count += 1
                    log.error("Failed to download direct item %s: %s", item.get("url"), e)

        self.is_downloading = False
        if self.failed_count == len(items) and len(items) > 0:
            raise DirectDownloadError(f"All {len(items)} direct file downloads failed.")

        return self.downloaded_files


async def download_direct(
    url_or_contents: str | list[dict[str, str]],
    dest_dir: Path,
    progress_cb: Callable[[int, int, str], Coroutine[None, None, None]] | None = None,
    headers: dict[str, str] | None = None,
) -> list[Path]:
    """Helper function to execute direct link download."""
    downloader = DirectDownloader(dest_dir=dest_dir, progress_cb=progress_cb, headers=headers)
    return await downloader.download(url_or_contents)
