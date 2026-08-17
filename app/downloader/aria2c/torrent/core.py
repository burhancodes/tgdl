from __future__ import annotations

import asyncio
import base64
import json
import logging
import secrets
import shutil
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from app.config import settings

log = logging.getLogger(__name__)

ARIA2_PORT: int | None = None
ARIA2_PROC: asyncio.subprocess.Process | None = None
ARIA2_SECRET: str | None = None


@dataclass
class DownloadResult:
    ok: bool
    files: list[Path] = field(default_factory=list)
    error_tail: str = ""
    attempts: int = 1


class Aria2DownloadTask:
    """Wrapper to allow canceling active aria2 RPC downloads via task cancellation interface."""
    def __init__(self, port: int, gid: str):
        self.port = port
        self.gid = gid

    def kill(self) -> None:
        """Issues forceRemove call to aria2 RPC daemon for active GID."""
        try:
            sync_rpc_call(self.port, "aria2.forceRemove", [self.gid])
        except Exception as e:
            log.warning("Failed to issue forceRemove for GID %s: %s", self.gid, e)


def get_free_port() -> int:
    import socket
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def sync_rpc_call(port: int, method: str, params: list[Any]) -> dict[str, Any]:
    global ARIA2_SECRET
    url = f"http://127.0.0.1:{port}/jsonrpc"
    p = list(params)
    if ARIA2_SECRET:
        p.insert(0, f"token:{ARIA2_SECRET}")
    payload = {
        "jsonrpc": "2.0",
        "id": "tgdl",
        "method": method,
        "params": p,
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


async def async_rpc_call(port: int, method: str, params: list[Any]) -> dict[str, Any]:
    return await asyncio.to_thread(sync_rpc_call, port, method, params)


from .trackers import add_trackers_to_magnet, fetch_latest_trackers, get_tracker_string


async def start_aria2_daemon() -> None:
    """Launch the global aria2c RPC daemon with live trackers from ngosang/trackerslist."""
    global ARIA2_PORT, ARIA2_PROC, ARIA2_SECRET
    if ARIA2_PROC is not None:
        if settings.global_download_speed_limit and str(settings.global_download_speed_limit).strip().lower() not in ("none", "0", ""):
            limit_val = str(settings.global_download_speed_limit)
        else:
            limit_val = "0"
        try:
            await async_rpc_call(ARIA2_PORT, "aria2.changeGlobalOption", [{"max-overall-download-limit": limit_val}])
        except Exception as e:
            log.debug("Failed to update aria2c global download speed limit: %s", e)
        return  # Already running

    if shutil.which("aria2c") is None:
        log.warning("aria2c is not installed. Torrent downloads will fail.")
        return

    # Dynamically fetch latest trackers list asynchronously
    await fetch_latest_trackers()

    port = get_free_port()
    ARIA2_SECRET = secrets.token_urlsafe(32)
    tracker_str = get_tracker_string()
    tracker_arg = f"--bt-tracker={tracker_str}"

    log_dir = settings.log_dir
    log_dir.mkdir(parents=True, exist_ok=True)
    log_file = log_dir / "aria2c_daemon.log"

    cmd = [
        "aria2c",
        "--enable-rpc",
        "--rpc-listen-all=false",
        f"--rpc-listen-port={port}",
        f"--rpc-secret={ARIA2_SECRET}",
        f"--log={log_file}",
        "--log-level=notice",
        "--seed-time=0",
        "--seed-ratio=0.0",
        "--bt-tracker-connect-timeout=10",
        "--bt-tracker-timeout=10",
        "--enable-dht=true",
        "--bt-enable-lpd=true",
        "--enable-peer-exchange=true",
        "--bt-max-peers=120",
        "--max-overall-upload-limit=50K",
        "--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
        tracker_arg
    ]
    if getattr(settings, "force_ipv6", False) or getattr(settings, "source_address", None):
        cmd.extend([
            "--disable-ipv6=false",
            "--enable-dht6=true",
            "--async-dns=true",
        ])
        src_addr = getattr(settings, "source_address", None) or "::"
        cmd.append(f"--interface={src_addr}")

    if settings.global_download_speed_limit and str(settings.global_download_speed_limit).strip().lower() not in ("none", "0", ""):
        cmd.append(f"--max-overall-download-limit={settings.global_download_speed_limit}")

    log.info("Launching global aria2c RPC daemon on port %s...", port)
    try:
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL
        )
        ARIA2_PORT = port
        ARIA2_PROC = proc
        await asyncio.sleep(0.5)
    except Exception as e:
        log.exception("Failed to start global aria2c RPC daemon: %s", e)


async def stop_aria2_daemon() -> None:
    """Shutdown global aria2c daemon process."""
    global ARIA2_PORT, ARIA2_PROC, ARIA2_SECRET
    if ARIA2_PROC is not None:
        try:
            if ARIA2_PORT:
                await async_rpc_call(ARIA2_PORT, "aria2.shutdown", [])
        except Exception:
            pass
        try:
            ARIA2_PROC.terminate()
            await ARIA2_PROC.wait()
        except Exception:
            pass
    ARIA2_PROC = None
    ARIA2_PORT = None
    ARIA2_SECRET = None


