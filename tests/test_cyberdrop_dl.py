from __future__ import annotations

import asyncio
import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.downloader.cyberdrop_dl.core import (
    CyberdropDLNotFound,
    DownloadResult,
    _build_cmd,
    get_cdl_config_path,
    get_cookies_path,
    get_user_cdl_config_path,
    run_with_progress,
)
from app.downloader import run_cyberdrop_dl


def test_build_cmd_defaults(tmp_path: Path):
    dest = tmp_path / "downloads"
    urls = ["https://example.com/album"]
    cmd = _build_cmd(urls, dest)

    assert "download" in cmd
    assert "--ui" in cmd
    assert "disabled" in cmd
    assert "--min-free-space" in cmd
    assert "0" in cmd
    assert "--ignore-history" in cmd
    assert "--download-folder" in cmd
    assert str(dest.absolute()) in cmd
    assert urls[0] in cmd


def test_build_cmd_with_extra_args_and_speed_limit(tmp_path: Path):
    dest = tmp_path / "downloads"
    urls = ["https://example.com/album"]
    with patch("app.downloader.cyberdrop_dl.core.settings.global_download_speed_limit", "5M"):
        cmd = _build_cmd(urls, dest, extra_args=["--deep-scrape"])
        assert "--deep-scrape" in cmd
        assert "--speed-limit" in cmd


def test_config_paths(tmp_path: Path):
    with patch("app.downloader.cyberdrop_dl.core.settings.auth_dir", tmp_path / "auth"):
        user_id = 12345
        user_conf = get_user_cdl_config_path(user_id)
        assert user_conf == tmp_path / "auth" / "12345" / "cyberdrop-dl.yaml"


@pytest.mark.asyncio
async def test_run_with_progress_success(tmp_path: Path):
    dest = tmp_path / "downloads"
    dest.mkdir(parents=True, exist_ok=True)
    test_file = dest / "image.jpg"
    test_file.write_bytes(b"mock image content")

    mock_proc = MagicMock()
    mock_proc.returncode = 0
    mock_proc.wait = AsyncMock(return_value=0)

    async def mock_stdout_gen():
        yield b"Lock for 'image.jpg' acquired\n"
        yield b"Completed: image.jpg\n"
        yield b"Downloaded: 1 files\n"

    async def mock_stderr_gen():
        if False:
            yield b""

    mock_proc.stdout = mock_stdout_gen()
    mock_proc.stderr = mock_stderr_gen()

    with patch("asyncio.create_subprocess_exec", AsyncMock(return_value=mock_proc)):
        progress_calls = []

        def on_prog(count, filename=None, url=None):
            progress_calls.append((count, filename, url))

        res = await run_with_progress("cdl:https://example.com/album", dest, on_progress=on_prog)
        assert res.ok is True
        assert len(res.files) == 1
        assert res.files[0] == test_file
        assert len(progress_calls) > 0


@pytest.mark.asyncio
async def test_run_cyberdrop_dl_json_batch(tmp_path: Path):
    dest = tmp_path / "downloads"
    dest.mkdir(parents=True, exist_ok=True)
    test_file1 = dest / "file1.jpg"
    test_file1.write_bytes(b"f1")

    mock_proc = MagicMock()
    mock_proc.returncode = 0
    mock_proc.wait = AsyncMock(return_value=0)

    async def mock_stdout_gen():
        yield b"Downloaded: 1 files\n"

    async def mock_stderr_gen():
        if False:
            yield b""

    mock_proc.stdout = mock_stdout_gen()
    mock_proc.stderr = mock_stderr_gen()

    urls_json = json.dumps(["cdl:https://example.com/item1", "cyberdrop-dl:https://example.com/item2"])
    with patch("asyncio.create_subprocess_exec", AsyncMock(return_value=mock_proc)) as mock_exec:
        res = await run_cyberdrop_dl(urls_json, dest)
        assert res.ok is True
        assert mock_exec.call_count == 2
        called_url1 = mock_exec.call_args_list[0][0][-1]
        called_url2 = mock_exec.call_args_list[1][0][-1]
        assert called_url1 == "https://example.com/item1"
        assert called_url2 == "https://example.com/item2"


@pytest.mark.asyncio
async def test_run_with_progress_missing_binary(tmp_path: Path):
    with patch("app.downloader.cyberdrop_dl.core._find_cdl_binary", return_value=[]):
        with pytest.raises(CyberdropDLNotFound):
            await run_with_progress("https://example.com/test", tmp_path)


@pytest.mark.asyncio
async def test_gallery_dl_per_url_immediate_fallback(tmp_path: Path):
    from app.downloader.gallery_dl.core import run_with_progress as run_gdl

    dest = tmp_path / "downloads"
    dest.mkdir(parents=True, exist_ok=True)

    urls = ["https://example.com/fail_gdl", "https://example.com/success_gdl"]

    # Mock gallery-dl subprocess: fails on url 1, succeeds on url 2
    async def mock_gdl_subprocess(*args, **kwargs):
        called_cmd = list(args)
        proc = MagicMock()
        target_url = called_cmd[-1] if called_cmd else ""

        if "fail_gdl" in target_url:
            proc.returncode = 1
            proc.wait = AsyncMock(return_value=1)
            async def mock_out():
                if False:
                    yield b""
            async def mock_err():
                yield b"ERROR: Unsupported URL\n"
        else:
            proc.returncode = 0
            proc.wait = AsyncMock(return_value=0)
            async def mock_out():
                (dest / "gdl_success.jpg").write_bytes(b"gdl")
                yield b"Completed: gdl_success.jpg\n"
            async def mock_err():
                if False:
                    yield b""
        proc.stdout = mock_out()
        proc.stderr = mock_err()
        return proc

    # Mock cyberdrop-dl for fallback: succeeds on fail_gdl
    async def mock_cdl_run(url, dest_dir, **kwargs):
        (dest_dir / "cdl_fallback.jpg").write_bytes(b"cdl")
        return DownloadResult(ok=True, files=[dest_dir / "cdl_fallback.jpg"])

    with patch("shutil.which", return_value="/usr/bin/gallery-dl"), \
         patch("asyncio.create_subprocess_exec", side_effect=mock_gdl_subprocess), \
         patch("app.downloader.cyberdrop_dl.run_with_progress", side_effect=mock_cdl_run) as mock_cdl_fallback:

        res = await run_gdl(urls, dest)
        assert res.ok is True
        assert len(res.files) == 2
        # Assert CDL was called immediately for URL 1
        assert mock_cdl_fallback.call_count == 1
        assert "fail_gdl" in mock_cdl_fallback.call_args_list[0][0][0]
