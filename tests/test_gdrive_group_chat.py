from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from pyrogram.types import Message, User, Chat

from app.db import Job, JobStatus, JobStore
from app.gdrive.auth import GoogleDriveAuthManager
from app.handlers.download import _create_and_enqueue_job, register_download_handlers
from app.manager.core import JobState, QueueManager


def test_gdrive_auth_manager_rejects_negative_chat_id():
    """Verify GoogleDriveAuthManager sanitizes negative group chat IDs to None."""
    # Negative integer group ID
    mgr_int = GoogleDriveAuthManager(user_id=-1003976795539)
    assert mgr_int.user_id is None
    assert mgr_int.token_path.name == "token.json"
    assert "-1003976795539" not in str(mgr_int.token_path)
    assert "-1003976795539" not in str(mgr_int.accounts_dir)

    # Negative string group ID
    mgr_str = GoogleDriveAuthManager(user_id="-1003976795539")
    assert mgr_str.user_id is None
    assert "-1003976795539" not in str(mgr_str.token_path)
    assert "-1003976795539" not in str(mgr_str.accounts_dir)

    # Valid positive user ID
    mgr_valid = GoogleDriveAuthManager(user_id=6754789603)
    assert mgr_valid.user_id == "6754789603"
    assert "6754789603" in str(mgr_valid.token_path)
    assert "6754789603" in str(mgr_valid.accounts_dir)


@pytest.mark.asyncio
async def test_create_and_enqueue_job_sets_sender_user_id_not_chat_id(tmp_path: Path):
    """Verify _create_and_enqueue_job saves from_user.id in args_dict and does NOT fallback to chat_id."""
    mock_store = AsyncMock()
    mock_qm = MagicMock()
    mock_qm.get_active_jobs_for_chat.return_value = []
    mock_qm.add_job = AsyncMock()
    mock_qm.jobs = {}

    dummy_job = Job(
        id="job_gd_test",
        chat_id=-1003976795539,
        status_message_id=None,
        url="gd2tg:https://drive.google.com/file/d/test/view",
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
    )
    mock_store.create_job.return_value = dummy_job
    mock_store.get_job.return_value = dummy_job

    mock_client = MagicMock()
    mock_message = MagicMock(spec=Message)
    mock_message.chat = MagicMock(spec=Chat, id=-1003976795539)
    mock_message.from_user = MagicMock(spec=User, id=6754789603)
    mock_message.reply_text = AsyncMock()

    with patch("app.handlers.download.store", mock_store), \
         patch("app.handlers.download.queue_manager", mock_qm), \
         patch("app.handlers.download.safe_send", new_callable=AsyncMock):
        await _create_and_enqueue_job(
            client=mock_client,
            chat_id=-1003976795539,
            target_url="gd2tg:https://drive.google.com/file/d/test/view",
            message=mock_message,
            display_text="https://drive.google.com/file/d/test/view",
        )

        mock_store.create_job.assert_called_once()
        call_args = mock_store.create_job.call_args
        # chat_id passed to DB must be group chat_id for routing
        assert call_args[0][0] == -1003976795539
        # args_dict passed in args must have user_id = 6754789603
        saved_args = json.loads(call_args[1]["args"])
        assert saved_args["user_id"] == 6754789603
        assert saved_args["user_id"] != -1003976795539


