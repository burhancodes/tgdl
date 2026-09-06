from __future__ import annotations

import logging
import re
from pathlib import Path
from typing import Any

from ...config import settings

log = logging.getLogger(__name__)

DEFAULT_FALLBACK_UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
)


def extract_user_agent_from_cookies(text: str | None) -> str | None:
    """
    Extracts User-Agent string from Netscape cookies.txt comment lines.
    Supported patterns:
      # User-Agent: Mozilla/5.0 ...
      # UA: Mozilla/5.0 ...
      # UserAgent: Mozilla/5.0 ...
      # Browser: Mozilla/5.0 ...
    """
    if not text:
        return None

    for line in text.splitlines():
        line = line.strip()
        if line.startswith("#"):
            m = re.match(r"^#\s*(?:user-agent|useragent|ua|browser|device)\s*:\s*(.+)$", line, re.IGNORECASE)
            if m:
                ua = m.group(1).strip()
                if len(ua) > 10 and "/" in ua:
                    return ua
    return None


def get_user_agent_path(user_id: int | str | None = None) -> Path | None:
    """
    Resolves path to user_id specific user-agent.txt or global fallback.
    """
    if user_id:
        user_ua = settings.auth_dir / str(user_id) / "user-agent.txt"
        if user_ua.exists() and user_ua.is_file():
            return user_ua
        user_ua_alt = settings.auth_dir / str(user_id) / "ua.txt"
        if user_ua_alt.exists() and user_ua_alt.is_file():
            return user_ua_alt

    auth_global_ua = settings.auth_dir / "user-agent.txt"
    if auth_global_ua.exists() and auth_global_ua.is_file():
        return auth_global_ua

    root_ua = Path("./user-agent.txt")
    if root_ua.exists() and root_ua.is_file():
        return root_ua

    return None


def parse_user_agent(ua_string: str | None) -> dict[str, Any]:
    """
    Analyzes a User-Agent string to determine browser engine, version, OS, and platform hints.
    """
    ua = (ua_string or "").strip() or DEFAULT_FALLBACK_UA

    is_mobile = bool(re.search(r"Android|iPhone|iPad|iPod|Mobile|webOS|BlackBerry|IEMobile|Opera Mini", ua, re.IGNORECASE))
    platform = "Windows"
    platform_version = "10.0.0"

    if re.search(r"Windows", ua, re.IGNORECASE):
        platform = "Windows"
        if "Windows NT 10.0" in ua:
            platform_version = "10.0.0"
        elif "Windows NT 6.3" in ua:
            platform_version = "8.1.0"
        elif "Windows NT 6.1" in ua:
            platform_version = "7.0.0"
    elif re.search(r"Macintosh|Mac OS X", ua, re.IGNORECASE):
        platform = "macOS"
        mac_m = re.search(r"Mac OS X (\d+[._]\d+[._]\d+)", ua, re.IGNORECASE)
        if mac_m:
            platform_version = mac_m.group(1).replace("_", ".")
        else:
            platform_version = "14.0.0"
    elif re.search(r"Android", ua, re.IGNORECASE):
        platform = "Android"
        and_m = re.search(r"Android (\d+(?:\.\d+)*)", ua, re.IGNORECASE)
        if and_m:
            platform_version = and_m.group(1)
        else:
            platform_version = "14.0.0"
    elif re.search(r"iPhone|iPad|iPod", ua, re.IGNORECASE):
        platform = "iOS"
        platform_version = "17.0"
    elif re.search(r"Linux", ua, re.IGNORECASE):
        platform = "Linux"
        platform_version = "6.5.0"

    # Engine & Brand
    engine = "Chromium"
    brand = "Google Chrome"
    major_version = "133"
    full_version = "133.0.0.0"

    edge_m = re.search(r"Edg/(\d+)(\.[\d.]+)", ua, re.IGNORECASE)
    opera_m = re.search(r"(?:OPR|Opera)/(\d+)(\.[\d.]+)", ua, re.IGNORECASE)
    chrome_m = re.search(r"Chrome/(\d+)(\.[\d.]+)", ua, re.IGNORECASE)
    firefox_m = re.search(r"Firefox/(\d+)(\.[\d.]+)", ua, re.IGNORECASE)
    safari_m = not chrome_m and not firefox_m and re.search(r"Version/(\d+)(\.[\d.]+).*Safari", ua, re.IGNORECASE)

    if edge_m:
        engine = "Chromium"
        brand = "Microsoft Edge"
        major_version = edge_m.group(1)
        full_version = f"{edge_m.group(1)}{edge_m.group(2)}"
    elif opera_m:
        engine = "Chromium"
        brand = "Opera"
        major_version = opera_m.group(1)
        full_version = f"{opera_m.group(1)}{opera_m.group(2)}"
    elif chrome_m:
        engine = "Chromium"
        brand = "Google Chrome"
        major_version = chrome_m.group(1)
        full_version = f"{chrome_m.group(1)}{chrome_m.group(2)}"
    elif firefox_m:
        engine = "Gecko"
        brand = "Firefox"
        major_version = firefox_m.group(1)
        full_version = f"{firefox_m.group(1)}{firefox_m.group(2)}"
    elif safari_m:
        engine = "WebKit"
        brand = "Safari"
        major_version = safari_m.group(1)
        full_version = f"{safari_m.group(1)}{safari_m.group(2)}"

    return {
        "user_agent": ua,
        "engine": engine,
        "brand": brand,
        "major_version": major_version,
        "full_version": full_version,
        "platform": platform,
        "platform_version": platform_version,
        "is_mobile": is_mobile,
    }