async def download_via_aria2_async(
    target: str,
    dest_dir: Path,
    options: dict[str, Any] | None = None,
    on_progress: Callable[..., None] | None = None,
    register_proc: Callable[[Any], None] | None = None,
) -> DownloadResult:
    """General-purpose downloader using global aria2c RPC daemon supporting HTTP/HTTPS/FTP/magnet/torrent."""
    global ARIA2_PORT, ARIA2_PROC
    if ARIA2_PROC is None or ARIA2_PORT is None:
        await start_aria2_daemon()
        if ARIA2_PROC is None or ARIA2_PORT is None:
            return DownloadResult(ok=False, error_tail="aria2c RPC daemon is not running.")

    port = ARIA2_PORT
    proc = ARIA2_PROC

    is_torrent_file = (target.startswith("torrent:") or target.endswith(".torrent")) and not target.startswith(("http://", "https://", "magnet:", "ftp://"))
    is_magnet = target.startswith("magnet:") or "magnet:?xt=" in target
    is_bittorrent = is_torrent_file or is_magnet

    rpc_options: dict[str, Any] = {"dir": str(dest_dir)}
    if is_bittorrent:
        rpc_options["bt-tracker"] = get_tracker_string()

    if options:
        rpc_options.update(options)

    gid = None
    try:
        if is_torrent_file:
            torrent_path_str = target.removeprefix("torrent:")
            torrent_path = Path(torrent_path_str)
            if not torrent_path.exists():
                raise FileNotFoundError(f"Torrent file not found: {torrent_path}")
            with open(torrent_path, "rb") as f:
                b64_content = base64.b64encode(f.read()).decode("utf-8")
            response = await async_rpc_call(port, "aria2.addTorrent", [b64_content, [], rpc_options])
        else:
            target_clean = target.removeprefix("torrent:")
            if is_magnet and target_clean.startswith("magnet:"):
                target_clean = add_trackers_to_magnet(target_clean)

            response = await async_rpc_call(port, "aria2.addUri", [[target_clean], rpc_options])

        if "error" in response:
            raise Exception(response["error"].get("message", "unknown error"))

        gid = response.get("result")
    except Exception as e:
        log.exception("Failed to add download to aria2c RPC daemon")
        return DownloadResult(ok=False, error_tail=f"Failed to add download to daemon: {e}")

    if not gid:
        return DownloadResult(ok=False, error_tail="aria2c daemon did not return a GID.")

    task_wrapper = Aria2DownloadTask(port, gid)
    if register_proc:
        register_proc(task_wrapper)

    ok = False
    error_tail = ""
    start_time = asyncio.get_event_loop().time()
    last_active_time = start_time
    try:
        while True:
            if proc.returncode is not None:
                log.error("Global aria2c daemon stopped unexpectedly with code %s", proc.returncode)
                break

            try:
                response = await async_rpc_call(port, "aria2.tellStatus", [gid])
                if "error" in response:
                    raise Exception(response["error"].get("message", "unknown error"))
                result = response.get("result", {})
            except Exception as e:
                log.error("Failed to query status from aria2c daemon (GID %s): %s", gid, e)
                await asyncio.sleep(2.0)
                continue

            followed_by = result.get("followedBy")
            if followed_by and len(followed_by) > 0:
                log.info("Download transitioned to new GID: %s -> %s", gid, followed_by[0])
                gid = followed_by[0]
                task_wrapper.gid = gid
                await asyncio.sleep(1.0)
                continue

            status = result.get("status")
            if status == "complete":
                ok = True
                break
            elif status == "error":
                ok = False
                error_code = result.get("errorCode", "unknown")
                error_msg = result.get("errorMessage", "unknown error")
                error_tail = f"Aria2 error code {error_code}: {error_msg}"
                break

            completed_len = float(result.get("completedLength", 0))
            total_len = float(result.get("totalLength", 0))
            speed = float(result.get("downloadSpeed", 0))
            seeders = int(result.get("numSeeders", 0))
            connections = int(result.get("connections", 0))

            pct = (completed_len * 100.0 / total_len) if total_len > 0 else 0.0

            now = asyncio.get_event_loop().time()
            if is_bittorrent:
                if completed_len > 0 or speed > 0 or seeders > 0:
                    last_active_time = now
                elif now - last_active_time > 300.0:
                    raise Exception("Torrent is dead (stuck at 0% with no active seeders/peers).")

            torrent_name = None
            bt = result.get("bittorrent", {})
            info = bt.get("info", {})
            if info.get("name"):
                torrent_name = info["name"]
            else:
                files = result.get("files", [])
                if files and files[0].get("path"):
                    p = Path(files[0]["path"])
                    if p.name:
                        torrent_name = p.name

            if on_progress:
                try:
                    on_progress(pct, completed_len, speed, seeders, connections, torrent_name)
                except TypeError:
                    try:
                        on_progress(pct, completed_len, speed, torrent_name)
                    except TypeError:
                        on_progress(pct, completed_len, speed)

            await asyncio.sleep(2.0)

    except Exception as e:
        log.exception("Exception in progress monitoring loop")
        error_tail = str(e)
    finally:
        try:
            await async_rpc_call(port, "aria2.removeDownloadResult", [gid])
        except Exception:
            pass

    files = []
    if ok and dest_dir.exists():
        files = [
            p for p in dest_dir.rglob("*")
            if p.is_file() and not p.name.endswith(".part") and not p.name.endswith(".aria2")
        ]

    return DownloadResult(ok=ok, files=files, error_tail=error_tail, attempts=1)


async def download_torrent_async(
    torrent_or_magnet: str,
    dest_dir: Path,
    on_progress: Callable[..., None] | None = None,
    register_proc: Callable[[Any], None] | None = None,
) -> DownloadResult:
    """Thin wrapper calling download_via_aria2_async with torrent-specific defaults."""
    return await download_via_aria2_async(
        target=torrent_or_magnet,
        dest_dir=dest_dir,
        options=None,
        on_progress=on_progress,
        register_proc=register_proc,
    )
