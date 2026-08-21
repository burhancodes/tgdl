from __future__ import annotations

import contextlib
import json
import logging
import time
from collections.abc import Callable
from pathlib import Path
from mega import progress
from ..utils.sorting import natural_path_sort_key
from .client import MegaClient, is_mega_url

log = logging.getLogger(__name__)


class MegaDownloader:
    """Downloader class for handling MEGA downloads with progress tracking."""

    def __init__(
        self,
        client: MegaClient | None = None,
        user_id: int | str | None = None,
        progress_callback: Callable[[int, float, str], None] | None = None,
    ) -> None:
        self.client = client or MegaClient()
        self.user_id = user_id
        self.progress_callback = progress_callback
        self.total_downloaded_bytes = 0
        self.start_time = time.time()
        self.current_filename = ""
        self._own_client = client is None

    def _create_hook_factory(self):
        from ..config import settings
        from ..pacing import DownloadThrottler
        throttler = DownloadThrottler(settings.global_download_speed_limit)

        @contextlib.contextmanager
        def factory(description: str, total: float, kind: str):
            self.current_filename = description

            def progress_hook(advance: float) -> None:
                advance_bytes = int(advance)
                throttler.consume_sync(advance_bytes)
                self.total_downloaded_bytes += advance_bytes
                elapsed = max(time.time() - self.start_time, 0.1)
                speed = self.total_downloaded_bytes / elapsed
                if self.progress_callback:
                    try:
                        self.progress_callback(
                            self.total_downloaded_bytes, speed, self.current_filename
                        )
                    except Exception as e:
                        log.debug("Mega progress_callback error: %s", e)

            try:
                yield progress_hook
            finally:
                pass

        return factory

    async def download_link(self, link_or_json: str, dest_dir: Path) -> list[Path]:
        """Download MEGA link(s) to destination directory."""
        dest_dir.mkdir(parents=True, exist_ok=True)
        urls: list[str] = []

        if not link_or_json:
            return []

        # Parse potential JSON array or single link
        if link_or_json.startswith("[") and link_or_json.endswith("]"):
            try:
                parsed = json.loads(link_or_json)
                if isinstance(parsed, list):
                    for item in parsed:
                        u = str(item).strip()
                        if u:
                            urls.append(u)
            except Exception as e:
                log.debug("Failed parsing JSON array in MegaDownloader: %s", e)

        if not urls:
            lines = [u.strip() for u in link_or_json.split() if u.strip()]
            for u in lines:
                if u.startswith("mega:") or is_mega_url(u) or u.startswith(("http://", "https://")):
                    urls.append(u)

        if not urls:
            urls = [link_or_json.strip()]

        self.start_time = time.time()
        self.total_downloaded_bytes = 0

        factory = self._create_hook_factory()
        token = progress._PROGRESS_HOOK_FACTORY.set(factory)

        try:
            await self.client.ensure_logged_in(user_id=self.user_id)
            failed_count = 0
            last_err = None
            for raw_url in urls:
                clean_url = raw_url
                clean_url = clean_url.removeprefix("mega:")

                log.info("Downloading MEGA url '%s' to %s", clean_url, dest_dir)
                try:
                    await self.client.download_url(clean_url, output_dir=dest_dir)
                except Exception as e:
                    failed_count += 1
                    last_err = e
                    log.error("Failed downloading MEGA URL '%s': %s", clean_url, e)

            if failed_count == len(urls) and len(urls) > 0:
                if last_err:
                    raise last_err
                raise RuntimeError(f"All {len(urls)} MEGA downloads failed.")
        finally:
            progress._PROGRESS_HOOK_FACTORY.reset(token)
            if self._own_client:
                await self.client.close()

        downloaded_files = sorted(
            (p for p in dest_dir.rglob("*") if p.is_file()),
            key=natural_path_sort_key,
        )
        return downloaded_files
