from __future__ import annotations

import json
import logging
import re
from collections.abc import Callable, Coroutine
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlparse, urlunparse

from ...config import settings
from ...utils.sorting import natural_path_sort_key
from ..direct.core import DirectDownloader

log = logging.getLogger(__name__)

ALLOWED_GOFILE_HOSTS = {
    "gofile.io",
    "www.gofile.io",
    "gofile.co",
    "www.gofile.co",
}

IGNORE_PREFIXES = (
    "/api/",
    "/public/",
    "/static/",
    "/cdn/",
    "/download",
)

ID_RE = re.compile(r"^[A-Za-z0-9_-]{4,40}$")

SHARE_REGEXES = [
    re.compile(r"/d/([A-Za-z0-9_-]{4,40})(/.*)?$", re.IGNORECASE),
    re.compile(r"/f/([A-Za-z0-9_-]{4,40})(/.*)?$", re.IGNORECASE),
    re.compile(r"/file/d/([A-Za-z0-9_-]{4,40})(/.*)?$", re.IGNORECASE),
    re.compile(r"[?&]file=([A-Za-z0-9_-]{4,40})", re.IGNORECASE),
    re.compile(r"/([A-Za-z0-9_-]{4,40})(?:/.*)?$", re.IGNORECASE),
]


def is_gofile_url(url: str) -> bool:
    """Returns True if the URL points to a GoFile share or GoFile bypass endpoint."""
    if not url:
        return False
    u = url.strip()
    if u.startswith(("gofile:", "gf:", "gf2tg:", "gfdl:")):
        return True

    clean = u
    for pfx in ("gofile:", "gf:", "gf2tg:", "gfdl:"):
        clean = clean.removeprefix(pfx)

    try:
        parsed = urlparse(clean)
        host = (parsed.hostname or "").lower()
        if not host:
            # Check if clean string is directly an ID e.g. "QGjyK2" with gofile prefix
            return bool(ID_RE.match(clean))

        bypass_host = (getattr(settings, "gofile_bypass_host", "gf.1drv.eu.org") or "gf.1drv.eu.org").lower()
        if host == bypass_host or host.endswith(f".{bypass_host}"):
            return True

        if host in ALLOWED_GOFILE_HOSTS or any(host.endswith(f".{h}") for h in ALLOWED_GOFILE_HOSTS):
            return True
        if ".gofile." in host:
            return True
    except Exception:
        pass

    return False


def extract_gofile_info(url: str) -> tuple[str, str] | None:
    """
    Extracts the (content_id, trailing_path) from a GoFile share URL
    following the Tampermonkey bypass script matching logic.
    Returns None if the URL does not match or is an ignored path.
    """
    if not url:
        return None

    clean = url.strip()
    for pfx in ("gofile:", "gf:", "gf2tg:", "gfdl:"):
        clean = clean.removeprefix(pfx)

    if not clean:
        return None

    # If the string is purely a content ID without scheme
    if ID_RE.match(clean):
        return clean, ""

    try:
        parsed = urlparse(clean)
    except Exception:
        return None

    host = (parsed.hostname or "").lower()
    path = parsed.path or "/"
    lower_path = path.lower()

    # Root paths without query file= parameter have no content ID
    if lower_path in ("/", "", "/index.html") and not re.search(r"[?&]file=", clean, re.IGNORECASE):
        return None

    # Opt-out check
    if "noredirect" in parsed.query.lower():
        return None

    # Ignore common API / static / download endpoints
    for pfx in IGNORE_PREFIXES:
        if lower_path.startswith(pfx):
            return None

    # Bypass host handling: if already pointing to bypass host, extract ID and trailing path
    bypass_host = (getattr(settings, "gofile_bypass_host", "gf.1drv.eu.org") or "gf.1drv.eu.org").lower()
    if host == bypass_host or host.endswith(f".{bypass_host}"):
        m = re.match(r"^/([A-Za-z0-9_-]{4,40})(/.*)?$", path)
        if m:
            cid = m.group(1)
            trailing = m.group(2) or ""
            if trailing == "/":
                trailing = ""
            return cid, trailing

    # Check allowed GoFile hosts
    is_allowed = (
        not host
        or host in ALLOWED_GOFILE_HOSTS
        or any(host.endswith(f".{h}") for h in ALLOWED_GOFILE_HOSTS)
        or ".gofile." in host
    )
    if not is_allowed:
        return None

    # Match share patterns
    for pattern in SHARE_REGEXES:
        m = pattern.search(path)
        if not m:
            m = pattern.search(clean)
        if m and m.group(1):
            cid = m.group(1)
            if not ID_RE.match(cid):
                continue
            trailing = ""
            if len(m.groups()) >= 2 and m.group(2):
                trailing = m.group(2)
                if trailing == "/":
                    trailing = ""
            return cid, trailing

    return None


