from app.telegraph import telegraph_helper

from .core import (
    DownloadResult,
    download_torrent_async,
    download_via_aria2_async,
    start_aria2_daemon,
    stop_aria2_daemon,
)
from .magnetio_client import (
    MagnetioRPCError,
    search_torrents_rpc,
)
from .magnetio_daemon import (
    start_magnetio_daemon,
    stop_magnetio_daemon,
)
from .search import (
    SITES,
    format_search_results_html,
    initiate_search_tools,
    search_torrents,
)
from .trackers import (
    add_trackers_to_magnet,
    fetch_latest_trackers,
    get_tracker_string,
    get_trackers,
)

__all__ = [
    "SITES",
    "DownloadResult",
    "MagnetioRPCError",
    "add_trackers_to_magnet",
    "download_torrent_async",
    "download_via_aria2_async",
    "fetch_latest_trackers",
    "format_search_results_html",
    "get_tracker_string",
    "get_trackers",
    "initiate_search_tools",
    "search_torrents",
    "search_torrents_rpc",
    "start_aria2_daemon",
    "start_magnetio_daemon",
    "stop_aria2_daemon",
    "stop_magnetio_daemon",
    "telegraph_helper",
]
