from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Callable, Coroutine
from pathlib import Path
from typing import Any

import webhost
from webhost.exceptions import WebHostError

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
            log.debug("Progress callback error in FileDitch sync_cb: %s", e)

    return sync_cb


async def upload_to_fileditch(
    file_path: Path | str,
    is_temp: bool = False,
    progress_callback: Callable[[int, int], Coroutine[None, None, None]] | None = None,
) -> tuple[dict[str, Any], list[str]]:
    """
    Upload a single file to FileDitch using webhost package.

    Args:
        file_path: Path to the local file to upload
        is_temp: If True, uploads to temp.fileditch.com (72h retention). Otherwise, new.fileditch.com.
        progress_callback: Optional async function called with (current_bytes, total_bytes)

    Returns:
        A tuple of (response_json_dict, log_messages_list)
    """
    logs: list[str] = []
    path = Path(file_path)

    if not path.exists():
        logs.append(f"File not found: {path}")
        return {"error": "File not found"}, logs

    file_size = path.stat().st_size
    if file_size == 0:
        logs.append(f"Cannot upload 0-byte file: {path}")
        return {"error": "File is empty (0 bytes)"}, logs

    logs.append(f"Uploading file to FileDitch ({'temp' if is_temp else 'permanent'}): {path.name} ({file_size} bytes)")

    loop = asyncio.get_running_loop()
    sync_cb = _make_sync_progress_callback(loop, progress_callback)

    def _do_upload() -> dict[str, Any]:
        return webhost.fileditch.upload_file(
            file_path=str(path),
            filename=path.name,
            progress_callback=sync_cb
        )

    try:
        res = await asyncio.to_thread(_do_upload)
        if res.get("success") is True:
            logs.append(f"Uploaded to FileDitch successfully: {res.get('url')}")
        else:
            logs.append(f"FileDitch API returned error status: {res}")
        return res, logs
    except WebHostError as e:
        logs.append(f"FileDitch upload failed: {e}")
        return {"error": str(e)}, logs
    except Exception as e:
        log.exception("Unexpected error uploading to FileDitch")
        logs.append(f"Unexpected error: {e}")
        return {"error": str(e)}, logs


class FileDitchUploader:
    """Stateful FileDitch uploader supporting directory and single file uploads."""

    def __init__(
        self,
        path: Path,
        is_temp: bool = False,
        progress_callback: Callable[[int, int], Coroutine[None, None, None]] | None = None,
    ) -> None:
        self.path = path
        self.is_temp = is_temp
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
        """Uploads files in self.path to FileDitch and returns (file_urls, summary_dict)."""
        files: list[Path] = []
        if self.path.is_file():
            files.append(self.path)
        else:
            for p in sorted(self.path.rglob("*"), key=natural_path_sort_key):
                if p.is_file() and not p.name.startswith("."):
                    files.append(p)

        if not files:
            return [], {"error": "No files found to upload"}

        file_urls: list[str] = []
        uploaded_count = 0
        failed_count = 0

        for f in files:
            if self.is_cancelled:
                break
            res, _ = await upload_to_fileditch(f, self.is_temp, self.progress_callback)
            if res.get("success") is True:
                url = res.get("url")
                if url:
                    file_urls.append(url)
                uploaded_count += 1
            else:
                failed_count += 1

        summary = {
            "uploaded": uploaded_count,
            "failed": failed_count,
            "total": len(files),
            "urls": file_urls,
        }
        return file_urls, summary

    def cancel_task(self) -> None:
        self.is_cancelled = True