def convert_gofile_url_to_bypass_url(url: str, bypass_host: str | None = None) -> str:
    """
    Translates a GoFile share URL (e.g. https://gofile.io/d/QGjyK2) into
    the direct bypass stream URL (e.g. https://gf.1drv.eu.org/QGjyK2)
    preserving trailing paths, query parameters (except noredirect), and fragments.
    """
    target_host = bypass_host or getattr(settings, "gofile_bypass_host", "gf.1drv.eu.org") or "gf.1drv.eu.org"
    info = extract_gofile_info(url)
    if not info:
        return url

    cid, trailing = info
    if trailing and not trailing.startswith("/"):
        trailing = "/" + trailing

    clean = url.strip()
    for pfx in ("gofile:", "gf:", "gf2tg:", "gfdl:"):
        clean = clean.removeprefix(pfx)

    try:
        parsed = urlparse(clean)
        # Filter out noredirect if present
        query = ""
        if parsed.query:
            q_dict = parse_qs(parsed.query, keep_blank_values=True)
            q_dict.pop("noredirect", None)
            if q_dict:
                query = urlencode(q_dict, doseq=True)

        target_path = f"/{cid}{trailing}"
        return urlunparse(("https", target_host, target_path, "", query, parsed.fragment or ""))
    except Exception:
        return f"https://{target_host}/{cid}{trailing}"


class GoFileDownloader:
    """
    Downloader for GoFile links using the bypass proxy logic from the userscript.
    Leverages DirectDownloader to follow HTTP 302 redirects, stream content chunks,
    honor speed limits, provide live progress reporting, and naturally sort outputs.
    """

    def __init__(
        self,
        dest_dir: Path,
        progress_cb: Callable[[int, int, str, str | None], Coroutine[None, None, None]] | None = None,
        bypass_host: str | None = None,
    ):
        self.dest_dir = dest_dir
        self.progress_cb = progress_cb
        self.bypass_host = bypass_host or getattr(settings, "gofile_bypass_host", "gf.1drv.eu.org") or "gf.1drv.eu.org"

    def _parse_input_urls(self, url_or_urls: str | list[str | dict[str, str]]) -> list[dict[str, str]]:
        """Parses various input formats (single URL, JSON array, space/newline separated list)."""
        raw_items: list[str | dict[str, str]] = []

        if isinstance(url_or_urls, list):
            raw_items = url_or_urls
        elif isinstance(url_or_urls, str):
            trimmed = url_or_urls.strip()
            if trimmed.startswith("[") and trimmed.endswith("]"):
                try:
                    parsed = json.loads(trimmed)
                    if isinstance(parsed, list):
                        raw_items = parsed
                except Exception as e:
                    log.debug("Failed parsing GoFile JSON input: %s", e)

            if not raw_items:
                lines = [line.strip() for line in trimmed.split() if line.strip()]
                raw_items = lines if lines else [trimmed]

        items: list[dict[str, str]] = []
        for it in raw_items:
            if isinstance(it, dict) and it.get("url"):
                u = str(it["url"]).strip()
                bypass_u = convert_gofile_url_to_bypass_url(u, bypass_host=self.bypass_host)
                items.append({
                    "url": bypass_u,
                    "filename": it.get("filename", ""),
                    "path": it.get("path", ""),
                })
            elif isinstance(it, str) and it.strip():
                u = it.strip()
                bypass_u = convert_gofile_url_to_bypass_url(u, bypass_host=self.bypass_host)
                items.append({
                    "url": bypass_u,
                    "filename": "",
                    "path": "",
                })

        return items

    async def download(self, url_or_urls: str | list[str | dict[str, str]]) -> list[Path]:
        """
        Downloads one or more GoFile items using the bypass proxy and DirectDownloader.
        Downloads items serially in naturally sorted hierarchical order.
        """
        items = self._parse_input_urls(url_or_urls)
        if not items:
            raise ValueError("No valid GoFile URLs provided for download.")

        def _item_natural_key(it: dict[str, str]):
            fn = it.get("filename") or ""
            sub = it.get("path") or ""
            if fn or sub:
                return natural_path_sort_key(Path(sub) / fn)
            parsed_path = Path(urlparse(it["url"]).path).name
            return natural_path_sort_key(parsed_path or it["url"])

        if len(items) > 1:
            items.sort(key=_item_natural_key)

        direct_downloader = DirectDownloader(dest_dir=self.dest_dir, progress_cb=self.progress_cb)
        downloaded = await direct_downloader.download(items)

        # Sort results naturally before returning
        downloaded.sort(key=natural_path_sort_key)
        return downloaded


async def download_gofile(
    url_or_urls: str | list[str | dict[str, str]],
    dest_dir: Path,
    progress_cb: Callable[[int, int, str, str | None], Coroutine[None, None, None]] | None = None,
    bypass_host: str | None = None,
) -> list[Path]:
    """Convenience async helper to download GoFile content via the bypass proxy."""
    downloader = GoFileDownloader(dest_dir=dest_dir, progress_cb=progress_cb, bypass_host=bypass_host)
    return await downloader.download(url_or_urls)
