from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Callable
from pathlib import Path

from googleapiclient.http import MediaIoBaseDownload

from ..utils.sorting import natural_path_sort_key
from .client import (
    EXPORT_MAP,
    G_DRIVE_DIR_MIME_TYPE,
    GoogleDriveClient,
    get_id_from_url,
    sanitize_filename,
)

log = logging.getLogger(__name__)


class GoogleDriveDownloader:
    def __init__(
        self,
        client: GoogleDriveClient | None = None,
        user_id: int | str | None = None,
        progress_callback: Callable[[int, float, str], None] | None = None,
    ):
        self.client = client or GoogleDriveClient(user_id=user_id)
        self.progress_callback = progress_callback
        self.total_bytes = 0
        self.downloaded_bytes = 0
        self.start_time = time.time()

    async def download_link(self, link_or_id: str, dest_dir: Path) -> Path | list[Path]:
        dest_dir.mkdir(parents=True, exist_ok=True)
        urls: list[str] = []

        if not link_or_id:
            return []

        if link_or_id.startswith("[") and link_or_id.endswith("]"):
            try:
                import json
                parsed = json.loads(link_or_id)
                if isinstance(parsed, list):
                    for item in parsed:
                        u = str(item).strip()
                        if u:
                            urls.append(u)
            except Exception as e:
                log.debug("Failed parsing JSON array in GoogleDriveDownloader: %s", e)

        if not urls:
            lines = [u.strip() for u in link_or_id.split() if u.strip()]
            for u in lines:
                urls.append(u)

        if not urls:
            urls = [link_or_id.strip()]

        if len(urls) > 1:
            urls.sort(key=natural_path_sort_key)

        res_paths: list[Path] = []
        failed_count = 0
        last_err = None

        for raw_url in urls:
            clean_url = raw_url
            for prefix in ("gdrive:", "gd2tg:"):
                clean_url = clean_url.removeprefix(prefix)

            try:
                file_id = get_id_from_url(clean_url)
                meta = await asyncio.to_thread(self.client.get_file_metadata, file_id)

                raw_name = meta.get("name", file_id)
                name = sanitize_filename(raw_name)
                mime_type = meta.get("mimeType", "")

                self.start_time = time.time()

                if mime_type == G_DRIVE_DIR_MIME_TYPE:
                    folder_path = dest_dir / name
                    await self._download_folder(file_id, folder_path)
                    res_paths.append(folder_path)
                else:
                    file_path = await self._download_file(meta, dest_dir)
                    res_paths.append(file_path)
            except Exception as e:
                failed_count += 1
                last_err = e
                log.error("Failed downloading Google Drive URL/ID '%s': %s", clean_url, e)

        if failed_count == len(urls) and len(urls) > 0:
            if last_err:
                raise last_err
            raise RuntimeError(f"All {len(urls)} Google Drive downloads failed.")

        downloaded_files = sorted(
            (p for p in dest_dir.rglob("*") if p.is_file()),
            key=natural_path_sort_key,
        )
        if len(res_paths) == 1:
            return res_paths[0]
        return downloaded_files if downloaded_files else res_paths

    async def _collect_folder_items(
        self, folder_id: str, rel_path: Path
    ) -> tuple[list[tuple[dict, Path]], list[Path]]:
        """Recursively collect all files with relative paths, and all directory relative paths."""
        files: list[tuple[dict, Path]] = []
        dirs: list[Path] = [rel_path] if str(rel_path) != "." else []
        items = await asyncio.to_thread(self.client.list_folder_contents, folder_id)
        for item in items:
            mime = item.get("mimeType", "")
            safe_name = sanitize_filename(item.get("name", item["id"]))
            item_rel_path = rel_path / safe_name
            if mime == G_DRIVE_DIR_MIME_TYPE:
                sub_files, sub_dirs = await self._collect_folder_items(item["id"], item_rel_path)
                files.extend(sub_files)
                dirs.extend(sub_dirs)
            else:
                item_copy = dict(item)
                item_copy["name"] = safe_name
                files.append((item_copy, item_rel_path))
        return files, dirs

    async def _download_folder(self, folder_id: str, folder_path: Path) -> None:
        folder_path.mkdir(parents=True, exist_ok=True)
        files, dirs = await self._collect_folder_items(folder_id, Path())

        for d in dirs:
            (folder_path / d).mkdir(parents=True, exist_ok=True)

        files.sort(key=lambda x: natural_path_sort_key(x[1]))

        for item_meta, rel_path in files:
            target_parent = folder_path / rel_path.parent
            target_parent.mkdir(parents=True, exist_ok=True)
            await self._download_file(item_meta, target_parent)

    async def _download_file(self, meta: dict, parent_dir: Path) -> Path:
        file_id = meta["id"]
        name = sanitize_filename(meta["name"])
        mime_type = meta.get("mimeType", "")

        if mime_type in EXPORT_MAP:
            export_info = EXPORT_MAP[mime_type]
            if not name.endswith(export_info["ext"]):
                name += export_info["ext"]
            request = self.client.service.files().export_media(
                fileId=file_id, mimeType=export_info["mime"]
            )
        else:
            request = self.client.service.files().get_media(
                fileId=file_id, supportsAllDrives=True
            )

        file_path = parent_dir / name
        log.info("Downloading GDrive file '%s' to %s", name, file_path)

        initial_downloaded = self.downloaded_bytes

        def _do_download():
            from ..config import settings
            from ..pacing import DownloadThrottler
            throttler = DownloadThrottler(settings.global_download_speed_limit)

            with open(file_path, "wb") as fh:
                downloader = MediaIoBaseDownload(fh, request, chunksize=8 * 1024 * 1024)
                done = False
                prev_bytes = 0
                while not done:
                    status, done = downloader.next_chunk()
                    if status:
                        current_file_bytes = status.resumable_progress
                        chunk_size = max(0, current_file_bytes - prev_bytes)
                        prev_bytes = current_file_bytes
                        throttler.consume_sync(chunk_size)

                        self.downloaded_bytes = initial_downloaded + current_file_bytes
                        elapsed = max(time.time() - self.start_time, 0.1)
                        speed = self.downloaded_bytes / elapsed
                        if self.progress_callback:
                            self.progress_callback(self.downloaded_bytes, speed, name)

        await asyncio.to_thread(_do_download)
        return file_path
