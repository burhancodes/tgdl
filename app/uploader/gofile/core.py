from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Callable, Coroutine
from pathlib import Path
from typing import Any

import webhost
from webhost.exceptions import WebHostError

from ...config import settings
from ...utils.sorting import natural_path_sort_key

log = logging.getLogger(__name__)


def _make_sync_progress_callback(
    loop: asyncio.AbstractEventLoop,
    async_cb: Callable[[int, int], Coroutine[None, None, None]] | Callable[[int, int], Any] | None,
) -> Callable[[int, int], None] | None:
    if not async_cb:
        return None

    def sync_cb(current: int, total: int) -> None:
        try:
            res = async_cb(current, total)
            if asyncio.iscoroutine(res):
                asyncio.run_coroutine_threadsafe(res, loop)
        except Exception as e:
            log.debug("Progress callback error in GoFile sync_cb: %s", e)

    return sync_cb


from ..user_keys import resolve_upload_api_key


async def upload_to_gofile(
    file_path: Path | str,
    api_token: str | None = None,
    progress_callback: Callable[[int, int], Coroutine[None, None, None]] | None = None,
    user_id: int | str | None = None,
) -> tuple[dict[str, Any], list[str]]:
    """
    Upload a single file to GoFile using webhost package.

    Args:
        file_path: Path to the local file to upload
        api_token: Optional GoFile API Token for authenticated uploads
        progress_callback: Optional async function called with (current_bytes, total_bytes)
        user_id: Optional user ID to look up user-specific API keys

    Returns:
        A tuple of (response_json_dict, log_messages_list)
    """
    logs: list[str] = []
    path = Path(file_path)

    if not path.exists():
        logs.append(f"File not found: {path}")
        return {"error": "File not found"}, logs

    token = (api_token or resolve_upload_api_key(user_id, "gofile") or "").strip() or None
    logs.append(f"Uploading file to GoFile: {path.name}")

    loop = asyncio.get_running_loop()
    sync_cb = _make_sync_progress_callback(loop, progress_callback)

    def _do_upload() -> dict[str, Any]:
        return webhost.gofile.upload_file(
            file_path=str(path),
            token=token,
            progress_callback=sync_cb
        )

    try:
        res = await asyncio.to_thread(_do_upload)
        if res.get("status") == "ok":
            logs.append("Uploaded to GoFile successfully")
        else:
            logs.append(f"GoFile API returned error status: {res}")
        return res, logs
    except WebHostError as e:
        logs.append(f"GoFile upload failed: {e}")
        return {"error": str(e)}, logs
    except Exception as e:
        log.exception("Unexpected error uploading to GoFile")
        logs.append(f"Unexpected error: {e}")
        return {"error": str(e)}, logs


class GoFileUploader:
    """Stateful GoFile uploader supporting directory and single file uploads."""

    def __init__(
        self,
        path: Path,
        api_token: str | None = None,
        progress_callback: Callable[[int, int], Coroutine[None, None, None]] | None = None,
    ) -> None:
        self.path = path
        self.api_token = api_token or getattr(settings, "gofile_api_key", None)
        self.progress_callback = progress_callback
        self.processed_bytes = 0
        self.start_time = time.time()
        self.is_cancelled = False

    @property
    def speed(self) -> float:
        elapsed = time.time() - self.start_time
        if elapsed > 0:
            return self.processed_bytes / elapsed
        return 0.0

    async def upload(self) -> tuple[list[str], dict[str, Any]]:
        """Uploads files in self.path to GoFile and returns (page_links, summary_dict)."""
        files: list[Path] = []
        if self.path.is_file():
            files.append(self.path)
        else:
            for p in sorted(self.path.rglob("*"), key=natural_path_sort_key):
                if p.is_file() and not p.name.startswith("."):
                    files.append(p)

        if not files:
            return [], {"error": "No files found to upload"}

        page_links: list[str] = []
        uploaded_count = 0
        failed_count = 0

        for f in files:
            if self.is_cancelled:
                break
            res, _ = await upload_to_gofile(f, self.api_token, self.progress_callback)
            if res.get("status") == "ok":
                data = res.get("data", {})
                link = data.get("downloadPage")
                if link:
                    page_links.append(link)
                uploaded_count += 1
            else:
                failed_count += 1

        summary = {
            "uploaded": uploaded_count,
            "failed": failed_count,
            "total": len(files),
            "links": page_links,
        }
        return page_links, summary

    def cancel_task(self) -> None:
        self.is_cancelled = True
