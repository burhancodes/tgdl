from __future__ import annotations

import asyncio
import json
import logging
import re
import shutil
import sys
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from ...config import settings
from ...pacing import Backoff, looks_rate_limited, parse_speed_limit

log = logging.getLogger(__name__)


class CyberdropDLNotFound(RuntimeError):
    pass


@dataclass
class DownloadResult:
    ok: bool
    files: list[Path] = field(default_factory=list)
    error_tail: str = ""
    attempts: int = 0


def get_user_cdl_config_path(user_id: int | str) -> Path:
    custom_yaml = settings.auth_dir / str(user_id) / "config.yaml"
    if custom_yaml.exists() and custom_yaml.is_file():
        return custom_yaml
    return settings.auth_dir / str(user_id) / "cyberdrop-dl.yaml"


def get_cdl_config_path(user_id: int | str | None = None) -> Path | None:
    if user_id:
        user_conf = get_user_cdl_config_path(user_id)
        if user_conf.exists() and user_conf.is_file():
            return user_conf
    pkg_conf = Path(__file__).parent / "config.yaml"
    if pkg_conf.exists() and pkg_conf.is_file():
        return pkg_conf
    if settings.cdl_config_path.exists() and settings.cdl_config_path.is_file():
        return settings.cdl_config_path
    auth_global_yaml = settings.auth_dir / "cyberdrop-dl.yaml"
    if auth_global_yaml.exists() and auth_global_yaml.is_file():
        return auth_global_yaml
    auth_global_conf = settings.auth_dir / "config.yaml"
    if auth_global_conf.exists() and auth_global_conf.is_file():
        return auth_global_conf
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


def _find_cdl_binary() -> list[str]:
    which_cdl = shutil.which("cyberdrop-dl")
    if which_cdl:
        return [which_cdl]

    venv_cdl = Path(sys.prefix) / "bin" / "cyberdrop-dl"
    if venv_cdl.exists() and venv_cdl.is_file():
        return [str(venv_cdl)]

    try:
        import cyberdrop_dl  # noqa: F401
        return [sys.executable, "-m", "cyberdrop_dl"]
    except ImportError:
        return []


def _build_cmd(
    urls: list[str],
    dest_dir: Path,
    extra_args: list[str] | None = None,
    config_path: Path | None = None,
    user_id: int | str | None = None,
) -> list[str]:
    conf = config_path or get_cdl_config_path(user_id=user_id)
    cookies = get_cookies_path(user_id=user_id)

    cmd = _find_cdl_binary() + [
        "download",
        "--ui", "disabled",
        "--no-stats",
        "--min-free-space", "0",
        "--ignore-history",
        "--no-mtime",
        "--download-folder", str(dest_dir.absolute()),
        "--attempts", str(settings.cdl_retries),
        "--database-file", str(settings.cdl_archive_path.absolute()),
    ]

    if conf and conf.exists():
        cmd.extend(["--config-file", str(conf.absolute())])
    if cookies and cookies.exists():
        cmd.extend(["--cookies", str(cookies.absolute())])

    if settings.global_download_speed_limit and str(settings.global_download_speed_limit).strip().lower() not in ("none", "0", ""):
        parsed_speed = parse_speed_limit(settings.global_download_speed_limit)
        if parsed_speed and parsed_speed > 0:
            cmd.extend(["--speed-limit", str(parsed_speed)])

    if extra_args:
        cmd.extend(extra_args)

    cmd.extend(urls)
    return cmd


_FILE_LOCK_PATTERN = re.compile(r"Lock for '([^']+)' acquired", re.IGNORECASE)
_DOWNLOAD_COMPLETE_PATTERN = re.compile(r"(?:Download Complete|Completed):\s*([^\n\r]+)", re.IGNORECASE)
_FAILED_COUNT_PATTERN = re.compile(r"Failed:\s*(\d+)\s*files", re.IGNORECASE)
_DOWNLOADED_COUNT_PATTERN = re.compile(r"Downloaded:\s*(\d+)\s*files", re.IGNORECASE)