def get_device_headers(
    ua_string: str | None,
    dest_type: str = "document",
    referer: str | None = None,
) -> dict[str, str]:
    """
    Generates exact matching HTTP headers based on the parsed device profile.
    - Chromium: Injects Sec-CH-UA, Sec-CH-UA-Platform, Sec-CH-UA-Mobile, Sec-Fetch-*
    - Firefox / Safari: Omits Sec-CH-UA to avoid bot signature.
    """
    profile = parse_user_agent(ua_string)
    headers: dict[str, str] = {
        "User-Agent": profile["user_agent"],
        "Accept-Language": "en-US,en;q=0.9",
    }

    if referer:
        headers["Referer"] = referer

    if dest_type == "image":
        headers["Accept"] = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
        headers["Sec-Fetch-Dest"] = "image"
        headers["Sec-Fetch-Mode"] = "no-cors"
        headers["Sec-Fetch-Site"] = "cross-site"
    elif dest_type == "video":
        headers["Accept"] = "*/*"
        headers["Sec-Fetch-Dest"] = "video"
        headers["Sec-Fetch-Mode"] = "no-cors"
        headers["Sec-Fetch-Site"] = "cross-site"
    elif dest_type == "json":
        headers["Accept"] = "application/json, text/plain, */*"
        headers["Sec-Fetch-Dest"] = "empty"
        headers["Sec-Fetch-Mode"] = "cors"
        headers["Sec-Fetch-Site"] = "same-origin"
    else:
        # Default document navigation
        headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
        headers["Upgrade-Insecure-Requests"] = "1"
        headers["Sec-Fetch-Dest"] = "document"
        headers["Sec-Fetch-Mode"] = "navigate"
        headers["Sec-Fetch-Site"] = "same-origin"
        headers["Sec-Fetch-User"] = "?1"

    # Injects Client Hints for Chromium
    if profile["engine"] == "Chromium":
        major = profile["major_version"]
        if profile["brand"] == "Microsoft Edge":
            sec_ch_ua = f'"Not(A:Brand";v="99", "Microsoft Edge";v="{major}", "Chromium";v="{major}"'
        elif profile["brand"] == "Opera":
            sec_ch_ua = f'"Not(A:Brand";v="99", "Opera";v="{major}", "Chromium";v="{major}"'
        else:
            sec_ch_ua = f'"Not(A:Brand";v="99", "Google Chrome";v="{major}", "Chromium";v="{major}"'

        headers["sec-ch-ua"] = sec_ch_ua
        headers["sec-ch-ua-mobile"] = "?1" if profile["is_mobile"] else "?0"
        headers["sec-ch-ua-platform"] = f'"{profile["platform"]}"'

    return headers


def resolve_user_device_agent(
    user_id: int | str | None = None,
    cookies_text: str | None = None,
    custom_ua: str | None = None,
) -> str:
    """
    Resolves the effective device User-Agent for a user session.
    """
    # 1. Explicit override
    if custom_ua and len(custom_ua.strip()) > 10:
        return custom_ua.strip()

    # 2. Extracted from cookies_text
    if cookies_text:
        extracted = extract_user_agent_from_cookies(cookies_text)
        if extracted:
            return extracted

    # 3. User cookies file on disk
    if user_id:
        user_cookies_path = settings.auth_dir / str(user_id) / "cookies.txt"
        if user_cookies_path.exists() and user_cookies_path.is_file():
            try:
                content = user_cookies_path.read_text(encoding="utf-8", errors="ignore")
                extracted = extract_user_agent_from_cookies(content)
                if extracted:
                    return extracted
            except Exception:
                pass

    # 4. auth/{user_id}/user-agent.txt file
    ua_path = get_user_agent_path(user_id)
    if ua_path and ua_path.exists() and ua_path.is_file():
        try:
            content = ua_path.read_text(encoding="utf-8", errors="ignore").strip()
            if len(content) > 10:
                return content
        except Exception:
            pass

    # 5. Global auth/cookies.txt
    auth_global_cookies = settings.auth_dir / "cookies.txt"
    if auth_global_cookies.exists() and auth_global_cookies.is_file():
        try:
            content = auth_global_cookies.read_text(encoding="utf-8", errors="ignore")
            extracted = extract_user_agent_from_cookies(content)
            if extracted:
                return extracted
        except Exception:
            pass

    return DEFAULT_FALLBACK_UA