@pytest.mark.asyncio
async def test_manager_uses_user_id_not_group_chat_id(tmp_path: Path):
    """Verify manager._process_download initializes GoogleDriveDownloader with sender user_id and never group chat_id."""
    qm = QueueManager()
    qm.client = MagicMock()
    mock_store = AsyncMock()
    qm.store = mock_store

    # Job created in group chat -1003976795539 by user 6754789603
    job = Job(
        id="job_gd_1",
        chat_id=-1003976795539,
        status_message_id=None,
        url="gd2tg:https://drive.google.com/file/d/test12345/view",
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
        args=json.dumps({"user_id": 6754789603}),
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    mock_store.get_job.return_value = job

    with patch("app.gdrive.GoogleDriveDownloader") as mock_downloader_cls, \
         patch("app.manager.core.safe_delete", new_callable=AsyncMock):
        mock_instance = MagicMock()
        mock_instance.download_link = AsyncMock()
        mock_downloader_cls.return_value = mock_instance

        await qm._process_download(job_state)

        mock_downloader_cls.assert_called_once()
        called_user_id = mock_downloader_cls.call_args[1]["user_id"]
        assert called_user_id == 6754789603
        assert called_user_id != -1003976795539


@pytest.mark.asyncio
async def test_manager_group_chat_without_user_id_passes_none(tmp_path: Path):
    """Verify that if a group chat job somehow lacks user_id, GoogleDriveDownloader receives user_id=None (not group chat_id)."""
    qm = QueueManager()
    qm.client = MagicMock()
    mock_store = AsyncMock()
    qm.store = mock_store

    # Job created in group chat -1003976795539 without user_id in args
    job = Job(
        id="job_gd_2",
        chat_id=-1003976795539,
        status_message_id=None,
        url="gd2tg:https://drive.google.com/file/d/test12345/view",
        status=JobStatus.QUEUED,
        total_files=0,
        sent_files=0,
        skipped_files=0,
        error=None,
        created_at=0,
        updated_at=0,
        args=None,
    )
    job_state = JobState(job=job, dest_dir=tmp_path)
    mock_store.get_job.return_value = job

    with patch("app.gdrive.GoogleDriveDownloader") as mock_downloader_cls, \
         patch("app.manager.core.safe_delete", new_callable=AsyncMock):
        mock_instance = MagicMock()
        mock_instance.download_link = AsyncMock()
        mock_downloader_cls.return_value = mock_instance

        await qm._process_download(job_state)

        mock_downloader_cls.assert_called_once()
        called_user_id = mock_downloader_cls.call_args[1]["user_id"]
        assert called_user_id is None
        assert called_user_id != -1003976795539


@pytest.mark.asyncio
async def test_gd2tg_credential_reply_saves_under_user_id(tmp_path: Path, monkeypatch):
    """Verify replying with /gd2tg to a service account JSON in a group chat saves under auth/<user_id>/accounts/."""
    monkeypatch.setattr("app.config.settings.auth_dir", tmp_path / "auth")

    mock_app = MagicMock()
    registered_handlers = {}

    def mock_on_message(filter_expr):
        def decorator(fn):
            registered_handlers[fn.__name__] = fn
            return fn
        return decorator

    mock_app.on_message = mock_on_message
    register_download_handlers(mock_app)

    gd2tg_fn = registered_handlers["gd2tg_cmd"]

    # Create dummy Service Account JSON
    temp_sa_file = tmp_path / "sa.json"
    sa_data = {"type": "service_account", "project_id": "my-project", "client_email": "sa@my-project.iam.gserviceaccount.com"}
    temp_sa_file.write_text(json.dumps(sa_data), encoding="utf-8")

    reply_msg = MagicMock(spec=Message)
    reply_doc = MagicMock()
    reply_doc.file_name = "sa.json"
    reply_msg.document = reply_doc
    reply_msg.download = AsyncMock(return_value=str(temp_sa_file))

    status_msg = AsyncMock()
    status_msg.edit_text = AsyncMock()

    msg = MagicMock(spec=Message)
    msg.text = "/gd2tg"
    msg.caption = None
    msg.chat = MagicMock(spec=Chat, id=-1003976795539)  # group chat
    msg.from_user = MagicMock(spec=User, id=6754789603) # real user
    msg.reply_to_message = reply_msg
    msg.reply_text = AsyncMock(return_value=status_msg)

    await gd2tg_fn(mock_app, msg)

    # Verify saved to auth/6754789603/accounts/sa.json
    expected_path = tmp_path / "auth" / "6754789603" / "accounts" / "sa.json"
    assert expected_path.exists()
    assert "-1003976795539" not in str(expected_path)
    content = json.loads(expected_path.read_text(encoding="utf-8"))
    assert content["type"] == "service_account"
