from __future__ import annotations

import asyncio
import json
import logging
import shutil
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from ...config import settings
from ...pacing import Backoff, looks_rate_limited

log = logging.getLogger(__name__)


class GalleryDLNotFound(RuntimeError):
    pass


@dataclass
class DownloadResult:
    ok: bool
    files: list[Path] = field(default_factory=list)
    error_tail: str = ""
    attempts: int = 0


def get_user_gdl_config_path(user_id: int | str) -> Path:
    return settings.auth_dir / str(user_id) / "gallery-dl.conf"


def get_gdl_config_path(user_id: int | str | None = None) -> Path | None:
    if user_id:
        user_conf = get_user_gdl_config_path(user_id)
        if user_conf.exists() and user_conf.is_file():
            return user_conf
    pkg_conf = Path(__file__).parent / "gallery-dl.conf"
    if pkg_conf.exists() and pkg_conf.is_file():
        return pkg_conf
    if settings.gdl_config_path.exists() and settings.gdl_config_path.is_file():
        return settings.gdl_config_path
    auth_global = settings.auth_dir / "gallery-dl.conf"
    if auth_global.exists() and auth_global.is_file():
        return auth_global
    return None


def get_user_cookies_path(user_id: int | str) -> Path:
    return settings.auth_dir / str(user_id) / "cookies.txt"


def get_cookies_path(user_id: int | str | None = None) -> Path | None:
    if user_id:
        user_cookies = get_user_cookies_path(user_id)
        if user_cookies.exists() and user_cookies.is_file():
            return user_cookies
    auth_global_cookies = settings.auth_dir / "cookies.txt"
    if auth_global_cookies.exists() and auth_global_cookies.is_file():
        return auth_global_cookies
    root_cookies = Path("./cookies.txt")
    if root_cookies.exists() and root_cookies.is_file():
        return root_cookies
    return None


def _build_cmd(
    urls: list[str],
    dest_dir: Path,
    extra_args: list[str] | None = None,
    links_file: Path | None = None,
    config_path: Path | None = None,
    user_id: int | str | None = None,
) -> list[str]:
    conf = config_path or get_gdl_config_path(user_id=user_id)
    cookies = get_cookies_path(user_id=user_id)

    cmd = ["gallery-dl"]
    if conf and conf.exists():
        cmd.extend(["--config", str(conf.absolute())])
    if cookies and cookies.exists():
        cmd.extend(["--cookies", str(cookies.absolute())])

    cmd.extend([
        "--no-mtime",
        "-D", str(dest_dir),
        "--sleep", f"{settings.gdl_sleep_min}-{settings.gdl_sleep_max}",
        "--sleep-request", settings.gdl_sleep_request,
        "--retries", str(settings.gdl_retries),
        "-v",
    ])
    if settings.global_download_speed_limit and str(settings.global_download_speed_limit).strip().lower() not in ("none", "0", ""):
        cmd.extend(["--limit-rate", str(settings.global_download_speed_limit)])
    if extra_args:
        cmd.extend(extra_args)
    if links_file:
        cmd.extend(["-i", str(links_file)])
    else:
        cmd.extend(urls)
    return cmd


async def _stream_run(cmd: list[str]) -> tuple[int, str, Callable[[], int]]:
    proc = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    count = 0
    stderr_chunks: list[str] = []

    async def read_stdout():
        nonlocal count
        assert proc.stdout is not None
        async for line in proc.stdout:
            text = line.decode(errors="replace")
            if text.strip():
                count += 1

    async def read_stderr():
        assert proc.stderr is not None
        async for line in proc.stderr:
            stderr_chunks.append(line.decode(errors="replace"))
            if len(stderr_chunks) > 200:
                stderr_chunks.pop(0)

    try:
        await asyncio.gather(read_stdout(), read_stderr())
        returncode = await proc.wait()
        return returncode, "".join(stderr_chunks), (lambda: count)
    except asyncio.CancelledError:
        try:
            proc.terminate()
            await proc.wait()
        except Exception:
            # expected: process already terminated
            pass
        raise


