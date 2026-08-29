from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.downloader.direct.core import DirectDownloader, DirectDownloadError
from app.gdrive.downloader import GoogleDriveDownloader
from app.manager.core import JobState, QueueManager
from app.mega import MegaDownloader
from app.db import Job, JobStatus


@pytest.mark.asyncio
async def test_direct_downloader_batch_strips_direct_prefix(tmp_path: Path):
    """Verify DirectDownloader.download() strips 'direct:' prefix from JSON array URLs."""
    downloader = DirectDownloader(dest_dir=tmp_path)
    urls = [
        "direct:https://example.com/file1.mp4",
        "direct:https://example.com/file2.mp4",
        "direct:https://example.com/file3.mp4",
    ]
    json_urls = json.dumps(urls)

    mock_item_download = AsyncMock()
    mock_item_download.side_effect = lambda session, url, filename, subpath: tmp_path / Path(url).name

    with patch.object(downloader, "_download_content_item", side_effect=mock_item_download):
        with patch("aiohttp.ClientSession"):
            res = await downloader.download(json_urls)
            assert len(res) == 3
            assert mock_item_download.call_count == 3
            called_urls = [call.kwargs["url"] for call in mock_item_download.call_args_list]
            assert called_urls == [
                "https://example.com/file1.mp4",
                "https://example.com/file2.mp4",
                "https://example.com/file3.mp4",
            ]


@pytest.mark.asyncio
async def test_direct_downloader_partial_failure_succeeds(tmp_path: Path):
    """Verify DirectDownloader only fails if all items in batch fail."""
    downloader = DirectDownloader(dest_dir=tmp_path)
    urls = [
        "https://example.com/file1.mp4",
        "https://example.com/file2.mp4",
        "https://example.com/file3.mp4",
    ]
    json_urls = json.dumps(urls)

    async def mock_download_item(session, url, filename, subpath):
        if "file2" in url:
            raise ValueError("HTTP 500 Connection Refused")
        f = tmp_path / Path(url).name
        f.touch()
        return f

    with patch.object(downloader, "_download_content_item", side_effect=mock_download_item):
        with patch("aiohttp.ClientSession"):
            res = await downloader.download(json_urls)
            assert len(res) == 2
            assert (tmp_path / "file1.mp4") in res
            assert (tmp_path / "file3.mp4") in res


@pytest.mark.asyncio
async def test_direct_downloader_all_failure_raises(tmp_path: Path):
    """Verify DirectDownloader raises DirectDownloadError when all items fail."""
    downloader = DirectDownloader(dest_dir=tmp_path)
    urls = ["https://example.com/file1.mp4", "https://example.com/file2.mp4"]
    json_urls = json.dumps(urls)

    with patch.object(downloader, "_download_content_item", side_effect=ValueError("Download failed")):
        with patch("aiohttp.ClientSession"):
            with pytest.raises(DirectDownloadError, match="All 2 direct file downloads failed"):
                await downloader.download(json_urls)


@pytest.mark.asyncio
async def test_mega_downloader_batch_partial_failure(tmp_path: Path):
    """Verify MegaDownloader processes all URLs and only fails if ALL fail."""
    mock_client = MagicMock()
    mock_client.ensure_logged_in = AsyncMock()

    async def mock_download_url(url, output_dir):
        if "fail" in url:
            raise RuntimeError("MEGA download error")
        (output_dir / f"{Path(url).name}.bin").touch()

    mock_client.download_url = AsyncMock(side_effect=mock_download_url)
    downloader = MegaDownloader(client=mock_client)

    urls = [
        "mega:https://mega.nz/file/ABC1",
        "mega:https://mega.nz/file/fail2",
        "mega:https://mega.nz/file/ABC3",
    ]
    res = await downloader.download_link(json.dumps(urls), tmp_path)
    assert len(res) == 2
    assert mock_client.download_url.call_count == 3


