from __future__ import annotations

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.db import JobStore
from app.handlers.callbacks import register_choice_callback_handlers
from app.handlers.conversion_state import conversion_session_store
from app.manager.core import QueueManager
from app.manager.state import JobState
from app.utils.archive import archive_session_store


@pytest.mark.asyncio
async def test_conversion_session_store_lifecycle():
    """Tests registration, event triggering, choice selection, and atomic pop in ConversionSessionStore."""
    job_id = "test_job_101"
    conv_id = "1"
    filename = "audio_sample.flac"

    conversion_session_store.register_conversion_id(job_id, conv_id, filename)
    assert conversion_session_store.get_conversion_filename(job_id, conv_id) == filename
    assert conversion_session_store.get_conversion_ids(job_id) == {"1": filename}
    assert conversion_session_store.get_next_conversion_id(job_id) == "2"

    evt = conversion_session_store.create_event(job_id, conv_id)
    assert not evt.is_set()

    conversion_session_store.set_choice(job_id, conv_id, "mp3")
    conversion_session_store.set_event(job_id, conv_id)

    assert evt.is_set()
    assert conversion_session_store.get_choice(job_id, conv_id) == "mp3"
    assert conversion_session_store.has_choice(job_id, conv_id) is True
    assert conversion_session_store.contains_job(job_id) is True

    conversion_session_store.add_converted_file(job_id, "audio_sample_converted.mp3")
    assert "audio_sample_converted.mp3" in conversion_session_store.get_converted_files(job_id)

    conversion_session_store.pop_job(job_id)
    assert conversion_session_store.contains_job(job_id) is False
    assert conversion_session_store.get_choice(job_id, conv_id) is None


@pytest.mark.asyncio
async def test_conversion_choice_callback_end_to_end(tmp_path: Path):
    """Simulates clicking a conversion-choice callback query button end-to-end."""
    db_path = tmp_path / "test_callback.sqlite3"
    store = JobStore(db_path)
    await store.open()

    chat_id = 123456
    job = await store.create_job(chat_id=chat_id, url="https://example.com/song.flac")
    job_id = job.id
    conv_id = "1"

    from app.handlers import callbacks
    callbacks.store = store

    # Pre-register conversion event and filename
    conversion_session_store.register_conversion_id(job_id, conv_id, "song.flac")
    evt = conversion_session_store.create_event(job_id, conv_id)

    mock_app = MagicMock()
    handler_fn = None

    def mock_on_callback_query(filters_arg):
        def decorator(fn):
            nonlocal handler_fn
            handler_fn = fn
            return fn
        return decorator

    mock_app.on_callback_query = mock_on_callback_query
    register_choice_callback_handlers(mock_app)

    # Find the conversion choice handler
    assert handler_fn is not None

    # Construct mock Pyrogram CallbackQuery
    mock_query = AsyncMock()
    mock_query.data = f"convert_mp3:{job_id}:{conv_id}"

    mock_match = MagicMock()
    mock_match.group = lambda i: {"1": "mp3", "2": job_id, "3": conv_id}[str(i)]
    mock_query.matches = [mock_match]

    mock_message = AsyncMock()
    mock_message.chat.id = chat_id
    mock_query.message = mock_message
    mock_query.from_user.id = chat_id

    # Execute conversion choice handler
    await handler_fn(mock_app, mock_query)

    # Assert choice set in store & event triggered
    assert conversion_session_store.get_choice(job_id, conv_id) == "mp3"
    assert evt.is_set() is True
    mock_query.answer.assert_called_once()
    mock_message.edit_text.assert_called_once()

    await store.close()


@pytest.mark.asyncio
async def test_process_upload_finally_cleanup_no_importerror(tmp_path: Path):
    """Regression test: Ensures _process_upload's finally block completes without ImportError and evicts qm.jobs."""
    db_path = tmp_path / "test_finally_cleanup.sqlite3"
    store = JobStore(db_path)
    await store.open()

    qm = QueueManager()
    qm.store = store
    qm.client = AsyncMock()

    job = await store.create_job(chat_id=999, url="https://example.com/test.zip")
    dest_dir = tmp_path / "downloads" / job.download_dir
    dest_dir.mkdir(parents=True, exist_ok=True)

    job_state = JobState(job=job, dest_dir=dest_dir)
    job_state.downloader_done.set()
    qm.jobs[job.id] = job_state

    # Populate session stores
    archive_session_store.register_archive_id(job.id, "1", "data.zip")
    conversion_session_store.register_conversion_id(job.id, "1", "audio.flac")

    assert job.id in qm.jobs

    # Execute _process_upload, which will complete and hit the finally block
    await qm._process_upload(job_state)

    # Assert job evicted from qm.jobs and session stores cleared
    assert job.id not in qm.jobs
    assert not archive_session_store.contains_job(job.id)
    assert not conversion_session_store.contains_job(job.id)

    await store.close()


@pytest.mark.asyncio
async def test_automatic_video_conversion_no_nameerror(tmp_path: Path):
    """Regression test: Ensures automatic video conversion code path uses conversion_session_store without NameError."""
    db_path = tmp_path / "test_video_conv.sqlite3"
    store = JobStore(db_path)
    await store.open()

    qm = QueueManager()
    qm.store = store
    qm.client = AsyncMock()

    job = await store.create_job(chat_id=888, url="https://example.com/video.mkv")
    dest_dir = tmp_path / "downloads" / job.download_dir
    dest_dir.mkdir(parents=True, exist_ok=True)

    # Create dummy video file with CONVERSION_EXT (.mov)
    dummy_video = dest_dir / "sample.mov"
    dummy_video.write_bytes(b"dummy video data")

    job_state = JobState(job=job, dest_dir=dest_dir)
    job_state.downloader_done.set()
    qm.jobs[job.id] = job_state

    # Mock conversion and upload functions to avoid real FFmpeg / Telegram calls
    with patch.object(conversion_session_store, "add_converted_file", wraps=conversion_session_store.add_converted_file) as mock_add, \
         patch("app.utils.media.convert_media_async", new=AsyncMock(return_value=True)), \
         patch("app.uploader.handle_large_file", new=AsyncMock(side_effect=lambda f, s: [f])), \
         patch("app.manager.core.safe_send", new=AsyncMock()), \
         patch("app.uploader.upload_file", new=AsyncMock(return_value=True)):
        await qm._process_upload(job_state)

    mock_add.assert_called_once_with(job.id, "sample.mov")

    await store.close()
