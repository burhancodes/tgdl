from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from pyrogram.types import Document, Message, User

from app.handlers.download import register_download_handlers


def _setup_mock_app():
    mock_app = MagicMock()
    registered_handlers = {}

    def mock_on_message(filter_expr):
        def decorator(fn):
            registered_handlers[fn.__name__] = fn
            return fn
        return decorator

    mock_app.on_message.side_effect = mock_on_message
    register_download_handlers(mock_app)
    return mock_app, registered_handlers


@pytest.mark.asyncio
async def test_gdrive_batch_inline_urls():
    """Verify /gd2tg with multiple URLs creates a batch job with json array."""
    app, handlers = _setup_mock_app()
    gd2tg_cmd = handlers["gd2tg_cmd"]

    msg = MagicMock(spec=Message)
    msg.chat = MagicMock(id=123)
    msg.from_user = User(id=999, is_self=False, first_name="Tester")
    msg.reply_to_message = None
    msg.text = "/gd2tg -m -uz https://drive.google.com/file/d/abc12345/view https://drive.google.com/file/d/xyz67890/view"
    msg.command = ["gd2tg"]

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await gd2tg_cmd(app, msg)

        mock_enqueue.assert_called_once()
        args = mock_enqueue.call_args
        urls_json = args[0][2]
        parsed = json.loads(urls_json)
        assert len(parsed) == 2
        assert "gdrive:https://drive.google.com/file/d/abc12345/view" in parsed
        assert "gdrive:https://drive.google.com/file/d/xyz67890/view" in parsed
        assert args[1]["is_mirror"] is True
        assert args[1]["unzip"] is True
        assert args[1]["user_id"] == 999


@pytest.mark.asyncio
async def test_gdrive_batch_txt_reply(tmp_path: Path):
    """Verify /gd2tg replying to a .txt file extracts batch URLs."""
    app, handlers = _setup_mock_app()
    gd2tg_cmd = handlers["gd2tg_cmd"]

    txt_file = tmp_path / "gdrive_links.txt"
    txt_file.write_text(
        "https://drive.google.com/file/d/link1/view\n"
        "https://drive.google.com/file/d/link2/view\n"
        "https://drive.google.com/file/d/link3/view\n"
    )

    reply_doc = MagicMock(spec=Document)
    reply_doc.file_name = "gdrive_links.txt"
    reply_doc.mime_type = "text/plain"

    reply_msg = MagicMock(spec=Message)
    reply_msg.document = reply_doc
    reply_msg.text = None
    reply_msg.caption = None
    reply_msg.download = AsyncMock(return_value=str(txt_file))

    msg = MagicMock(spec=Message)
    msg.chat = MagicMock(id=123)
    msg.from_user = User(id=888, is_self=False, first_name="Tester")
    msg.reply_to_message = reply_msg
    msg.text = "/gd"
    msg.command = ["gd"]

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await gd2tg_cmd(app, msg)

        mock_enqueue.assert_called_once()
        args = mock_enqueue.call_args
        urls_json = args[0][2]
        parsed = json.loads(urls_json)
        assert len(parsed) == 3
        assert "gdrive:https://drive.google.com/file/d/link1/view" in parsed
        assert "gdrive:https://drive.google.com/file/d/link2/view" in parsed
        assert "gdrive:https://drive.google.com/file/d/link3/view" in parsed
        assert args[1]["user_id"] == 888


@pytest.mark.asyncio
async def test_mega_batch_txt_reply(tmp_path: Path):
    """Verify /mega replying to a .txt file extracts batch URLs and passes user_id."""
    app, handlers = _setup_mock_app()
    mega_cmd = handlers["mega_cmd"]

    txt_file = tmp_path / "mega_links.txt"
    txt_file.write_text(
        "https://mega.nz/file/abc#key1\n"
        "https://mega.nz/file/xyz#key2\n"
    )

    reply_doc = MagicMock(spec=Document)
    reply_doc.file_name = "mega_links.txt"
    reply_doc.mime_type = "text/plain"

    reply_msg = MagicMock(spec=Message)
    reply_msg.document = reply_doc
    reply_msg.text = None
    reply_msg.caption = None
    reply_msg.download = AsyncMock(return_value=str(txt_file))

    msg = MagicMock(spec=Message)
    msg.chat = MagicMock(id=123)
    msg.from_user = User(id=777, is_self=False, first_name="Tester")
    msg.reply_to_message = reply_msg
    msg.text = "/mega -uz"
    msg.command = ["mega"]

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await mega_cmd(app, msg)

        mock_enqueue.assert_called_once()
        args = mock_enqueue.call_args
        urls_json = args[0][2]
        parsed = json.loads(urls_json)
        assert len(parsed) == 2
        assert "mega:https://mega.nz/file/abc#key1" in parsed
        assert "mega:https://mega.nz/file/xyz#key2" in parsed
        assert args[1]["unzip"] is True
        assert args[1]["user_id"] == 777


