from __future__ import annotations

import asyncio
import json
import logging
import random
import shutil
import time
from pathlib import Path

from pyrogram import Client
from pyrogram.types import (
    ForceReply,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
)

from ..config import settings
from ..db import JobStatus, JobStore
from ..utils.sorting import natural_path_sort_key
from .state import JobState
from .status import (
    compile_archive_prompt_text,
    compile_audio_conversion_failed_status_text,
    compile_audio_conversion_prompt_text,
    compile_audio_conversion_running_status_text,
    compile_conversion_running_status_text,
    compile_extraction_failed_status_text,
    compile_extraction_status_text,
    compile_extraction_success_status_text,
    compile_job_status_text,
    safe_delete,
    safe_edit,
    safe_pin,
)

log = logging.getLogger(__name__)

_password_prompt_events: dict[str, dict[str, tuple[asyncio.Event, dict]]] = {}
_password_prompt_messages: dict[int, tuple[str, str, int]] = {}

store = JobStore(settings.db_path)


class QueueManager:
    def __init__(self):
        self.client: Client | None = None
        self.store: JobStore | None = None
        self.download_queue: asyncio.Queue[str] = asyncio.Queue()
        self.upload_queue: asyncio.Queue[str] = asyncio.Queue()
        self.jobs: dict[str, JobState] = {}
        self.download_workers: list[asyncio.Task] = []
        self.upload_workers: list[asyncio.Task] = []
        self.sweep_task: asyncio.Task | None = None
        self.is_running = False
        self.upload_delay_multiplier = 1.0

    def notify_floodwait(self, seconds: int) -> None:
        self.upload_delay_multiplier = min(self.upload_delay_multiplier + 1.0, 5.0)
        log.warning("Uploader hit FloodWait. Increased upload delay multiplier to %.1f", self.upload_delay_multiplier)

    async def start(self, client: Client, store: JobStore) -> None:
        self.client = client
        self.store = store
        self.is_running = True
        num_dl = settings.tg_max_concurrent_downloads
        num_ul = settings.tg_max_concurrent_uploads

        # Start the global aria2c daemon
        try:
            from ..downloader import start_aria2_daemon
            await start_aria2_daemon()
        except Exception as e:
            log.error("Failed to start global aria2c daemon at QueueManager startup: %s", e)

        for i in range(num_dl):
            self.download_workers.append(asyncio.create_task(self._download_worker_loop(i)))
        for i in range(num_ul):
            self.upload_workers.append(asyncio.create_task(self._upload_worker_loop(i)))

        from ..pacing import telegram_limiter
        self.sweep_task = asyncio.create_task(telegram_limiter.start_periodic_sweep())

        log.info("Queue manager started with %s download and %s upload workers", num_dl, num_ul)

    async def stop(self) -> None:
        self.is_running = False
        if self.sweep_task:
            self.sweep_task.cancel()
            self.sweep_task = None
        for w in self.download_workers:
            w.cancel()
        for w in self.upload_workers:
            w.cancel()
        for job_id in list(self.jobs.keys()):
            await self.cancel_job(job_id)
        self.download_workers.clear()
        self.upload_workers.clear()

        # Stop the global aria2c daemon
        try:
            from ..downloader import stop_aria2_daemon
            await stop_aria2_daemon()
        except Exception as e:
            log.error("Failed to stop global aria2c daemon at QueueManager shutdown: %s", e)

        log.info("Queue manager stopped")

    async def add_job(self, job_id: str) -> None:
        await self.download_queue.put(job_id)
        log.info("Job #%s added to download queue", job_id)

    async def cancel_job(self, job_id: str) -> bool:
        job_state = self.jobs.get(job_id)

        if self.store:
            await self.store.update_progress(job_id, status=JobStatus.CANCELLED)

        if not job_state:
            return False

        log.info("Cancelling job #%s", job_id)

        if job_state.active_process:
            try:
                job_state.active_process.kill()
            except Exception:
                # expected: active process may have already exited
                pass

        if job_state.active_download_task:
            try:
                job_state.active_download_task.cancel()
            except Exception:
                # expected: download task may have already completed or cancelled
                pass

        if job_state.active_upload_task:
            try:
                job_state.active_upload_task.cancel()
            except Exception:
                # expected: upload task may have already completed or cancelled
                pass

        job_state.downloader_done.set()
        job_state.uploader_done.set()
        job_state.trigger_event.set()
        shutil.rmtree(job_state.dest_dir, ignore_errors=True)
        shutil.rmtree(job_state.dest_dir.parent / f"{job_state.dest_dir.name}_extracted", ignore_errors=True)
        shutil.rmtree(job_state.dest_dir.parent / f"{job_state.dest_dir.name}_patch_work", ignore_errors=True)

        from ..handlers.conversion_state import conversion_session_store
        from ..utils.archive import archive_session_store
        from .status.messaging import _last_edit_times

        archive_session_store.pop_job(job_id)
        conversion_session_store.pop_job(job_id)
        _password_prompt_events.pop(job_id, None)
        to_remove = [mid for mid, info in _password_prompt_messages.items() if info[0] == job_id]
        for mid in to_remove:
            _password_prompt_messages.pop(mid, None)
        if job_state.msg_id:
            _last_edit_times.pop((job_state.job.chat_id, job_state.msg_id), None)
        self.jobs.pop(job_id, None)
        return True

    def get_active_jobs_for_chat(self, chat_id: int) -> list[JobState]:
        return [js for js in self.jobs.values() if js.job.chat_id == chat_id]

    async def register_process(self, job_id: str, proc: asyncio.subprocess.Process) -> None:
        job_state = self.jobs.get(job_id)
        if job_state:
            job_state.active_process = proc

    async def unregister_process(self, job_id: str) -> None:
        job_state = self.jobs.get(job_id)
        if job_state:
            job_state.active_process = None

    async def _download_worker_loop(self, worker_id: int) -> None:
        while self.is_running:
            try:
                job_id = await self.download_queue.get()
            except asyncio.CancelledError:
                break

            try:
                job = await self.store.get_job(job_id)
                if not job or job.status == JobStatus.CANCELLED:
                    self.download_queue.task_done()
                    continue

                dest_dir = (settings.downloads_dir / job.download_dir).resolve()

                # Disk limit safeguards
                if settings.max_total_downloads_bytes is not None:
                    try:
                        total_used = sum(p.stat().st_size for p in settings.downloads_dir.rglob("*") if p.is_file())
                        if total_used >= settings.max_total_downloads_bytes:
                            log.warning("Total downloads disk usage (%.2f GB) exceeds limit (%.2f GB). Failing job #%s.",
                                        total_used / (1024**3), settings.max_total_downloads_bytes / (1024**3), job_id)
                            await self.store.update_progress(job_id, status=JobStatus.FAILED, error="Total downloads disk usage limit exceeded")
                            continue
                    except Exception as de:
                        log.warning("Error checking downloads dir size: %s", de)

                try:
                    usage = shutil.disk_usage(settings.downloads_dir)
                    if usage.free < 500 * 1024 * 1024:
                        log.warning("Host disk free space critically low (%.2f MB). Failing job #%s.", usage.free / (1024*1024), job_id)
                        await self.store.update_progress(job_id, status=JobStatus.FAILED, error="Host disk space critically low")
                        continue
                except Exception as e:
                    log.debug("Failed checking free disk space safeguard: %s", e)

                job_state = JobState(job, dest_dir)
                if job.status_message_id:
                    job_state.msg_id = job.status_message_id
                self.jobs[job_id] = job_state
                await self.upload_queue.put(job_id)
                await self._process_download(job_state)
            except asyncio.CancelledError:
                if not self.is_running:
                    break
                log.info("Job execution cancelled in download worker %s", worker_id)
            except Exception:
                log.exception("Error in download worker %s", worker_id)
            finally:
                self.download_queue.task_done()

    async def _upload_worker_loop(self, worker_id: int) -> None:
        while self.is_running:
            try:
                job_id = await self.upload_queue.get()
            except asyncio.CancelledError:
                break

            try:
                job_state = self.jobs.get(job_id)
                if not job_state:
                    self.upload_queue.task_done()
                    continue

                db_job = await self.store.get_job(job_id)
                if not db_job or db_job.status == JobStatus.CANCELLED:
                    self.upload_queue.task_done()
                    self.jobs.pop(job_id, None)
                    continue

                await self._process_upload(job_state)
            except asyncio.CancelledError:
                if not self.is_running:
                    break
                log.info("Job execution cancelled in upload worker %s", worker_id)
            except Exception:
                log.exception("Error in upload worker %s", worker_id)
            finally:
                self.upload_queue.task_done()

    async def _process_download(self, job_state: JobState) -> None:
        job_state.active_download_task = asyncio.current_task()
        job = job_state.job
        chat_id = job.chat_id
        dest_dir = job_state.dest_dir

        args_dict: dict = {}
        if job.args:
            try:
                args_dict = json.loads(job.args)
                if not isinstance(args_dict, dict):
                    args_dict = {}
            except Exception as e:
                log.debug("Failed parsing job.args JSON for job #%s: %s", job.id, e)

        async def report(text: str) -> None:
            await safe_send(self.client, chat_id, text, link_preview_options=LinkPreviewOptions(is_disabled=True))

        monitor_task = None
        try:
            cleaned_url = job.url
            if job.url.startswith("[") and job.url.endswith("]"):
                try:
                    parsed = json.loads(job.url)
                    if parsed and isinstance(parsed, list):
                        cleaned_url = parsed[0]
                except Exception as e:
                    log.debug("Failed parsing JSON array URL for job #%s: %s", job.id, e)

            from ..downloader import is_direct_url, is_m3u8_url

            is_torrent = (
                cleaned_url.startswith("magnet:") or
                cleaned_url.startswith("torrent:") or
                cleaned_url.endswith(".torrent") or
                "magnet:?xt=" in cleaned_url
            )
            is_unzip = cleaned_url.startswith("unzip:")
            is_gdrive = (
                cleaned_url.startswith("gdrive:") or
                cleaned_url.startswith("gd2tg:") or
                "drive.google.com" in cleaned_url or
                "docs.google.com" in cleaned_url
            )
            is_mega = (
                cleaned_url.startswith("mega:") or
                "mega.nz" in cleaned_url or
                "mega.co.nz" in cleaned_url or
                "mega.io" in cleaned_url
            )
            is_patch = cleaned_url.startswith("patch:")


            await self.store.update_progress(job.id, status=JobStatus.DOWNLOADING)

            queued_msg_id = job_state.msg_id or job.status_message_id
            if queued_msg_id:
                await safe_delete(self.client, chat_id, queued_msg_id)
                job_state.msg_id = None

            db_job = await self.store.get_job(job.id) or job
            status_text = compile_job_status_text(db_job, job_state)
            from pyrogram.types import InlineKeyboardButton, InlineKeyboardMarkup
            keyboard = InlineKeyboardMarkup([
                [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
            ])
            status_msg = await safe_send(
                self.client,
                chat_id,
                status_text,
                reply_markup=keyboard,
                link_preview_options=LinkPreviewOptions(is_disabled=True)
            )
            if status_msg:
                job_state.msg_id = status_msg.id
                job_state.last_edited_text = status_text
                await self.store.set_status_message(job.id, status_msg.id)
                await safe_pin(self.client, chat_id, status_msg.id)
                job_state.is_pinned = True

            job_state.trigger_event.set()

            async def download_status_updater_loop() -> None:
                while not job_state.downloader_done.is_set():
                    try:
                        await asyncio.wait_for(job_state.trigger_event.wait(), timeout=5.0)
                        job_state.trigger_event.clear()
                        if job_state.downloader_done.is_set():
                            break

                        db_job = await self.store.get_job(job.id)
                        if not db_job or db_job.status == JobStatus.CANCELLED:
                            break

                        status_text = compile_job_status_text(db_job, job_state)
                        keyboard = InlineKeyboardMarkup([
                            [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                        ])
                        if not job_state.msg_id:
                            init_m = await safe_send(
                                self.client,
                                chat_id,
                                status_text,
                                reply_markup=keyboard,
                                link_preview_options=LinkPreviewOptions(is_disabled=True)
                            )
                            if init_m:
                                job_state.msg_id = init_m.id
                                job_state.last_edited_text = status_text
                                await self.store.set_status_message(job.id, init_m.id)
                                await safe_pin(self.client, chat_id, init_m.id)
                                job_state.is_pinned = True
                        elif status_text != job_state.last_edited_text:
                            if await safe_edit(self.client, chat_id, job_state.msg_id, status_text, reply_markup=keyboard):
                                job_state.last_edited_text = status_text
                                if not job_state.is_pinned:
                                    await safe_pin(self.client, chat_id, job_state.msg_id)
                                    job_state.is_pinned = True
                    except TimeoutError:
                        try:
                            db_job = await self.store.get_job(job.id)
                            if db_job and db_job.status != JobStatus.CANCELLED:
                                status_text = compile_job_status_text(db_job, job_state)
                                keyboard = InlineKeyboardMarkup([
                                    [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                                ])
                                if not job_state.msg_id:
                                    init_m = await safe_send(
                                        self.client,
                                        chat_id,
                                        status_text,
                                        reply_markup=keyboard,
                                        link_preview_options=LinkPreviewOptions(is_disabled=True)
                                    )
                                    if init_m:
                                        job_state.msg_id = init_m.id
                                        job_state.last_edited_text = status_text
                                        await self.store.set_status_message(job.id, init_m.id)
                                        if await safe_pin(self.client, chat_id, init_m.id):
                                            job_state.is_pinned = True
                                elif status_text != job_state.last_edited_text:
                                    if await safe_edit(self.client, chat_id, job_state.msg_id, status_text, reply_markup=keyboard):
                                        job_state.last_edited_text = status_text
                                        if not job_state.is_pinned:
                                            if await safe_pin(self.client, chat_id, job_state.msg_id):
                                                job_state.is_pinned = True

                        except Exception as e:
                            log.debug("Failed editing status message during timeout in download loop for job #%s: %s", job.id, e)
                    except Exception as e:
                        log.debug("Error in download status updater loop for job #%s: %s", job.id, e)

            download_updater_task = asyncio.create_task(download_status_updater_loop())

            def reg(proc):
                job_state.active_process = proc

            is_aria = (args_dict.get("engine") == "aria2")

            if not is_torrent and not is_unzip and not is_gdrive and not is_mega and not is_aria and not is_patch:
                async def monitor_download_speed():
                    last_download_size = 0
                    last_download_time = time.time()
                    stable_file_sizes: dict[Path, int] = {}
                    last_known_sizes: dict[Path, int] = {}
                    cached_file_list: list[Path] = []
                    scan_tick = 0

                    while not job_state.downloader_done.is_set():
                        await asyncio.sleep(1.0)
                        if not dest_dir.exists():
                            continue
                        try:
                            scan_tick += 1
                            if scan_tick % 3 == 1 or not cached_file_list:
                                cached_file_list = [p for p in dest_dir.rglob("*") if p.is_file()]

                            current_paths = set(cached_file_list)
                            # Remove stale paths
                            stale_stable = set(stable_file_sizes.keys()) - current_paths
                            for sp in stale_stable:
                                stable_file_sizes.pop(sp, None)
                            stale_last = set(last_known_sizes.keys()) - current_paths
                            for lp in stale_last:
                                last_known_sizes.pop(lp, None)

                            on_disk = 0
                            part_files: list[str] = []

                            for p in cached_file_list:
                                if p.name.endswith(".part"):
                                    part_files.append(p.name)

                                if p in stable_file_sizes:
                                    on_disk += stable_file_sizes[p]
                                else:
                                    try:
                                        sz = p.stat().st_size
                                        on_disk += sz
                                        if not p.name.endswith(".part") and p in last_known_sizes and last_known_sizes[p] == sz:
                                            stable_file_sizes[p] = sz
                                        else:
                                            last_known_sizes[p] = sz
                                    except Exception:
                                        # expected: file may have been moved or unlinked during scan
                                        pass

                            current_size = on_disk + job_state.deleted_bytes
                        except Exception as e:
                            log.debug("Notice on download speed monitor scan tick for job #%s: %s", job.id, e)
                            continue

                        now = time.time()
                        dt = now - last_download_time
                        if dt >= 1.0:
                            speed = max(0.0, (current_size - last_download_size) / dt)
                            job_state.download_speed = 0.7 * speed + 0.3 * job_state.download_speed if last_download_size > 0 else speed
                            last_download_size = current_size
                            last_download_time = now
                            job_state.total_downloaded_bytes = current_size
                            job_state.trigger_event.set()

                        if part_files:
                            job_state.current_download_file = sorted(part_files)[0]

                monitor_task = asyncio.create_task(monitor_download_speed())

            if is_unzip:
                from ..downloader import DownloadResult, download_telegram_media
                reply_msg_id = None
                if job.args:
                    try:
                        args_data = json.loads(job.args)
                        reply_msg_id = args_data.get("reply_message_id")
                    except Exception as e:
                        log.debug("Failed parsing job.args JSON for unzip job #%s: %s", job.id, e)

                existing_archive_files = [p for p in dest_dir.rglob("*") if p.is_file()]
                if not existing_archive_files and reply_msg_id:
                    try:
                        reply_msg = await self.client.get_messages(chat_id, message_ids=reply_msg_id)
                        if reply_msg and (reply_msg.document or reply_msg.video or reply_msg.audio or reply_msg.photo):
                            last_tg_time = 0.0
                            last_tg_bytes = 0

                            async def on_tg_download_progress(current: int, total: int, *args) -> None:
                                nonlocal last_tg_time, last_tg_bytes
                                now = time.time()
                                job_state.total_downloaded_bytes = current
                                if total > 0:
                                    job_state.total_expected_bytes = total
                                    job_state.download_pct = min(100.0, (current / total) * 100.0)

                                speed = 0.0
                                filename = "telegram_media"
                                if len(args) == 2:
                                    speed = float(args[0])
                                    filename = str(args[1])
                                elif len(args) == 1:
                                    filename = str(args[0])

                                if speed > 0:
                                    job_state.download_speed = speed
                                else:
                                    if last_tg_time > 0:
                                        dt = now - last_tg_time
                                        if dt >= 0.5:
                                            db = current - last_tg_bytes
                                            calc_speed = max(0.0, db / dt)
                                            job_state.download_speed = 0.7 * calc_speed + 0.3 * job_state.download_speed if last_tg_bytes > 0 else calc_speed
                                            last_tg_time = now
                                            last_tg_bytes = current
                                    else:
                                        last_tg_time = now
                                        last_tg_bytes = current

                                job_state.current_download_file = filename
                                job_state.trigger_event.set()

                            await download_telegram_media(
                                self.client, reply_msg, dest_dir, progress_cb=on_tg_download_progress
                            )
                    except Exception as e:
                        log.exception("Failed to download replied Telegram media for unzip job #%s: %s", job.id, e)

                archive_files = [p for p in dest_dir.rglob("*") if p.is_file()]
                result = DownloadResult(ok=True, files=archive_files)
            elif is_patch:
                from ..downloader import DownloadResult, download_direct, download_telegram_media
                from ..utils.apk_patcher import patch_apk_async

                reply_msg_id = args_dict.get("reply_message_id")
                target_url = args_dict.get("target_url")
                orig_filename = args_dict.get("original_filename") or "app.apk"
                user_id = args_dict.get("user_id") or chat_id

                patch_work_dir = (dest_dir.parent / f"{dest_dir.name}_patch_work").resolve()
                patch_work_dir.mkdir(parents=True, exist_ok=True)
                temp_input_apk = patch_work_dir / "input_temp.apk"

                try:
                    if reply_msg_id:
                        try:
                            reply_msg = await self.client.get_messages(chat_id, message_ids=reply_msg_id)
                            if reply_msg and (reply_msg.document or reply_msg.video or reply_msg.audio or reply_msg.photo):
                                async def on_tg_download_progress(current: int, total: int, *args) -> None:
                                    job_state.total_downloaded_bytes = current
                                    if total > 0:
                                        job_state.total_expected_bytes = total
                                        job_state.download_pct = min(100.0, (current / total) * 100.0)
                                    job_state.current_download_file = orig_filename
                                    job_state.trigger_event.set()

                                await download_telegram_media(
                                    self.client, reply_msg, patch_work_dir, progress_cb=on_tg_download_progress
                                )
                                downloaded_files = [p for p in patch_work_dir.rglob("*") if p.is_file() and p.name != "input_temp.apk"]
                                if downloaded_files:
                                    temp_input_apk = downloaded_files[0]
                        except Exception as e:
                            log.exception("Failed to download Telegram APK document for patch job #%s: %s", job.id, e)
                    elif target_url:
                        async def on_url_progress(current: int, total: int, filename: str, url: str | None = None) -> None:
                            job_state.total_downloaded_bytes = current
                            if total > 0:
                                job_state.total_expected_bytes = total
                                job_state.download_pct = min(100.0, (current / total) * 100.0)
                            job_state.current_download_file = filename or orig_filename
                            job_state.trigger_event.set()

                        downloaded_paths = await download_direct(target_url, patch_work_dir, progress_cb=on_url_progress)
                        if downloaded_paths:
                            temp_input_apk = downloaded_paths[0]

                    if not temp_input_apk.is_file():
                        raise RuntimeError("Failed to download input APK file for patching.")

                    def on_patch_status(stage: str) -> None:
                        job_state.current_download_file = stage
                        job_state.trigger_event.set()

                    ks_info = settings.get_user_keystore_info(user_id)
                    patched_file = await patch_apk_async(
                        input_apk=temp_input_apk,
                        output_dir=patch_work_dir,
                        original_filename=orig_filename,
                        keystore_info=ks_info,
                        progress_cb=on_patch_status,
                    )

                    if not patched_file.is_file():
                        raise RuntimeError("APK patcher did not produce an output APK file.")

                    dest_dir.mkdir(parents=True, exist_ok=True)
                    final_patched_apk = dest_dir / patched_file.name
                    shutil.move(str(patched_file), str(final_patched_apk))
                    result = DownloadResult(ok=True, files=[final_patched_apk])
                finally:
                    shutil.rmtree(patch_work_dir, ignore_errors=True)
            elif is_gdrive:
                from ..downloader import DownloadResult
                from ..gdrive import GoogleDriveDownloader, archive_all_folders_in_dir

                gdrive_link = job.url
                for prefix in ("gdrive:", "gd2tg:"):
                    gdrive_link = gdrive_link.removeprefix(prefix)

                def on_gdrive_progress(downloaded: int, speed: float, filename: str) -> None:
                    job_state.total_downloaded_bytes = downloaded
                    job_state.download_speed = speed
                    job_state.current_download_file = filename
                    job_state.trigger_event.set()

                archive_fmt = args_dict.get("archive_format")
                mirror_pixeldrain = bool(args_dict.get("mirror_pixeldrain"))
                gdrive_user_id = args_dict.get("user_id") or chat_id

                downloader = GoogleDriveDownloader(user_id=gdrive_user_id, progress_callback=on_gdrive_progress)

                await downloader.download_link(gdrive_link, dest_dir)


                if archive_fmt:
                    log.info("Archiving downloaded GDrive folders in %s format for job #%s", archive_fmt, job.id)
                    job_state.is_archiving = True
                    job_state.archive_format = archive_fmt
                    job_state.trigger_event.set()
                    try:
                        _archive_paths, _pd_links = await archive_all_folders_in_dir(
                            dest_dir,
                            archive_format=archive_fmt,
                            mirror_pixeldrain=mirror_pixeldrain,
                            job_state=job_state,
                        )
                        if _pd_links:
                            job_state.pixeldrain_links.extend(_pd_links)
                    finally:
                        job_state.is_archiving = False
                        job_state.trigger_event.set()
                elif mirror_pixeldrain:
                    from ..uploader import upload_to_pixeldrain
                    domain = settings.pixeldrain_domain or "pixeldrain.com"
                    log.info("Mirroring downloaded GDrive files to Pixeldrain for job #%s", job.id)
                    for f in sorted(dest_dir.rglob("*")):
                        if not f.is_file():
                            continue
                        try:
                            res, _ = await upload_to_pixeldrain(
                                f, api_key=settings.pixeldrain_api_key, domain=domain
                            )
                            if isinstance(res, dict) and res.get("id"):
                                pd_url = f"https://{domain}/u/{res['id']}"
                                log.info("Successfully mirrored raw file '%s' to Pixeldrain: %s", f.name, pd_url)
                                job_state.pixeldrain_links.append((f.name, pd_url))
                        except Exception as pe:
                            log.exception("Failed to mirror raw file '%s' to Pixeldrain: %s", f.name, pe)


                final_files = [p for p in dest_dir.rglob("*") if p.is_file()]
                result = DownloadResult(ok=True, files=final_files)
            elif is_mega:
                from ..downloader import DownloadResult
                from ..mega import MegaDownloader

                def on_mega_progress(downloaded: int, speed: float, filename: str) -> None:
                    job_state.total_downloaded_bytes = downloaded
                    job_state.download_speed = speed
                    job_state.current_download_file = filename
                    job_state.trigger_event.set()

                downloader = MegaDownloader(user_id=chat_id, progress_callback=on_mega_progress)
                final_files = await downloader.download_link(job.url, dest_dir)
                result = DownloadResult(ok=True, files=final_files)

            elif is_torrent:
                def on_torrent_progress(
                    pct: float,
                    downloaded_bytes: float,
                    speed_bytes: float,
                    seeders: int = 0,
                    connections: int = 0,
                    name: str | None = None
                ) -> None:
                    job_state.download_pct = pct
                    job_state.total_downloaded_bytes = downloaded_bytes
                    job_state.download_speed = speed_bytes
                    job_state.torrent_seeders = seeders
                    job_state.torrent_peers = connections
                    if name:
                        job_state.torrent_name = name
                    job_state.trigger_event.set()

                if args_dict.get("engine") == "aria2":
                    from ..downloader import download_via_aria2_async
                    result = await download_via_aria2_async(
                        cleaned_url, dest_dir, options=args_dict.get("aria_options") or {}, on_progress=on_torrent_progress, register_proc=reg
                    )
                else:
                    from ..downloader import download_torrent_async
                    result = await download_torrent_async(
                        cleaned_url, dest_dir, on_progress=on_torrent_progress, register_proc=reg
                    )

            elif cleaned_url.startswith("mirror_tg:"):
                parts = cleaned_url.split(":")
                tgt_chat_id = int(parts[1])
                tgt_msg_id = int(parts[2])
                tgt_msg = await self.client.get_messages(tgt_chat_id, tgt_msg_id)
                if not tgt_msg or tgt_msg.empty:
                    raise Exception(f"Failed to fetch Telegram message {tgt_msg_id} for mirror")

                last_tg_time = 0.0
                last_tg_bytes = 0

                async def on_tg_progress(current: int, total: int, *args) -> None:
                    nonlocal last_tg_time, last_tg_bytes
                    now = time.time()
                    job_state.total_downloaded_bytes = current
                    if total > 0:
                        job_state.total_expected_bytes = total
                        job_state.download_pct = min(100.0, (current / total) * 100.0)

                    speed = 0.0
                    filename = "telegram_media"
                    if len(args) == 2:
                        speed = float(args[0])
                        filename = str(args[1])
                    elif len(args) == 1:
                        filename = str(args[0])

                    if speed > 0:
                        job_state.download_speed = speed
                    else:
                        if last_tg_time > 0:
                            dt = now - last_tg_time
                            if dt >= 0.5:
                                db = current - last_tg_bytes
                                calc_speed = max(0.0, db / dt)
                                job_state.download_speed = 0.7 * calc_speed + 0.3 * job_state.download_speed if last_tg_bytes > 0 else calc_speed
                                last_tg_time = now
                                last_tg_bytes = current
                        else:
                            last_tg_time = now
                            last_tg_bytes = current

                    job_state.current_download_file = filename
                    job_state.trigger_event.set()

                from ..downloader import DownloadResult, TelegramDownloader
                downloader = TelegramDownloader(
                    client=self.client,
                    message=tgt_msg,
                    dest_dir=dest_dir,
                    progress_cb=on_tg_progress
                )
                dl_path = await downloader.download()
                result = DownloadResult(ok=True, files=[dl_path])

            elif cleaned_url.startswith("mirror:"):
                target_u = cleaned_url[len("mirror:"):]
                from ..downloader import (
                    DownloadResult,
                    download_direct,
                    download_via_aria2_async,
                    run_with_progress,
                )
                async def on_direct_progress(current: int, total: int, filename: str, url: str | None = None) -> None:
                    job_state.total_downloaded_bytes = current
                    job_state.total_expected_bytes = total
                    if total > 0:
                        job_state.download_pct = min(100.0, (current / total) * 100.0)
                    if filename:
                        job_state.current_download_file = filename
                    if url:
                        job_state.current_download_url = url
                    job_state.trigger_event.set()

                if args_dict.get("engine") == "aria2":
                    def on_aria_progress(
                        pct: float,
                        downloaded_bytes: float,
                        speed_bytes: float,
                        seeders: int = 0,
                        connections: int = 0,
                        name: str | None = None
                    ) -> None:
                        job_state.download_pct = pct
                        job_state.total_downloaded_bytes = downloaded_bytes
                        job_state.download_speed = speed_bytes
                        if name:
                            job_state.current_download_file = name
                        job_state.trigger_event.set()

                    result = await download_via_aria2_async(
                        target_u, dest_dir, options=args_dict.get("aria_options") or {}, on_progress=on_aria_progress, register_proc=reg
                    )
                else:
                    try:
                        downloaded_paths = await download_direct(target_u, dest_dir, progress_cb=on_direct_progress)
                        result = DownloadResult(ok=True, files=downloaded_paths)
                    except Exception as de:
                        log.warning("DirectDownloader failed for mirror link %s, attempting gallery-dl fallback: %s", target_u, de)
                        def on_dl_progress(count: int, filename: str | None = None, current_url: str | None = None) -> None:
                            job_state.download_count = count
                            if filename:
                                job_state.current_download_file = filename
                            if current_url:
                                job_state.current_download_url = current_url
                            job_state.trigger_event.set()
                        from ..downloader import run_cyberdrop_dl
                        result = await run_with_progress(
                            target_u,
                            dest_dir,
                            on_progress=on_dl_progress,
                            register_proc=reg,
                            user_id=job.chat_id,
                        )
                        if not result.ok:
                            log.warning("gallery-dl failed for mirror link %s, attempting cyberdrop-dl fallback", target_u)
                            result = await run_cyberdrop_dl(
                                target_u,
                                dest_dir,
                                on_progress=on_dl_progress,
                                register_proc=reg,
                                user_id=job.chat_id,
                            )

            elif cleaned_url.startswith(("cdl:", "cyberdrop-dl:")) or args_dict.get("engine") == "cyberdrop-dl":
                target_u = cleaned_url
                if target_u.startswith("cdl:"):
                    target_u = target_u[len("cdl:"):]
                elif target_u.startswith("cyberdrop-dl:"):
                    target_u = target_u[len("cyberdrop-dl:"):]

                extra_args_list = []
                if job.args:
                    try:
                        args_data = json.loads(job.args)
                        if isinstance(args_data, dict):
                            pwd = args_data.get("password")
                            if pwd:
                                extra_args_list.extend(["--password", str(pwd)])
                            raw_extra = args_data.get("extra_args")
                            if isinstance(raw_extra, list):
                                extra_args_list.extend([str(x) for x in raw_extra])
                        elif isinstance(args_data, list):
                            extra_args_list = [str(x) for x in args_data]
                    except Exception as e:
                        log.warning("Failed to parse job.args for job #%s: %s", job.id, e)

                from ..downloader import (
                    DownloadResult,
                    download_direct,
                    run_cyberdrop_dl,
                    run_with_progress,
                )

                def on_cdl_progress(count: int, filename: str | None = None, current_url: str | None = None) -> None:
                    job_state.download_count = count
                    if filename:
                        job_state.current_download_file = filename
                    if current_url:
                        job_state.current_download_url = current_url
                    job_state.trigger_event.set()

                result = await run_cyberdrop_dl(
                    target_u,
                    dest_dir,
                    on_progress=on_cdl_progress,
                    extra_args=extra_args_list if extra_args_list else None,
                    register_proc=reg,
                    user_id=job.chat_id,
                )
                if not result.ok:
                    log.info("cyberdrop-dl failed for %s. Attempting gallery-dl fallback...", target_u)
                    result = await run_with_progress(
                        target_u,
                        dest_dir,
                        on_progress=on_cdl_progress,
                        extra_args=extra_args_list if extra_args_list else None,
                        register_proc=reg,
                        user_id=job.chat_id,
                    )
                if not result.ok:
                    log.info("gallery-dl fallback also failed for %s. Falling back to DirectDownloader...", target_u)
                    async def on_fallback_progress(current: int, total: int, filename: str, url: str | None = None) -> None:
                        job_state.total_downloaded_bytes = current
                        job_state.total_expected_bytes = total
                        if total > 0:
                            job_state.download_pct = min(100.0, (current / total) * 100.0)
                        if filename:
                            job_state.current_download_file = filename
                        if url:
                            job_state.current_download_url = url
                        job_state.trigger_event.set()
                    try:
                        downloaded_paths = await download_direct(target_u, dest_dir, progress_cb=on_fallback_progress)
                        result = DownloadResult(ok=True, files=downloaded_paths)
                    except Exception as fallback_err:
                        log.warning("DirectDownloader fallback also failed for %s: %s", target_u, fallback_err)

            elif cleaned_url.startswith("direct:") or is_direct_url(cleaned_url) or (await is_m3u8_url(cleaned_url)):
                direct_url = job.url if (job.url.startswith("[") and job.url.endswith("]")) else job.url.removeprefix("direct:")
                async def on_direct_progress(current: int, total: int, filename: str, url: str | None = None) -> None:
                    job_state.total_downloaded_bytes = current
                    job_state.total_expected_bytes = total
                    if total > 0:
                        job_state.download_pct = min(100.0, (current / total) * 100.0)
                    if filename:
                        job_state.current_download_file = filename
                    if url:
                        job_state.current_download_url = url
                    job_state.trigger_event.set()

                from ..downloader import (
                    DownloadResult,
                    download_direct,
                    download_via_aria2_async,
                )
                if args_dict.get("engine") == "aria2":
                    def on_aria_progress(
                        pct: float,
                        downloaded_bytes: float,
                        speed_bytes: float,
                        seeders: int = 0,
                        connections: int = 0,
                        name: str | None = None
                    ) -> None:
                        job_state.download_pct = pct
                        job_state.total_downloaded_bytes = downloaded_bytes
                        job_state.download_speed = speed_bytes
                        if name:
                            job_state.current_download_file = name
                        job_state.trigger_event.set()

                    result = await download_via_aria2_async(
                        direct_url, dest_dir, options=args_dict.get("aria_options") or {}, on_progress=on_aria_progress, register_proc=reg
                    )
                else:
                    downloaded_paths = await download_direct(job.url, dest_dir, progress_cb=on_direct_progress)
                    result = DownloadResult(ok=True, files=downloaded_paths)
            else:
                extra_args_list: list[str] = []
                if job.args:
                    try:
                        args_data = json.loads(job.args)
                        if isinstance(args_data, dict):
                            pwd = args_data.get("password")
                            if pwd:
                                extra_args_list.extend(["--password", str(pwd)])
                            raw_extra = args_data.get("extra_args")
                            if isinstance(raw_extra, list):
                                extra_args_list.extend([str(x) for x in raw_extra])
                        elif isinstance(args_data, list):
                            extra_args_list = [str(x) for x in args_data]
                    except Exception as e:
                        log.warning("Failed to parse job.args for job #%s: %s", job.id, e)

                from ..downloader import (
                    DownloadResult,
                    download_direct,
                    download_via_aria2_async,
                    run_cyberdrop_dl,
                    run_with_progress,
                )
                if args_dict.get("engine") == "aria2":
                    def on_aria_progress(
                        pct: float,
                        downloaded_bytes: float,
                        speed_bytes: float,
                        seeders: int = 0,
                        connections: int = 0,
                        name: str | None = None
                    ) -> None:
                        job_state.download_pct = pct
                        job_state.total_downloaded_bytes = downloaded_bytes
                        job_state.download_speed = speed_bytes
                        if name:
                            job_state.current_download_file = name
                        job_state.trigger_event.set()

                    result = await download_via_aria2_async(
                        job.url, dest_dir, options=args_dict.get("aria_options") or {}, on_progress=on_aria_progress, register_proc=reg
                    )
                else:
                    def on_download_progress(count: int, filename: str | None = None, current_url: str | None = None) -> None:
                        job_state.download_count = count
                        if filename:
                            job_state.current_download_file = filename
                        if current_url:
                            job_state.current_download_url = current_url
                        job_state.trigger_event.set()

                    result = await run_with_progress(
                        job.url,
                        dest_dir,
                        on_progress=on_download_progress,
                        extra_args=extra_args_list if extra_args_list else None,
                        register_proc=reg,
                        user_id=job.chat_id,
                    )
                    if not result.ok:
                        log.info("gallery-dl failed or unsupported site for %s. Attempting cyberdrop-dl immediate fallback...", job.url)
                        result = await run_cyberdrop_dl(
                            job.url,
                            dest_dir,
                            on_progress=on_download_progress,
                            extra_args=extra_args_list if extra_args_list else None,
                            register_proc=reg,
                            user_id=job.chat_id,
                        )
                    if not result.ok:
                        log.info("cyberdrop-dl fallback also failed for %s. Falling back to DirectDownloader...", job.url)
                        async def on_fallback_progress(current: int, total: int, filename: str, url: str | None = None) -> None:
                            job_state.total_downloaded_bytes = current
                            job_state.total_expected_bytes = total
                            if total > 0:
                                job_state.download_pct = min(100.0, (current / total) * 100.0)
                            if filename:
                                job_state.current_download_file = filename
                            if url:
                                job_state.current_download_url = url
                            job_state.trigger_event.set()
                        try:
                            downloaded_paths = await download_direct(job.url, dest_dir, progress_cb=on_fallback_progress)
                            result = DownloadResult(ok=True, files=downloaded_paths)
                        except Exception as fallback_err:
                            log.warning("DirectDownloader fallback also failed for %s: %s", job.url, fallback_err)


            job_state.downloader_result = result
            log.info("Download finished for job #%s (ok=%s)", job.id, result.ok)
        except Exception as e:
            from ..downloader import DownloadResult
            log.exception("Download failed for job #%s", job.id)
            job_state.downloader_result = DownloadResult(ok=False, error_tail=str(e))
        finally:
            job_state.downloader_done.set()
            job_state.trigger_event.set()
            if 'download_updater_task' in locals() and download_updater_task:
                download_updater_task.cancel()
                await asyncio.gather(download_updater_task, return_exceptions=True)
            if monitor_task:
                monitor_task.cancel()
                await asyncio.gather(monitor_task, return_exceptions=True)

    async def _process_upload(self, job_state: JobState) -> None:
        job_state.active_upload_task = asyncio.current_task()
        from ..handlers.conversion_state import conversion_session_store
        from ..utils.archive import (
            ARCHIVE_EXT,
            ArchivePasswordRequired,
            archive_session_store,
            extract_archive_async,
            get_split_archive_info,
        )
        from ..utils.media import (
            AUDIO_CONVERSION_EXT,
            CONVERSION_EXT,
            convert_audio_async,
            convert_media_async,
        )

        job = job_state.job
        chat_id = job.chat_id
        dest_dir = job_state.dest_dir
        extract_dir = dest_dir.parent / f"{dest_dir.name}_extracted"
        cleaned_url = job.url
        if job.url.startswith("[") and job.url.endswith("]"):
            try:
                parsed = json.loads(job.url)
                if parsed and isinstance(parsed, list):
                    cleaned_url = parsed[0]
            except Exception as e:
                log.debug("Failed parsing JSON array URL in upload worker for job #%s: %s", job.id, e)

        is_torrent = (
            cleaned_url.startswith("magnet:") or
            cleaned_url.startswith("torrent:") or
            cleaned_url.endswith(".torrent") or
            "magnet:?xt=" in cleaned_url
        )

        is_mirror_job = (
            cleaned_url.startswith("mirror:") or
            cleaned_url.startswith("mirror_tg:")
        )

        is_patch_job = cleaned_url.startswith("patch:")
        if is_patch_job:
            await job_state.downloader_done.wait()

        is_unzip_job = cleaned_url.startswith("unzip:")
        has_archive_fmt = False
        upload_tg = False
        if job.args:
            try:
                args_dict = json.loads(job.args)
                if isinstance(args_dict, dict):
                    if args_dict.get("archive_format"):
                        has_archive_fmt = True
                    if args_dict.get("is_mirror"):
                        is_mirror_job = True
                    if args_dict.get("upload_tg"):
                        upload_tg = True
                    if args_dict.get("unzip"):
                        is_unzip_job = True
            except Exception as e:
                log.debug("Failed parsing job.args JSON in upload worker for job #%s: %s", job.id, e)

        async def report(text: str) -> None:
            await safe_send(self.client, chat_id, text, link_preview_options=LinkPreviewOptions(is_disabled=True))

        async def status_updater_loop() -> None:
            while not job_state.uploader_done.is_set():
                try:
                    await asyncio.sleep(5)
                    if job_state.uploader_done.is_set():
                        break

                    db_job = await self.store.get_job(job.id)
                    if not db_job or db_job.status == JobStatus.CANCELLED:
                        break

                    if job_state.msg_id:
                        status_text = compile_job_status_text(db_job, job_state)
                        if status_text != job_state.last_edited_text:
                            from pyrogram.types import (
                                InlineKeyboardButton,
                                InlineKeyboardMarkup,
                            )
                            keyboard = InlineKeyboardMarkup([
                                [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                            ])
                            if await safe_edit(self.client, chat_id, job_state.msg_id, status_text, reply_markup=keyboard):
                                job_state.last_edited_text = status_text
                                if not job_state.is_pinned:
                                    await safe_pin(self.client, chat_id, job_state.msg_id)
                                    job_state.is_pinned = True
                except Exception as e:
                    log.debug("Error in upload status updater loop for job #%s: %s", job.id, e)

        async def perform_uploads() -> None:
            nonlocal job
            if not dest_dir.exists():
                return

            from ..uploader import should_ignore_file

            if job_state.downloader_done.is_set() and job_state.uploader_done.is_set():
                try:
                    for d in (dest_dir, extract_dir):
                        if d.exists():
                            for p in list(d.rglob("*")):
                                if p.is_file() and should_ignore_file(p):
                                    try:
                                        p.unlink()
                                    except Exception:
                                        # expected: file already unlinked
                                        pass
                except Exception as e:
                    log.debug("Notice scanning directory for ignored files cleanup: %s", e)

            try:
                files = []
                if dest_dir.exists():
                    files.extend(sorted((p for p in dest_dir.rglob("*") if p.is_file() and not should_ignore_file(p)), key=natural_path_sort_key))
                if extract_dir.exists():
                    files.extend(sorted((p for p in extract_dir.rglob("*") if p.is_file() and not should_ignore_file(p)), key=natural_path_sort_key))
            except Exception as e:
                log.debug("Error listing files for upload in job #%s: %s", job.id, e)
                return

            db_total = len(files)
            if job.total_files != db_total:
                await self.store.update_progress(job.id, total_files=db_total)
                job = await self.store.get_job(job.id)

            if is_mirror_job and not getattr(job_state, "web_mirror_done", False):
                if not upload_tg:
                    from .mirror import mirror_file_to_web_hosts
                    log.info("Processing mirror upload to web hosts for job #%s", job.id)

                    all_downloaded = [f for f in files if f.is_file() and not should_ignore_file(f)]
                    for f in all_downloaded:
                        try:
                            f_rel = str(f.relative_to(extract_dir))
                        except ValueError:
                            # expected: file is in dest_dir rather than extract_dir
                            f_rel = str(f.relative_to(dest_dir))

                        job_state.uploading_files.add(f_rel)
                        job_state.current_upload_file = f.name
                        job_state.trigger_event.set()

                        async def on_hosts_info_update(h_info: dict) -> None:
                            job_state.web_mirror_info = dict(h_info)
                            job_state.trigger_event.set()

                        await mirror_file_to_web_hosts(
                            f,
                            hosts_info_callback=on_hosts_info_update
                        )

                        if f_rel in job_state.uploading_files:
                            job_state.uploading_files.remove(f_rel)
                else:
                    log.info("Mirror job #%s specified -tg flag: skipping web host mirror, will upload to Telegram", job.id)

                job_state.web_mirror_done = True

            pending = []
            for f in files:
                try:
                    f_rel = str(f.relative_to(extract_dir))
                except ValueError:
                    # expected: file is in dest_dir rather than extract_dir
                    f_rel = str(f.relative_to(dest_dir))
                if (f_rel not in job_state.uploaded_filenames and
                    f_rel not in job_state.uploading_files and
                    f_rel not in job_state.failed_uploads):
                    pending.append(f)

            if is_mirror_job and not upload_tg:
                log.info("Mirror job #%s completing files without Telegram upload", job.id)
                for f in pending:
                    if not f.exists():
                        continue
                    if not job_state.downloader_done.is_set():
                        try:
                            sz1 = f.stat().st_size
                            await asyncio.sleep(1.5)
                            sz2 = f.stat().st_size
                            if sz1 != sz2 or sz1 == 0:
                                continue
                        except Exception:
                            # expected: file may have been moved or unlinked during check
                            continue

                    from ..utils.filetype import ensure_extension
                    f = await ensure_extension(f)

                    try:
                        f_rel = str(f.relative_to(extract_dir))
                    except ValueError:
                        # expected: file is in dest_dir rather than extract_dir
                        f_rel = str(f.relative_to(dest_dir))

                    job_state.uploaded_filenames.add(f_rel)
                    await self.store.mark_uploaded(job.id, f_rel)
                    try:
                        if f.exists():
                            f_size = f.stat().st_size
                            f.unlink(missing_ok=True)
                            job_state.deleted_bytes += f_size
                    except Exception as de:
                        log.debug("Notice on post-mirror file cleanup for %s: %s", f, de)

                if job_state.downloader_done.is_set():
                    job_state.uploader_done.set()
                return

            for f in pending:
                if not f.exists():
                    continue
                if not job_state.downloader_done.is_set():
                    try:
                        sz1 = f.stat().st_size
                        await asyncio.sleep(1.5)
                        sz2 = f.stat().st_size
                        if sz1 != sz2 or sz1 == 0:
                            continue
                    except Exception:
                        # expected: file may have been moved or unlinked during check
                        continue

                from ..utils.filetype import ensure_extension
                f = await ensure_extension(f)

                db_job = await self.store.get_job(job.id)
                if db_job and db_job.status == JobStatus.CANCELLED:
                    return

                if job_state.uploader_done.is_set():
                    return

                try:
                    f_rel = str(f.relative_to(extract_dir))
                except ValueError:
                    f_rel = str(f.relative_to(dest_dir))

                is_internal_split = (f_rel in job_state.split_parts_created or f.name in job_state.split_parts_created)
                is_archive = (f.suffix.lower() in ARCHIVE_EXT) and not is_internal_split
                f_split = get_split_archive_info(f.name)
                if not is_archive and not is_internal_split and f_split:
                    if f_split["part"] == 1:
                        base_ext = f".{f_split.get('ext')}" if f_split.get("ext") else None
                        if not base_ext or base_ext.lower() in ARCHIVE_EXT:
                            is_archive = True

                if is_archive:
                    archive_prompt_msg_id = None
                    archive_id = None
                    for aid, rel_path in archive_session_store.get_archive_ids(job.id).items():
                        if rel_path == f_rel:
                            archive_id = aid
                            break
                    if archive_id is None:
                        archive_id = archive_session_store.get_next_archive_id(job.id)
                        archive_session_store.register_archive_id(job.id, archive_id, f_rel)

                    if is_unzip_job:
                        archive_session_store.set_choice(job.id, archive_id, "ext")
                    else:
                        if not archive_session_store.has_choice(job.id, archive_id):
                            prompt_text = compile_archive_prompt_text(job.id, f.name)
                            kb = InlineKeyboardMarkup([
                                [
                                    InlineKeyboardButton("Upload Archive Only", callback_data=f"archive_only:{job.id}:{archive_id}"),
                                    InlineKeyboardButton("Upload + Extract", callback_data=f"archive_ext:{job.id}:{archive_id}")
                                ],
                                [
                                    InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")
                                ]
                            ])
                            prompt_msg = await safe_send(self.client, chat_id, prompt_text, reply_markup=kb)
                            if prompt_msg:
                                archive_prompt_msg_id = prompt_msg.id
                                evt = archive_session_store.create_event(job.id, archive_id)

                                start_t = time.time()
                                while not job_state.uploader_done.is_set() and not evt.is_set():
                                    if time.time() - start_t >= 15.0:
                                        break
                                    try:
                                        await asyncio.wait_for(evt.wait(), timeout=2.0)
                                    except TimeoutError:
                                        pass
                                if job_state.uploader_done.is_set():
                                    return

                                if not evt.is_set():
                                    if archive_prompt_msg_id:
                                        try:
                                            await self.client.delete_messages(chat_id, archive_prompt_msg_id)
                                        except Exception:
                                            # expected: prompt message already deleted
                                            pass
                                        archive_prompt_msg_id = None
                                    archive_session_store.set_choice(job.id, archive_id, "only")
                            else:
                                archive_session_store.set_choice(job.id, archive_id, "only")

                    choice = archive_session_store.get_choice(job.id, archive_id)
                    if choice == "ext" and f_rel not in archive_session_store.get_extracted_archives(job.id):
                        archive_session_store.add_extracted_archive(job.id, f_rel)

                        status_msg = await safe_send(
                            self.client,
                            chat_id,
                            compile_extraction_status_text(job.id, f.name),
                            reply_markup=InlineKeyboardMarkup([
                                [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                            ]),
                            link_preview_options=LinkPreviewOptions(is_disabled=True)
                        )

                        extract_dir.mkdir(parents=True, exist_ok=True)
                        before_files = set()
                        try:
                            before_files = {p.resolve() for p in extract_dir.rglob("*") if p.is_file()}
                        except Exception as e:
                            log.debug("Notice scanning extract_dir before extraction: %s", e)

                        try:
                            password = None
                            if job.args:
                                try:
                                    parsed = json.loads(job.args)
                                    if isinstance(parsed, dict):
                                        password = parsed.get("password")
                                except Exception as e:
                                    log.debug("Failed parsing job.args JSON for password in extraction: %s", e)

                            extracted = await extract_archive_async(f, extract_dir, password=password)
                            if extracted:
                                log.info("Successfully extracted archive %s", f.name)
                                job_state.trigger_event.set()
                                try:
                                    after_files = {p.resolve() for p in extract_dir.rglob("*") if p.is_file()}
                                    new_files = after_files - before_files
                                    for new_f in new_files:
                                        archive_session_store.add_extracted_file_name(job.id, new_f.name)
                                except Exception as e:
                                    log.debug("Notice scanning extract_dir after extraction: %s", e)

                                if is_unzip_job:
                                    try:
                                        f.unlink(missing_ok=True)
                                    except Exception:
                                        # expected: file already unlinked
                                        pass
                                    job_state.uploaded_filenames.add(f_rel)

                                    if f_split and f_split["part"] == 1:
                                        for sibling in dest_dir.iterdir():
                                             if sibling.is_file() and f_split["pattern"].match(sibling.name):
                                                 try:
                                                     sibling.unlink(missing_ok=True)
                                                 except Exception:
                                                     # expected: sibling already unlinked
                                                     pass
                                                 try:
                                                     sib_rel = str(sibling.relative_to(dest_dir))
                                                     job_state.uploaded_filenames.add(sib_rel)
                                                 except Exception:
                                                     # expected: sibling is outside dest_dir
                                                     pass

                                if archive_prompt_msg_id:
                                    try:
                                        await self.client.delete_messages(chat_id, archive_prompt_msg_id)
                                    except Exception:
                                        # expected: prompt message already deleted
                                        pass
                                try:
                                    if status_msg:
                                        await self.client.delete_messages(chat_id, status_msg.id)
                                except Exception:
                                    # expected: status message already deleted
                                    pass
                                end_extraction_msg = compile_extraction_success_status_text(job.id, f.name)
                                success_msg = await safe_send(
                                    self.client,
                                    chat_id,
                                    end_extraction_msg,
                                    link_preview_options=LinkPreviewOptions(is_disabled=True)
                                )
                                async def delete_success_msg(m):
                                    await asyncio.sleep(5)
                                    try:
                                        await self.client.delete_messages(chat_id, m.id)
                                    except Exception:
                                        # expected: success message already deleted
                                        pass
                                if success_msg:
                                    asyncio.create_task(delete_success_msg(success_msg))

                                break
                            else:
                                log.error("Failed to extract archive %s", f.name)
                                try:
                                    if status_msg:
                                        await self.client.delete_messages(chat_id, status_msg.id)
                                except Exception:
                                    # expected: status message already deleted
                                    pass
                                fail_msg = await safe_send(
                                    self.client,
                                    chat_id,
                                    compile_extraction_failed_status_text(job.id, f.name),
                                    link_preview_options=LinkPreviewOptions(is_disabled=True)
                                )
                                if is_unzip_job:
                                    raise Exception(f"Failed to extract archive {f.name}")
                                if archive_prompt_msg_id:
                                    try:
                                        await self.client.delete_messages(chat_id, archive_prompt_msg_id)
                                    except Exception:
                                        # expected: prompt message already deleted
                                        pass
                                async def delete_fail_msg(m):
                                    await asyncio.sleep(5)
                                    try:
                                        await self.client.delete_messages(chat_id, m.id)
                                    except Exception:
                                        # expected: fail message already deleted
                                        pass
                                if fail_msg:
                                    asyncio.create_task(delete_fail_msg(fail_msg))
                        except ArchivePasswordRequired:
                            log.warning("Archive %s requires a password to extract", f.name)
                            try:
                                if status_msg:
                                    await self.client.delete_messages(chat_id, status_msg.id)
                            except Exception:
                                # expected: status message already deleted
                                pass

                            prompt_msg = await safe_send(
                                self.client,
                                chat_id,
                                f"**Password Required**: `{f.name}` is password-protected or password was incorrect.\n\n"
                                f"Please reply directly to this message with the password to extract it.",
                                reply_markup=ForceReply(placeholder="Enter archive password")
                            )
                            if prompt_msg:
                                if job.id not in _password_prompt_events:
                                    _password_prompt_events[job.id] = {}
                                event = asyncio.Event()
                                data = {"password": None}
                                _password_prompt_events[job.id][archive_id] = (event, data)
                                _password_prompt_messages[prompt_msg.id] = (job.id, archive_id, chat_id)

                                try:
                                    start_time = time.time()
                                    while not job_state.uploader_done.is_set() and not event.is_set():
                                        if time.time() - start_time >= 300:
                                            raise TimeoutError()
                                        try:
                                            await asyncio.wait_for(event.wait(), timeout=2.0)
                                        except TimeoutError:
                                            # expected: timeout waiting for password prompt event tick
                                            pass
                                    if job_state.uploader_done.is_set():
                                        return
                                    new_password = data["password"]

                                    job_args_dict = {}
                                    if job.args:
                                        try:
                                            job_args_dict = json.loads(job.args)
                                        except Exception as e:
                                            log.debug("Failed parsing job.args JSON for password update: %s", e)
                                    job_args_dict["password"] = new_password
                                    await self.store.db.execute(
                                        "UPDATE jobs SET args = ? WHERE id = ?",
                                        (json.dumps(job_args_dict), job.id)
                                    )
                                    await self.store.db.commit()

                                    job_state.job = await self.store.get_job(job.id)
                                    job = job_state.job

                                    try:
                                        await self.client.delete_messages(chat_id, prompt_msg.id)
                                    except Exception:
                                        # expected: prompt message already deleted
                                        pass
                                    break
                                except TimeoutError:
                                    try:
                                        await self.client.delete_messages(chat_id, prompt_msg.id)
                                    except Exception:
                                        # expected: prompt message already deleted
                                        pass
                                    await safe_send(
                                        self.client,
                                        chat_id,
                                        f"**Job #{job.id} aborted**: Timeout waiting for password for `{f.name}`."
                                    )
                                    raise Exception(f"Timeout waiting for password for {f.name}")
                                finally:
                                    _password_prompt_events.get(job.id, {}).pop(archive_id, None)
                                    _password_prompt_messages.pop(prompt_msg.id, None)
                            else:
                                raise
                        except Exception:
                            try:
                                if status_msg:
                                    await self.client.delete_messages(chat_id, status_msg.id)
                            except Exception:
                                # expected: status message already deleted
                                pass
                            raise

                    if choice == "only" and archive_prompt_msg_id:
                        try:
                            await self.client.delete_messages(chat_id, archive_prompt_msg_id)
                        except Exception:
                            # expected: prompt message already deleted
                            pass

                is_file_archive = (f.suffix.lower() in ARCHIVE_EXT) and not is_internal_split
                if not is_file_archive and not is_internal_split:
                    f_split = get_split_archive_info(f.name)
                    if f_split:
                        base_ext = f".{f_split.get('ext')}" if f_split.get("ext") else None
                        if not base_ext or base_ext.lower() in ARCHIVE_EXT:
                            is_file_archive = True

                if is_unzip_job and is_file_archive:
                    try:
                        f.unlink(missing_ok=True)
                    except Exception:
                        # expected: file already unlinked
                        pass
                    job_state.uploaded_filenames.add(f_rel)

                    f_split = get_split_archive_info(f.name)
                    if f_split and f_split["part"] == 1:
                        for sibling in dest_dir.iterdir():
                            if sibling.is_file() and f_split["pattern"].match(sibling.name):
                                try:
                                    sibling.unlink(missing_ok=True)
                                except Exception:
                                    # expected: sibling already unlinked
                                    pass
                                try:
                                    sib_rel = str(sibling.relative_to(dest_dir))
                                    job_state.uploaded_filenames.add(sib_rel)
                                except Exception:
                                    pass
                    continue

                is_incompatible = f.suffix.lower() in CONVERSION_EXT
                if is_incompatible:
                    if f.name not in conversion_session_store.get_converted_files(job.id):
                        conversion_session_store.add_converted_file(job.id, f.name)

                        log.info("Automatically converting video %s to MKV for job %s", f.name, job.id)
                        output_name = f.stem + "_converted.mkv"
                        output_path = f.parent / output_name

                        conv_msg = await safe_send(
                            self.client,
                            chat_id,
                            compile_conversion_running_status_text(job.id, f.name),
                            reply_markup=InlineKeyboardMarkup([
                                [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                            ])
                        )

                        job_state.is_converting = True
                        job_state.conversion_file = f.name
                        job_state.trigger_event.set()

                        try:
                            success = await convert_media_async(f, output_path)
                        finally:
                            job_state.is_converting = False
                            job_state.conversion_file = None
                            job_state.trigger_event.set()

                        if conv_msg:
                            try:
                                await self.client.delete_messages(chat_id, conv_msg.id)
                            except Exception:
                                # expected: conversion message already deleted
                                pass

                        if success:
                            # Converter may have fallen back to MKV if MP4 muxing was incompatible
                            if not output_path.exists():
                                mkv_path = output_path.with_suffix(".mkv")
                                if mkv_path.exists():
                                    log.info("Converter produced MKV fallback: %s", mkv_path.name)
                                    output_path = mkv_path
                                    output_name = mkv_path.name
                            log.info("Successfully converted video %s to %s", f.name, output_name)
                            try:
                                f.unlink(missing_ok=True)
                            except Exception:
                                # expected: original video file already unlinked
                                pass
                            f = output_path
                            try:
                                f_rel = str(f.relative_to(extract_dir))
                            except ValueError:
                                f_rel = str(f.relative_to(dest_dir))
                        else:
                            log.error("Failed to convert video %s; keeping original file", f.name)

                # Audio format conversion & processing using Pedalboard
                is_audio_incompatible = f.suffix.lower() in AUDIO_CONVERSION_EXT
                if is_audio_incompatible:
                    conv_id = None
                    for cid, fname in conversion_session_store.get_conversion_ids(job.id).items():
                        if fname == f.name:
                            conv_id = cid
                            break
                    if conv_id is None:
                        conv_id = conversion_session_store.get_next_conversion_id(job.id)
                        conversion_session_store.register_conversion_id(job.id, conv_id, f.name)

                    choice = conversion_session_store.get_choice(job.id, conv_id)
                    if choice != "orig" and f.name not in conversion_session_store.get_converted_files(job.id):
                        conversion_prompt_msg_id = None
                        if choice is None:
                            keyboard = InlineKeyboardMarkup([
                                [
                                    InlineKeyboardButton("Convert to MP3", callback_data=f"convert_mp3:{job.id}:{conv_id}"),
                                    InlineKeyboardButton("Original File", callback_data=f"convert_orig:{job.id}:{conv_id}")
                                ],
                                [
                                    InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")
                                ]
                            ])
                            prompt_text = compile_audio_conversion_prompt_text(job.id, f.name)
                            prompt_msg = await safe_send(self.client, chat_id, prompt_text, reply_markup=keyboard)
                            if prompt_msg:
                                conversion_prompt_msg_id = prompt_msg.id
                                evt = conversion_session_store.create_event(job.id, conv_id)

                                start_t = time.time()
                                while not job_state.uploader_done.is_set() and not evt.is_set():
                                    if time.time() - start_t >= 15.0:
                                        break
                                    try:
                                        await asyncio.wait_for(evt.wait(), timeout=2.0)
                                    except TimeoutError:
                                        # expected: timeout waiting for audio conversion prompt event tick
                                        pass
                                if job_state.uploader_done.is_set():
                                    return

                                if not evt.is_set():
                                    if conversion_prompt_msg_id:
                                        try:
                                            await self.client.delete_messages(chat_id, conversion_prompt_msg_id)
                                        except Exception:
                                            # expected: prompt message already deleted
                                            pass
                                        conversion_prompt_msg_id = None
                                    conversion_session_store.set_choice(job.id, conv_id, "orig")
                            else:
                                conversion_session_store.set_choice(job.id, conv_id, "orig")
                            choice = conversion_session_store.get_choice(job.id, conv_id)

                        if choice == "mp3":
                            conversion_session_store.add_converted_file(job.id, f.name)

                            log.info("Converting/processing audio %s to MP3 for job %s", f.name, job.id)
                            output_name = f.stem + "_converted.mp3"
                            output_path = f.parent / output_name

                            conv_msg = await safe_send(
                                self.client,
                                chat_id,
                                compile_audio_conversion_running_status_text(job.id, f.name),
                                reply_markup=InlineKeyboardMarkup([
                                    [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
                                ])
                            )

                            job_state.is_converting = True
                            job_state.conversion_file = f.name
                            job_state.trigger_event.set()

                            try:
                                success = await convert_audio_async(f, output_path)
                            finally:
                                job_state.is_converting = False
                                job_state.conversion_file = None
                                job_state.trigger_event.set()

                            if conv_msg:
                                try:
                                    await self.client.delete_messages(chat_id, conv_msg.id)
                                except Exception:
                                    # expected: conversion message already deleted
                                    pass

                            if success:
                                log.info("Successfully converted audio %s to %s", f.name, output_name)
                                try:
                                    f.unlink(missing_ok=True)
                                except Exception:
                                    # expected: original audio file already unlinked
                                    pass
                                f = output_path
                                try:
                                    f_rel = str(f.relative_to(extract_dir))
                                except ValueError:
                                    f_rel = str(f.relative_to(dest_dir))

                                if conversion_prompt_msg_id:
                                    try:
                                        await self.client.delete_messages(chat_id, conversion_prompt_msg_id)
                                    except Exception:
                                        # expected: prompt message already deleted
                                        pass
                                break
                            else:
                                log.error("Failed to convert audio %s", f.name)
                                fail_msg = await safe_send(
                                    self.client,
                                    chat_id,
                                    compile_audio_conversion_failed_status_text(job.id, f.name)
                                )
                                async def delete_fail_msg(m):
                                    await asyncio.sleep(5)
                                    try:
                                        await self.client.delete_messages(chat_id, m.id)
                                    except Exception:
                                        # expected: fail message already deleted
                                        pass
                                if fail_msg:
                                    asyncio.create_task(delete_fail_msg(fail_msg))

                                conversion_session_store.set_choice(job.id, conv_id, "orig")

                        if choice == "orig" and conversion_prompt_msg_id:
                            try:
                                await self.client.delete_messages(chat_id, conversion_prompt_msg_id)
                            except Exception:
                                # expected: prompt message already deleted
                                pass

                from ..uploader import UploadTooLarge, handle_large_file, upload_file

                try:
                    split_parts = await handle_large_file(f, bool(job.split_large_files))
                except Exception as sle:
                    log.exception("Error while handling large file split for %s: %s", f.name, sle)
                    split_parts = [f]

                if not split_parts:
                    await self.store.mark_uploaded(job.id, f_rel)
                    break

                if len(split_parts) == 1 and split_parts[0] == f:
                    pass
                else:
                    for p in split_parts:
                        try:
                            p_rel = str(p.relative_to(extract_dir))
                        except ValueError:
                            p_rel = str(p.relative_to(dest_dir))
                        job_state.split_parts_created.add(p_rel)
                        job_state.split_parts_created.add(p.name)
                    await self.store.mark_uploaded(job.id, f_rel)
                    break

                job_state.uploading_files.add(f_rel)
                try:
                    await self.store.update_progress(job.id, status=JobStatus.UPLOADING)

                    last_uploaded_bytes = 0
                    last_upload_speed_time = time.time()

                    async def progress_cb(current, total):
                        nonlocal last_uploaded_bytes, last_upload_speed_time
                        job_state.current_upload_pct = (current / total) * 100 if total > 0 else 0.0
                        now = time.time()
                        elapsed = now - last_upload_speed_time
                        if elapsed >= 1.0:
                            uploaded_since_last = current - last_uploaded_bytes
                            job_state.upload_speed = max(0.0, uploaded_since_last / elapsed)
                            last_uploaded_bytes = current
                            last_upload_speed_time = now

                    job_state.current_upload_file = f.name
                    await upload_file(self.client, chat_id, f, progress=progress_cb)
                    await self.store.mark_uploaded(job.id, f_rel)

                    job_state.uploaded_filenames.add(f_rel)
                    job_state.sent += 1
                    await log_upload(job.id, f.name)
                    log.info("Successfully uploaded %s for job %s", f.name, job.id)

                    try:
                        if f.exists():
                            f_size = f.stat().st_size
                            f.unlink(missing_ok=True)
                            job_state.deleted_bytes += f_size
                    except Exception as de:
                        log.debug("Notice on post-upload file cleanup for %s: %s", f, de)

                except UploadTooLarge as e:
                    job_state.skipped.append((f.name, str(e)))
                    job_state.failed_uploads.add(f_rel)
                except Exception as e:
                    log.exception("Upload failed for %s", f)
                    job_state.skipped.append((f.name, f"error: {e}"))
                    job_state.failed_uploads.add(f_rel)
                finally:
                    job_state.current_upload_file = None
                    job_state.current_upload_pct = 0.0
                    job_state.upload_speed = 0.0
                    if f_rel in job_state.uploading_files:
                        job_state.uploading_files.remove(f_rel)

                await self.store.update_progress(job.id, sent_files=job_state.sent, skipped_files=len(job_state.skipped))
                job_state.trigger_event.set()

                # Decay delay multiplier slowly on successful upload
                self.upload_delay_multiplier = max(self.upload_delay_multiplier - 0.05, 1.0)

                job_state.session_uploaded_count += 1
                if job_state.session_uploaded_count % settings.tg_batch_size == 0:
                    await asyncio.sleep(settings.tg_batch_cooldown_s * self.upload_delay_multiplier)
                else:
                    delay = random.uniform(settings.tg_upload_delay_min, settings.tg_upload_delay_max) * self.upload_delay_multiplier
                    await asyncio.sleep(delay)

        async def run_uploader() -> None:
            from ..uploader import should_ignore_file
            from ..utils.media import (
                AUDIO_CONVERSION_EXT,
                CONVERSION_EXT,
                convert_audio_async,
                convert_media_async,
            )
            if is_torrent or has_archive_fmt:
                while not job_state.downloader_done.is_set():
                    await asyncio.sleep(2.0)

            if is_torrent:
                try:
                    if dest_dir.exists():
                        files = sorted(p for p in dest_dir.rglob("*") if p.is_file() and not should_ignore_file(p))
                        for f in files:
                            db_job = await self.store.get_job(job.id)
                            if not db_job or db_job.status == JobStatus.CANCELLED or job_state.uploader_done.is_set():
                                break

                            if f.suffix.lower() in CONVERSION_EXT:
                                job_state.is_converting = True
                                job_state.conversion_file = f.name
                                job_state.trigger_event.set()

                                output_path = f.with_suffix(".mkv")
                                if output_path.exists():
                                    output_path = f.with_name(f"{f.stem}_converted.mkv")

                                log.info("Converting incompatible torrent file %s to %s", f.name, output_path.name)
                                success = await convert_media_async(f, output_path)
                                if success:
                                    # Converter may have fallen back to MKV
                                    if not output_path.exists():
                                        mkv_path = output_path.with_suffix(".mkv")
                                        if mkv_path.exists():
                                            log.info("Converter produced MKV fallback: %s", mkv_path.name)
                                            output_path = mkv_path
                                    log.info("Successfully converted incompatible torrent file %s to %s", f.name, output_path.name)
                                    try:
                                        f.unlink(missing_ok=True)
                                    except Exception:
                                        pass
                                else:
                                    log.error("Failed to convert incompatible torrent file %s", f.name)
                            elif f.suffix.lower() in AUDIO_CONVERSION_EXT:
                                job_state.is_converting = True
                                job_state.conversion_file = f.name
                                job_state.trigger_event.set()

                                output_path = f.with_suffix(".mp3")
                                if output_path.exists():
                                    output_path = f.with_name(f"{f.stem}_converted.mp3")

                                log.info("Converting incompatible audio torrent file %s to %s", f.name, output_path.name)
                                success = await convert_audio_async(f, output_path)
                                if success:
                                    log.info("Successfully converted incompatible audio torrent file %s", f.name)
                                    try:
                                        f.unlink(missing_ok=True)
                                    except Exception:
                                        # expected: file already unlinked
                                        pass
                                else:
                                    log.error("Failed to convert incompatible audio torrent file %s", f.name)
                except Exception as ce:
                    log.exception("Error during torrent media conversion: %s", ce)
                finally:
                    job_state.is_converting = False
                    job_state.conversion_file = None
                    job_state.trigger_event.set()
            else:
                while not job_state.downloader_done.is_set():
                    if job_state.uploader_done.is_set():
                        break
                    has_completed_file = False
                    if dest_dir.exists():
                        try:
                            files = [p for p in dest_dir.rglob("*") if p.is_file() and not p.name.endswith(".part") and not should_ignore_file(p)]
                            if files:
                                snapshots = {}
                                for f in files:
                                    try:
                                        snapshots[f] = f.stat().st_size
                                    except Exception:
                                        # expected: file unlinked or moved during scan
                                        pass
                                await asyncio.sleep(0.5)
                                for f, sz1 in snapshots.items():
                                    try:
                                        if f.exists() and sz1 > 0 and f.stat().st_size == sz1:
                                            has_completed_file = True
                                            break
                                    except Exception:
                                        # expected: file unlinked or moved during check
                                        pass
                        except Exception as e:
                            log.debug("Notice on download completed file checker loop: %s", e)
                    if has_completed_file:
                        break
                    await asyncio.sleep(2.0)

            while True:
                if job_state.uploader_done.is_set():
                    break
                await perform_uploads()

                if job_state.downloader_done.is_set():
                    has_pending = False
                    if dest_dir.exists():
                        try:
                            files = [p for p in dest_dir.rglob("*") if p.is_file() and not p.name.endswith(".part") and not should_ignore_file(p)]
                            pending = [
                                f for f in files
                                if str(f.relative_to(dest_dir)) not in job_state.uploaded_filenames
                                and str(f.relative_to(dest_dir)) not in job_state.uploading_files
                                and str(f.relative_to(dest_dir)) not in job_state.failed_uploads
                            ]
                            if pending:
                                has_pending = True
                        except Exception as e:
                            log.debug("Notice checking pending files in dest_dir for job #%s: %s", job.id, e)
                    if extract_dir.exists():
                        try:
                            files = [p for p in extract_dir.rglob("*") if p.is_file() and not p.name.endswith(".part") and not should_ignore_file(p)]
                            pending = [
                                f for f in files
                                if str(f.relative_to(extract_dir)) not in job_state.uploaded_filenames
                                and str(f.relative_to(extract_dir)) not in job_state.uploading_files
                                and str(f.relative_to(extract_dir)) not in job_state.failed_uploads
                            ]
                            if pending:
                                has_pending = True
                        except Exception as e:
                            log.debug("Notice checking pending files in extract_dir for job #%s: %s", job.id, e)
                    if not has_pending:
                        break

                try:
                    await asyncio.wait_for(job_state.trigger_event.wait(), timeout=5.0)
                    job_state.trigger_event.clear()
                except TimeoutError:
                    # expected: timeout waiting for upload trigger event tick
                    pass

        updater_task = asyncio.create_task(status_updater_loop())
        try:
            await run_uploader()

            dl_res = getattr(job_state, "downloader_result", None)

            if dl_res and not dl_res.ok and job_state.sent == 0:
                await self.store.update_progress(
                    job.id, status=JobStatus.FAILED, error=dl_res.error_tail[-1500:], url=""
                )
                await report(
                    f"Download failed after {dl_res.attempts} attempt(s) and produced no files.\n"
                    f"Last output:\n```\n{dl_res.error_tail[-800:]}\n```"
                )
                return

            await self.store.update_progress(job.id, status=JobStatus.DONE, sent_files=job_state.sent, skipped_files=len(job_state.skipped), url="")

            summary = f"Done. Uploaded {job_state.sent} file(s) total."
            if dl_res and not dl_res.ok:
                summary = (
                    f"Completed with some errors. Uploaded {job_state.sent} file(s) total.\n\n"
                    f"**Error tail:**\n"
                    f"```\n{dl_res.error_tail[-600:]}\n```"
                )
            if job_state.skipped:
                preview = "\n".join(f"- {n} ({info})" for n, info in job_state.skipped[:20])
                more = f"\n…and {len(job_state.skipped) - 20} more" if len(job_state.skipped) > 20 else ""
                summary += f"\nSkipped:\n{preview}{more}"

            if getattr(job_state, "pixeldrain_links", None):
                link_lines = [f"• `{fname}`: {url}" for fname, url in job_state.pixeldrain_links]
                summary += "\n\n**Pixeldrain Mirror Links:**\n" + "\n".join(link_lines)

            if not (is_mirror_job and not upload_tg):
                await report(summary)

            shutil.rmtree(dest_dir, ignore_errors=True)
            shutil.rmtree(extract_dir, ignore_errors=True)

        except Exception as e:
            log.exception("Upload process failed for job #%s", job.id)
            await self.store.update_progress(job.id, status=JobStatus.FAILED, error=str(e), url="")
            await report(f"Job failed with error: {e}")
            shutil.rmtree(dest_dir, ignore_errors=True)
            shutil.rmtree(extract_dir, ignore_errors=True)
        finally:
            shutil.rmtree(extract_dir, ignore_errors=True)
            job_state.uploader_done.set()
            updater_task.cancel()
            await asyncio.gather(updater_task, return_exceptions=True)

            # Edit the status message one final time with force=True to ensure final state is updated
            db_job = await self.store.get_job(job.id)
            if db_job and job_state.msg_id:
                final_text = compile_job_status_text(db_job, job_state)
                await safe_edit(self.client, chat_id, job_state.msg_id, final_text, reply_markup=None, force=True)

            from ..handlers.conversion_state import conversion_session_store
            from ..utils.archive import archive_session_store
            from .status.messaging import _last_edit_times

            archive_session_store.pop_job(job.id)
            conversion_session_store.pop_job(job.id)
            _password_prompt_events.pop(job.id, None)
            to_remove = [mid for mid, info in _password_prompt_messages.items() if info[0] == job.id]
            for mid in to_remove:
                _password_prompt_messages.pop(mid, None)
            if job_state.msg_id:
                _last_edit_times.pop((chat_id, job_state.msg_id), None)
            self.jobs.pop(job.id, None)


async def safe_send(client: Client, chat_id: int, text: str, **kwargs) -> Message | None:
    from pyrogram.errors import FloodWait
    from pyrogram.types import LinkPreviewOptions
    kwargs.setdefault("link_preview_options", LinkPreviewOptions(is_disabled=True))
    for _ in range(3):
        try:
            return await client.send_message(chat_id, text, **kwargs)
        except FloodWait as e:
            log.warning("Telegram FloodWait: waiting %s seconds on send", e.value)
            await asyncio.sleep(e.value + 1)
        except Exception as e:
            log.warning("Failed to send message: %s", e)
            return None
    return None


async def log_upload(job_id: str, filename: str) -> None:
    log_path = settings.log_dir / "uploads.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)

    def append_to_file():
        with open(log_path, "a", encoding="utf-8") as f:
            f.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} - Job #{job_id} - Uploaded: {filename}\n")

    await asyncio.to_thread(append_to_file)


async def cleanup_orphaned_directories() -> None:
    """Scan downloads directory and delete any directories job_{id} that are
    not active, queued, or waiting in the database."""
    if not settings.downloads_dir.exists():
        return

    try:
        cur = await store.db.execute(
            "SELECT id FROM jobs WHERE status IN ('queued', 'downloading', 'uploading', 'waiting')"
        )
        rows = await cur.fetchall()
        keep_ids = {f"job_{r['id']}" for r in rows}

        def run_cleanup():
            import shutil
            for p in settings.downloads_dir.iterdir():
                if p.is_dir() and p.name.startswith("job_"):
                    if p.name not in keep_ids:
                        log.info("Cleaning up orphaned directory: %s", p)
                        shutil.rmtree(p, ignore_errors=True)

        await asyncio.to_thread(run_cleanup)
    except Exception:
        log.exception("Error during orphaned directories cleanup")


queue_manager = QueueManager()
