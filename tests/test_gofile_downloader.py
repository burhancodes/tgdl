from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.downloader.gofile import (
    GoFileDownloader,
    convert_gofile_url_to_bypass_url,
    download_gofile,
    extract_gofile_info,
    is_gofile_url,
)


def test_is_gofile_url():
    assert is_gofile_url("https://gofile.io/d/QGjyK2")
    assert is_gofile_url("https://www.gofile.io/d/QGjyK2")
    assert is_gofile_url("https://gofile.co/d/XYZ123")
    assert is_gofile_url("https://sub.gofile.io/f/abcd456")
    assert is_gofile_url("gofile:https://gofile.io/d/QGjyK2")
    assert is_gofile_url("gofile:QGjyK2")
    assert is_gofile_url("gf:https://gofile.io/d/QGjyK2")
    assert is_gofile_url("gfdl:https://gofile.io/d/QGjyK2")
    assert is_gofile_url("https://gf.1drv.eu.org/QGjyK2")

    assert not is_gofile_url("https://example.com/file.zip")
    assert not is_gofile_url("https://drive.google.com/file/d/123")
    assert not is_gofile_url("https://mega.nz/file/123#abc")
    assert not is_gofile_url("")


def test_extract_gofile_info():
    # /d/<id>
    assert extract_gofile_info("https://gofile.io/d/QGjyK2") == ("QGjyK2", "")
    assert extract_gofile_info("https://www.gofile.io/d/QGjyK2") == ("QGjyK2", "")
    # with trailing path
    assert extract_gofile_info("https://gofile.io/d/QGjyK2/video.mp4") == ("QGjyK2", "/video.mp4")
    # /f/<id>
    assert extract_gofile_info("https://gofile.co/f/myfolder123") == ("myfolder123", "")
    # /file/d/<id>
    assert extract_gofile_info("https://gofile.io/file/d/test_id_99") == ("test_id_99", "")
    # ?file=<id>
    assert extract_gofile_info("https://gofile.io/?file=query_id_1") == ("query_id_1", "")
    # /<id> direct
    assert extract_gofile_info("https://gofile.io/direct_id_42") == ("direct_id_42", "")
    # gofile: prefix
    assert extract_gofile_info("gofile:https://gofile.io/d/prefixed123") == ("prefixed123", "")
    assert extract_gofile_info("gofile:raw_id_only") == ("raw_id_only", "")

    # Ignored prefixes
    assert extract_gofile_info("https://gofile.io/api/getFolder") is None
    assert extract_gofile_info("https://gofile.io/public/assets/logo.png") is None
    assert extract_gofile_info("https://gofile.io/static/style.css") is None
    assert extract_gofile_info("https://gofile.io/cdn/chunk.bin") is None
    assert extract_gofile_info("https://gofile.io/download?id=123") is None

    # Opt-out parameter
    assert extract_gofile_info("https://gofile.io/d/QGjyK2?noredirect=1") is None


def test_convert_gofile_url_to_bypass_url():
    # Default host gf.1drv.eu.org
    res1 = convert_gofile_url_to_bypass_url("https://gofile.io/d/QGjyK2")
    assert res1 == "https://gf.1drv.eu.org/QGjyK2"

    # Trailing path preserved
    res2 = convert_gofile_url_to_bypass_url("https://gofile.io/d/QGjyK2/movie.mp4")
    assert res2 == "https://gf.1drv.eu.org/QGjyK2/movie.mp4"

    # Custom bypass host
    res3 = convert_gofile_url_to_bypass_url("https://gofile.co/f/abcd", bypass_host="custom.proxy.org")
    assert res3 == "https://custom.proxy.org/abcd"

    # Preserves query params and hash
    res4 = convert_gofile_url_to_bypass_url("https://gofile.io/d/XYZ123?token=abc#section")
    assert res4 == "https://gf.1drv.eu.org/XYZ123?token=abc#section"

    # Opt-out noredirect returns original URL
    res5 = convert_gofile_url_to_bypass_url("https://gofile.io/d/XYZ123?token=abc&noredirect=1")
    assert res5 == "https://gofile.io/d/XYZ123?token=abc&noredirect=1"