@pytest.mark.asyncio
async def test_manager_passes_job_url_to_downloaders(tmp_path: Path):
    """Verify Manager _process_download passes full job.url to Mega & Direct downloaders."""
    mock_store = AsyncMock()
    manager = QueueManager()
    manager.client = MagicMock()
    manager.store = mock_store

    urls = [
        "direct:https://example.com/a.mp4",
        "direct:https://example.com/b.mp4",
        "direct:https://example.com/c.mp4",
    ]
    job_url_json = json.dumps(urls)

    job = Job(
        id="job123",
        chat_id=100,
        status_message_id=None,
        url=job_url_json,
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    mock_store.get_job.return_value = job

    with patch("app.downloader.download_direct", new_callable=AsyncMock) as mock_direct:
        mock_direct.return_value = [tmp_path / "a.mp4", tmp_path / "b.mp4", tmp_path / "c.mp4"]
        with patch("app.manager.core.safe_delete", new_callable=AsyncMock):
            await manager._process_download(job_state)
            mock_direct.assert_called_once()
            called_target_url = mock_direct.call_args[0][0]
            assert called_target_url == job_url_json


@pytest.mark.asyncio
async def test_manager_passes_mega_job_url(tmp_path: Path):
    """Verify Manager _process_download passes unreduced job.url to MegaDownloader."""
    mock_store = AsyncMock()
    manager = QueueManager()
    manager.client = MagicMock()
    manager.store = mock_store

    urls = [
        "mega:https://mega.nz/file/ABC1",
        "mega:https://mega.nz/file/ABC2",
    ]
    job_url_json = json.dumps(urls)

    job = Job(
        id="job456",
        chat_id=100,
        status_message_id=None,
        url=job_url_json,
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    mock_store.get_job.return_value = job

    with patch("app.mega.MegaDownloader.download_link", new_callable=AsyncMock) as mock_mega_dl:
        mock_mega_dl.return_value = [tmp_path / "ABC1.bin", tmp_path / "ABC2.bin"]
        with patch("app.manager.core.safe_delete", new_callable=AsyncMock):
            await manager._process_download(job_state)
            mock_mega_dl.assert_called_once()
            called_url = mock_mega_dl.call_args[0][0]
            assert called_url == job_url_json


@pytest.mark.asyncio
async def test_download_direct_content_type_html_rejected(tmp_path: Path):
    """Verify DirectDownloader rejects text/html Content-Type for binary extension (.mp4)."""
    downloader = DirectDownloader(dest_dir=tmp_path)

    mock_resp = MagicMock()
    mock_resp.status = 200
    mock_resp.headers = {"Content-Type": "text/html; charset=utf-8"}
    mock_resp.content.read = AsyncMock(return_value=b"<html>Login required</html>")

    mock_get = AsyncMock()
    mock_get.__aenter__.return_value = mock_resp

    mock_session = MagicMock()
    mock_session.get.return_value = mock_get

    with patch("app.downloader.direct.core.is_url_private_ip", return_value=False):
        with pytest.raises(DirectDownloadError, match="Expected a file but got an HTML/text response"):
            await downloader._download_content_item(mock_session, "https://example.com/video.mp4")

    assert not (tmp_path / "video.mp4").exists()
    assert not (tmp_path / "video.mp4.part").exists()


@pytest.mark.asyncio
async def test_download_direct_magic_byte_sniff_html_rejected(tmp_path: Path):
    """Verify DirectDownloader magic-byte sniffing rejects HTML body with octet-stream Content-Type."""
    downloader = DirectDownloader(dest_dir=tmp_path)

    mock_resp = MagicMock()
    mock_resp.status = 200
    mock_resp.headers = {"Content-Type": "application/octet-stream"}
    html_body = b"<!DOCTYPE html><html><body>Cloudflare Access Denied</body></html>"
    mock_resp.content.read = AsyncMock(return_value=html_body)

    mock_get = AsyncMock()
    mock_get.__aenter__.return_value = mock_resp

    mock_session = MagicMock()
    mock_session.get.return_value = mock_get

    with patch("app.downloader.direct.core.is_url_private_ip", return_value=False):
        with pytest.raises(DirectDownloadError, match="Expected a file but got an HTML/text response"):
            await downloader._download_content_item(mock_session, "https://example.com/movie.mp4")

    assert not (tmp_path / "movie.mp4").exists()
    assert not (tmp_path / "movie.mp4.part").exists()


@pytest.mark.asyncio
async def test_converted_video_passed_to_handle_large_file_and_uploaded(tmp_path: Path):
    """Verify that after video conversion, converted file falls through to handle_large_file and is uploaded."""
    mock_store = AsyncMock()
    manager = QueueManager()
    manager.client = MagicMock()
    manager.store = mock_store

    large_mov = tmp_path / "large_video.mov"
    large_mov.write_bytes(b"ORIGINAL_MOV_CONTENT")

    job = Job(
        id="job999",
        chat_id=100,
        status_message_id=None,
        url="https://example.com/large_video.mov",
        status=JobStatus.DOWNLOADING,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    job_state.downloader_done.set()
    mock_store.get_job.return_value = job

    async def mock_convert(input_path, output_path):
        in_p = Path(input_path)
        out_p = Path(output_path)
        out_p.write_bytes(b"CONVERTED_MKV_CONTENT")
        in_p.unlink(missing_ok=True)
        return True

    handle_large_file_calls = []

    async def mock_handle_large(f, split_flag):
        handle_large_file_calls.append(Path(f))
        return [Path(f)]

    with patch("app.utils.media.convert_media_async", side_effect=mock_convert):
        with patch("app.uploader.handle_large_file", side_effect=mock_handle_large):
            with patch("app.manager.core.safe_send", new_callable=AsyncMock):
                with patch("app.manager.core.safe_delete", new_callable=AsyncMock):
                    with patch("app.uploader.telegram.core.TelegramUploader.upload", new_callable=AsyncMock) as mock_upload:
                        mock_upload.return_value = MagicMock()
                        await manager._process_upload(job_state)

    # Assert converted file was passed to handle_large_file
    assert len(handle_large_file_calls) == 1
    assert handle_large_file_calls[0].name == "large_video_converted.mkv"

    # Assert mark_uploaded was called for converted file
    mock_store.mark_uploaded.assert_called_with("job999", "large_video_converted.mkv")

    # Assert original file was removed
    assert not large_mov.exists()


@pytest.mark.asyncio
async def test_photo_save_file_invalid_enables_as_doc_for_batch(tmp_path: Path):
    """Verify that if a photo upload triggers PHOTO_SAVE_FILE_INVALID, as_doc mode is remembered on job_state for remaining files in batch."""
    from pyrogram.errors import BadRequest
    from app.uploader.telegram.core import TelegramUploader

    client = MagicMock()
    # Mock send_photo to fail with PHOTO_SAVE_FILE_INVALID on first call
    client.send_photo = AsyncMock(
        side_effect=BadRequest('Telegram says: [400 PHOTO_SAVE_FILE_INVALID] - The photo you tried to send cannot be saved by Telegram.')
    )
    client.send_document = AsyncMock(return_value=MagicMock())

    import PIL.Image
    img1 = tmp_path / "1 (10).jpg"
    img2 = tmp_path / "1 (11).jpg"
    img_data = PIL.Image.new("RGB", (100, 100), color="blue")
    img_data.save(img1, "JPEG")
    img_data.save(img2, "JPEG")

    manager = QueueManager()
    manager.client = client
    mock_store = AsyncMock()
    manager.store = mock_store

    job = Job(
        id="job_photo_test",
        chat_id=123,
        status_message_id=None,
        url="test_url",
        status=JobStatus.DOWNLOADING,
        total_files=2,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
    )
    job_state = JobState(job, tmp_path)
    job_state.downloader_done.set()
    mock_store.get_job.return_value = job

    with patch("app.manager.core.safe_send", new_callable=AsyncMock), \
         patch("app.manager.core.safe_delete", new_callable=AsyncMock):
        await manager._process_upload(job_state)

    # job_state.as_doc should now be True
    assert job_state.as_doc is True
    # send_photo should only have been called ONCE (for img1). img2 should have been sent as document directly!
    assert client.send_photo.call_count == 1
    # send_document should have been called TWICE (once as retry for img1, once directly for img2)
    assert client.send_document.call_count == 2



