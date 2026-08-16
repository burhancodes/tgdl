from .aria2c import (
    download_torrent_async,
    download_via_aria2_async,
    start_aria2_daemon,
    stop_aria2_daemon,
)
from .direct import (
    DirectDownloader,
    DirectDownloadError,
    download_direct,
    download_hls,
    is_direct_url,
    is_m3u8_url,
)
from .cyberdrop_dl import (
    CyberdropDLNotFound,
    get_cdl_config_path,
    get_user_cdl_config_path,
    run_with_progress as run_cyberdrop_dl,
)
from .gallery_dl import (
    DownloadResult,
    GalleryDLNotFound,
    get_cookies_path,
    get_gdl_config_path,
    get_user_cookies_path,
    get_user_gdl_config_path,
    run_with_progress,
)
from .telegram import TelegramDownloader, TelegramDownloadError, download_telegram_media

__all__ = [
    "CyberdropDLNotFound",
    "DirectDownloadError",
    "DirectDownloader",
    "DownloadResult",
    "GalleryDLNotFound",
    "TelegramDownloadError",
    "TelegramDownloader",
    "download_direct",
    "download_hls",
    "download_telegram_media",
    "download_torrent_async",
    "download_via_aria2_async",
    "get_cdl_config_path",
    "get_cookies_path",
    "get_gdl_config_path",
    "get_user_cdl_config_path",
    "get_user_cookies_path",
    "get_user_gdl_config_path",
    "is_direct_url",
    "is_m3u8_url",
    "run_cyberdrop_dl",
    "run_with_progress",
    "start_aria2_daemon",
    "stop_aria2_daemon",
]