async def run_with_progress(
    url: str | list[str],
    dest_dir: Path,
    on_progress: Callable[[int, str | None, str | None], None] | None = None,
    extra_args: list[str] | None = None,
    register_proc: Callable[[asyncio.subprocess.Process | None], None] | None = None,
    user_id: int | str | None = None,
    config_path: Path | None = None,
) -> DownloadResult:
    binary_cmd = _find_cdl_binary()
    if not binary_cmd or (len(binary_cmd) == 1 and not shutil.which(binary_cmd[0]) and not Path(binary_cmd[0]).exists()):
        raise CyberdropDLNotFound(
            "cyberdrop-dl not found on PATH. Install with: uv add cyberdrop-dl-patched"
        )

    urls: list[str] = []
    if isinstance(url, list):
        for u in url:
            s = str(u).strip()
            if s.startswith("cdl:"):
                s = s[len("cdl:"):].strip()
            elif s.startswith("cyberdrop-dl:"):
                s = s[len("cyberdrop-dl:"):].strip()
            if s:
                urls.append(s)
    elif isinstance(url, str):
        raw = url.strip()
        if raw.startswith("cdl:"):
            raw = raw[len("cdl:"):].strip()
        elif raw.startswith("cyberdrop-dl:"):
            raw = raw[len("cyberdrop-dl:"):].strip()

        try:
            parsed = json.loads(raw)
            if isinstance(parsed, list):
                for u in parsed:
                    s = str(u).strip()
                    if s.startswith("cdl:"):
                        s = s[len("cdl:"):].strip()
                    elif s.startswith("cyberdrop-dl:"):
                        s = s[len("cyberdrop-dl:"):].strip()
                    if s:
                        urls.append(s)
            elif isinstance(parsed, str):
                urls = [u.strip() for u in parsed.split() if u.strip().startswith(("http://", "https://"))]
        except Exception as e:
            log.debug("Failed parsing JSON url string in cyberdrop_dl: %s", e)

        if not urls:
            urls = [u.strip() for u in raw.split() if u.strip().startswith(("http://", "https://"))]

        if not urls:
            urls = [raw]

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
                log.debug("Progress callback error in cyberdrop_dl loop: %s", e)

        backoff = Backoff(
            base_s=settings.cdl_backoff_base_s,
            multiplier=settings.cdl_backoff_multiplier,
            max_attempts=settings.cdl_max_run_retries,
        )

        while True:
            attempts += 1
            cmd = _build_cmd(
                [single_url],
                dest_dir,
                extra_args=extra_args,
                config_path=config_path,
                user_id=user_id,
            )
            log.info(
                "cyberdrop-dl run url %s/%s attempt=%s url=%s args=%s user_id=%s",
                idx, total_urls, attempts, single_url, extra_args, user_id,
            )

            try:
                proc = await asyncio.create_subprocess_exec(
                    *cmd, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE
                )
            except FileNotFoundError:
                raise CyberdropDLNotFound("cyberdrop-dl executable not found on the system.")

            if register_proc:
                register_proc(proc)

            count = 0
            stdout_buf: list[str] = []
            stderr_buf: list[str] = []

            async def pump_stdout():
                nonlocal count
                assert proc.stdout is not None
                async for line in proc.stdout:
                    text = line.decode(errors="replace").strip()
                    if not text:
                        continue
                    stdout_buf.append(text)
                    if len(stdout_buf) > 300:
                        stdout_buf.pop(0)

                    filename = None
                    m_lock = _FILE_LOCK_PATTERN.search(text)
                    if m_lock:
                        filename = m_lock.group(1).strip()

                    m_done = _DOWNLOAD_COMPLETE_PATTERN.search(text)
                    if m_done:
                        filename = m_done.group(1).strip()
                        count += 1

                    if "Download attempt" in text or "Downloading" in text or filename:
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
                    if len(stderr_buf) > 200:
                        stderr_buf.pop(0)

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

            combined_output = "\n".join(stdout_buf) + "\n" + "".join(stderr_buf)
            last_stderr = "".join(stderr_buf)[-3000:] or combined_output[-3000:]

            # Scan destination directory to check if files were downloaded
            cur_files = [p for p in dest_dir.rglob("*") if p.is_file()]

            # Determine success based on exit code, files downloaded, or output stats
            m_downloaded = _DOWNLOADED_COUNT_PATTERN.search(combined_output)
            m_failed = _FAILED_COUNT_PATTERN.search(combined_output)
            files_downloaded_stat = int(m_downloaded.group(1)) if m_downloaded else len(cur_files)
            files_failed_stat = int(m_failed.group(1)) if m_failed else 0

            url_success = (returncode == 0 and (len(cur_files) > 0 or files_downloaded_stat > 0) and files_failed_stat == 0)
            if not url_success and len(cur_files) > 0 and returncode == 0:
                url_success = True

            if url_success:
                success_count += 1
                total_download_count += max(count, files_downloaded_stat, len(cur_files))
                if on_progress:
                    try:
                        on_progress(total_download_count, None, single_url)
                    except Exception:
                        pass
                break

            rate_limited = looks_rate_limited(last_stderr)
            if not rate_limited or backoff.exhausted:
                log.error("cyberdrop-dl failed for URL %s: %s", single_url, last_stderr)
                break

            delay = backoff.next_delay()
            log.warning(
                "cyberdrop-dl looks rate-limited (attempt %s), backing off %.0fs before retry",
                attempts, delay,
            )
            await asyncio.sleep(delay)
            last_stderr = ""

    files = sorted(p for p in dest_dir.rglob("*") if p.is_file())
    ok = (success_count == total_urls and len(files) > 0) or (len(files) > 0)
    return DownloadResult(ok=ok, files=files, error_tail=last_stderr, attempts=attempts)