async def run_with_progress(
    url: str | list[str],
    dest_dir: Path,
    on_progress: Callable[[int, str | None, str | None], None] | None = None,
    extra_args: list[str] | None = None,
    register_proc: Callable[[asyncio.subprocess.Process | None], None] | None = None,
    user_id: int | str | None = None,
    config_path: Path | None = None,
    fallback_cdl: bool = True,
) -> DownloadResult:

    if shutil.which("gallery-dl") is None:
        raise GalleryDLNotFound(
            "gallery-dl not found on PATH. Install with: "
            "uv add gallery-dl"
        )

    urls = []
    if isinstance(url, list):
        urls = [str(u).strip() for u in url if str(u).strip()]
    elif isinstance(url, str):
        try:
            parsed = json.loads(url)
            if isinstance(parsed, list):
                urls = [str(u).strip() for u in parsed if str(u).strip()]
            elif isinstance(parsed, str):
                urls = [u.strip() for u in parsed.split() if u.strip().startswith(("http://", "https://"))]
        except Exception as e:
            log.debug("Failed parsing JSON url string in gallery_dl: %s", e)

        if not urls:
            urls = [u.strip() for u in url.split() if u.strip().startswith(("http://", "https://"))]

        if not urls:
            urls = [url.strip()]

    dest_dir.mkdir(parents=True, exist_ok=True)
    attempts = 0
    last_stderr = ""
    success_count = 0
    total_urls = len(urls)
    total_download_count = 0

    for idx, single_url in enumerate(urls, 1):
        if on_progress:
            try:
                on_progress(total_download_count, None, single_url)
            except Exception as e:
                log.debug("Progress callback error in gallery_dl loop: %s", e)

        files_before = set(p for p in dest_dir.rglob("*") if p.is_file())
        attempts += 1
        cmd = _build_cmd(
            [single_url],
            dest_dir,
            extra_args,
            links_file=None,
            config_path=config_path,
            user_id=user_id,
        )
        log.info("gallery-dl run url %s/%s attempt=%s url=%s args=%s user_id=%s", idx, total_urls, attempts, single_url, extra_args, user_id)

        proc = await asyncio.create_subprocess_exec(
            *cmd, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE
        )
        if register_proc:
            register_proc(proc)

        count = 0
        stderr_buf: list[str] = []

        async def pump_stdout():
            nonlocal count
            assert proc.stdout is not None
            async for line in proc.stdout:
                text = line.decode(errors="replace").strip()
                if not text:
                    continue

                count += 1
                filename = None
                parts = text.split()
                if parts:
                    last_part = parts[-1].strip("'\"")
                    if "/" in last_part or "\\" in last_part or "." in last_part:
                        try:
                            filename = Path(last_part).name
                        except Exception:
                            # expected: last_part is not a valid path
                            pass

                if on_progress:
                    try:
                        on_progress(total_download_count + count, filename, single_url)
                    except TypeError:
                        try:
                            on_progress(total_download_count + count, filename)
                        except TypeError:
                            on_progress(total_download_count + count)

        async def pump_stderr():
            assert proc.stderr is not None
            async for line in proc.stderr:
                stderr_buf.append(line.decode(errors="replace"))

        try:
            await asyncio.gather(pump_stdout(), pump_stderr())
            returncode = await proc.wait()
        except asyncio.CancelledError:
            try:
                proc.kill()
                await proc.wait()
            except Exception:
                pass
            raise
        finally:
            if register_proc:
                register_proc(None)

        last_stderr = last_stderr or "".join(stderr_buf)[-3000:]
        files_after = set(p for p in dest_dir.rglob("*") if p.is_file())
        new_files = files_after - files_before

        cur_files = [p for p in dest_dir.rglob("*") if p.is_file()]
        if returncode == 0 and (len(new_files) > 0 or len(cur_files) > 0):
            success_count += 1
            total_download_count += max(count, len(new_files), len(cur_files))
            if on_progress:
                try:
                    on_progress(total_download_count, None, single_url)
                except Exception:
                    pass
        else:
            log.info("gallery-dl failed or produced 0 files for URL %s (code=%s).", single_url, returncode)
            fallback_handled = False
            if fallback_cdl:
                log.info("Immediately passing failed URL %s to cyberdrop-dl...", single_url)
                try:
                    from ..cyberdrop_dl import run_with_progress as run_cdl_progress

                    def on_cdl_item_progress(cdl_count, cdl_filename=None, cdl_url=None):
                        if on_progress:
                            try:
                                on_progress(total_download_count + cdl_count, cdl_filename, single_url)
                            except Exception:
                                pass

                    cdl_res = await run_cdl_progress(
                        single_url,
                        dest_dir,
                        on_progress=on_cdl_item_progress,
                        extra_args=extra_args,
                        register_proc=register_proc,
                        user_id=user_id,
                        fallback_gdl=False,
                    )
                    cdl_files_after = set(p for p in dest_dir.rglob("*") if p.is_file())
                    cdl_new_files = cdl_files_after - files_before
                    if cdl_res.ok and (len(cdl_new_files) > 0 or len(cdl_files_after) > 0):
                        success_count += 1
                        total_download_count += max(len(cdl_new_files), len(cdl_res.files))
                        fallback_handled = True
                        if on_progress:
                            try:
                                on_progress(total_download_count, None, single_url)
                            except Exception:
                                pass
                except Exception as cdl_err:
                    log.warning("cyberdrop-dl fallback error for URL %s: %s", single_url, cdl_err)

            if not fallback_handled:
                log.info("Attempting DirectDownloader fallback for URL %s...", single_url)
                from ..direct import download_direct
                try:
                    async def on_direct_prog(current_bytes, total_bytes, filename, direct_u=None):
                        if on_progress:
                            try:
                                on_progress(total_download_count + 1, filename, single_url)
                            except Exception:
                                pass

                    direct_paths = await download_direct(single_url, dest_dir, progress_cb=on_direct_prog)
                    if direct_paths:
                        success_count += 1
                        total_download_count += len(direct_paths)
                        if on_progress:
                            try:
                                on_progress(total_download_count, None, single_url)
                            except Exception:
                                pass
                except Exception as de:
                    log.warning("DirectDownloader fallback also failed for URL %s: %s", single_url, de)

    files = sorted(p for p in dest_dir.rglob("*") if p.is_file())
    ok = (success_count == total_urls and len(files) > 0) or (len(files) > 0)
    return DownloadResult(ok=ok, files=files, error_tail=last_stderr, attempts=attempts)