@pytest.mark.asyncio
async def test_gofile_batch_inline_and_txt_reply(tmp_path: Path):
    """Verify /gofile with multiple links and replying to .txt works, while media replies still upload."""
    app, handlers = _setup_mock_app()
    gfup_cmd = handlers["gfup_cmd"]

    # 1. Inline multiple URLs: /gofile url1 url2
    msg_inline = MagicMock(spec=Message)
    msg_inline.chat = MagicMock(id=123)
    msg_inline.from_user = User(id=666, is_self=False, first_name="Tester")
    msg_inline.reply_to_message = None
    msg_inline.text = "/gofile https://gofile.io/d/share1 https://gofile.io/d/share2"
    msg_inline.command = ["gofile"]

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await gfup_cmd(app, msg_inline)

        mock_enqueue.assert_called_once()
        args = mock_enqueue.call_args
        urls_json = args[0][2]
        parsed = json.loads(urls_json)
        assert len(parsed) == 2
        assert "gofile:https://gofile.io/d/share1" in parsed
        assert "gofile:https://gofile.io/d/share2" in parsed
        assert args[1]["user_id"] == 666

    # 2. Reply to .txt file: /gfdl replying to links.txt
    txt_file = tmp_path / "gofile_links.txt"
    txt_file.write_text(
        "https://gofile.io/d/fileA\n"
        "https://gofile.io/d/fileB\n"
    )

    reply_doc = MagicMock(spec=Document)
    reply_doc.file_name = "gofile_links.txt"
    reply_doc.mime_type = "text/plain"

    reply_msg = MagicMock(spec=Message)
    reply_msg.document = reply_doc
    reply_msg.video = None
    reply_msg.photo = None
    reply_msg.audio = None
    reply_msg.voice = None
    reply_msg.text = None
    reply_msg.caption = None
    reply_msg.download = AsyncMock(return_value=str(txt_file))

    msg_reply = MagicMock(spec=Message)
    msg_reply.chat = MagicMock(id=123)
    msg_reply.from_user = User(id=666, is_self=False, first_name="Tester")
    msg_reply.reply_to_message = reply_msg
    msg_reply.text = "/gfdl"
    msg_reply.command = ["gfdl"]

    with patch("app.handlers.download._create_and_enqueue_job", new_callable=AsyncMock) as mock_enqueue:
        await gfup_cmd(app, msg_reply)

        mock_enqueue.assert_called_once()
        args = mock_enqueue.call_args
        urls_json = args[0][2]
        parsed = json.loads(urls_json)
        assert len(parsed) == 2
        assert "gofile:https://gofile.io/d/fileA" in parsed
        assert "gofile:https://gofile.io/d/fileB" in parsed


@pytest.mark.asyncio
async def test_gofile_media_reply_still_uploads(tmp_path: Path):
    """Verify replying to actual video/photo/media still uploads to GoFile."""
    app, handlers = _setup_mock_app()
    gfup_cmd = handlers["gfup_cmd"]

    dummy_media = tmp_path / "video.mp4"
    dummy_media.write_bytes(b"VIDEO_DATA")

    reply_msg = MagicMock(spec=Message)
    reply_msg.document = None
    reply_msg.video = MagicMock(file_name="video.mp4")
    reply_msg.photo = None
    reply_msg.audio = None
    reply_msg.voice = None
    reply_msg.text = None
    reply_msg.caption = None
    reply_msg.download = AsyncMock(return_value=str(dummy_media))

    msg = MagicMock(spec=Message)
    msg.chat = MagicMock(id=123)
    msg.from_user = User(id=555, is_self=False, first_name="Tester")
    msg.reply_to_message = reply_msg
    msg.text = "/gfup"
    msg.command = ["gfup"]
    status_msg = AsyncMock()
    msg.reply_text = AsyncMock(return_value=status_msg)

    with patch("app.handlers.download.upload_to_gofile", new_callable=AsyncMock) as mock_upload:
        mock_upload.return_value = ({"status": "ok", "data": {"downloadPage": "https://gofile.io/d/xyz"}}, 0.5)
        await gfup_cmd(app, msg)

        mock_upload.assert_called_once()
        status_msg.edit_text.assert_called()
        assert "GoFile Upload Complete" in status_msg.edit_text.call_args[0][0]