@pytest.mark.asyncio
async def test_gofile_downloader_batch_serial_natural_sort(tmp_path: Path):
    """Verify GoFileDownloader translates URLs, sorts batch naturally, and downloads serially."""
    downloader = GoFileDownloader(dest_dir=tmp_path)

    batch_urls = [
        "https://gofile.io/d/id_ep10",
        "https://gofile.io/d/id_ep2",
        "https://gofile.io/d/id_ep1",
    ]

    downloaded_urls: list[str] = []

    async def mock_direct_download(items):
        for it in items:
            downloaded_urls.append(it["url"])
            f = tmp_path / Path(it["url"]).name
            f.touch()
        return [tmp_path / Path(it["url"]).name for it in items]

    with patch("app.downloader.gofile.core.DirectDownloader.download", side_effect=mock_direct_download):
        files = await downloader.download(json.dumps(batch_urls))

        # Expected natural order: ep1, ep2, ep10
        assert downloaded_urls == [
            "https://gf.1drv.eu.org/id_ep1",
            "https://gf.1drv.eu.org/id_ep2",
            "https://gf.1drv.eu.org/id_ep10",
        ]
        assert [f.name for f in files] == ["id_ep1", "id_ep2", "id_ep10"]


@pytest.mark.asyncio
async def test_download_gofile_helper(tmp_path: Path):
    """Verify download_gofile top-level helper function."""
    with patch("app.downloader.gofile.core.DirectDownloader.download", new_callable=AsyncMock) as mock_dl:
        dummy_file = tmp_path / "downloaded.mkv"
        dummy_file.touch()
        mock_dl.return_value = [dummy_file]

        res = await download_gofile("https://gofile.io/d/sample123", tmp_path)
        assert res == [dummy_file]
        mock_dl.assert_awaited_once()


@pytest.mark.asyncio
async def test_manager_gofile_routing(tmp_path: Path):
    """Verify that QueueManager routes GoFile URLs directly to GoFileDownloader."""
    from app.db import Job, JobStatus
    from app.manager.core import QueueManager
    from app.manager.state import JobState

    qm = QueueManager()
    qm.client = MagicMock()
    mock_store = AsyncMock()
    qm.store = mock_store

    job = Job(
        id="job_gf_1",
        chat_id=12345,
        status_message_id=None,
        url="https://gofile.io/d/testGfLink",
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
        args=json.dumps({"user_id": 67890}),
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    mock_store.get_job.return_value = job

    mock_downloaded_file = tmp_path / "test.mp4"
    mock_downloaded_file.touch()

    with patch("app.manager.core.store.get_job", new_callable=AsyncMock) as mock_get_job, \
         patch("app.manager.core.store.update_progress", new_callable=AsyncMock), \
         patch("app.downloader.gofile.core.GoFileDownloader.download", new_callable=AsyncMock) as mock_gf_download:

        mock_get_job.return_value = job
        mock_gf_download.return_value = [mock_downloaded_file]

        await qm._process_download(job_state)

        mock_gf_download.assert_awaited_once_with("https://gofile.io/d/testGfLink")


@pytest.mark.asyncio
async def test_gofile_handler_command():
    """Verify /gofile with link enqueues a download job, while reply to media uploads."""
    from pyrogram.types import Message, User
    from app.handlers.download import register_download_handlers

    mock_app = MagicMock()
    registered_handlers = {}

    def mock_on_message(filter_expr):
        def decorator(fn):
            registered_handlers[fn.__name__] = fn
            return fn
        return decorator

    mock_app.on_message.side_effect = mock_on_message
    register_download_handlers(mock_app)

    gfup_cmd = registered_handlers["gfup_cmd"]
    user = User(id=42, is_self=False, first_name="Tester")

    # 1. Download flow: /gofile <link>
    msg_dl = MagicMock(spec=Message)
    msg_dl.chat = MagicMock(id=123)
    msg_dl.from_user = user
    msg_dl.reply_to_message = None
    msg_dl.text = "/gofile https://gofile.io/d/myShare"
    msg_dl.command = ["gofile", "https://gofile.io/d/myShare"]
    msg_dl.reply_text = AsyncMock()

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await gfup_cmd(mock_app, msg_dl)
        mock_enqueue.assert_awaited_once()
        args, kwargs = mock_enqueue.call_args
        assert args[2] == "gofile:https://gofile.io/d/myShare"
        assert kwargs["user_id"] == 42
