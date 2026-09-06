from .core import (
    DownloadResult,
    XenForoDownloader,
    XenForoDownloadError,
    download_xenforo_post,
    get_cookies_path,
    get_user_cookies_path,
    is_simpcity_url,
    is_xenforo_url,
    load_user_cookies_text,
    run_with_progress,
)
from .device_profile import (
    DEFAULT_FALLBACK_UA,
    extract_user_agent_from_cookies,
    get_device_headers,
    get_user_agent_path,
    parse_user_agent,
    resolve_user_device_agent,
)

__all__ = [
    "DEFAULT_FALLBACK_UA",
    "DownloadResult",
    "XenForoDownloadError",
    "XenForoDownloader",
    "download_xenforo_post",
    "extract_user_agent_from_cookies",
    "get_cookies_path",
    "get_device_headers",
    "get_user_agent_path",
    "get_user_cookies_path",
    "is_simpcity_url",
    "is_xenforo_url",
    "load_user_cookies_text",
    "parse_user_agent",
    "resolve_user_device_agent",
    "run_with_progress",
]
