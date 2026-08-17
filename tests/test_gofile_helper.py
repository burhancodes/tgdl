from __future__ import annotations

import hashlib
import json
import os
import time
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from app.downloader.gallery_dl.gofile_helper import (
    DEFAULT_FALLBACK_SALT,
    DEFAULT_USER_AGENT,
    _extract_salt_via_js_runtime,
    _extract_salt_via_regex,
    fetch_gofile_salt,
    get_browser_user_agent,
    patch_gallery_dl_gofile,
    sync_gofile_salt,
    update_all_gdl_configs,
    update_gdl_conf_gofile,
)


def test_get_browser_user_agent():
    ua = get_browser_user_agent()
    assert isinstance(ua, str)
    assert len(ua) > 10
    assert "Mozilla" in ua

    # Test environment variable override
    with patch.dict(os.environ, {"GOFILE_USER_AGENT": "CustomUserAgent/1.0"}):
        assert get_browser_user_agent() == "CustomUserAgent/1.0"


def test_extract_salt_from_js():
    sample_js = """
    function generateWT(token) {
        return _sha256(navigator.userAgent + "::" + navigator.language + "::" + token + "::1000::12af056dacea0b");
    }
    """
    salt_js = _extract_salt_via_js_runtime(sample_js)
    assert salt_js == "12af056dacea0b"

    salt_rgx = _extract_salt_via_regex(sample_js)
    assert salt_rgx == "12af056dacea0b"


def test_update_gdl_conf_gofile(tmp_path: Path):
    conf_file = tmp_path / "test_gallery-dl.conf"
    initial_content = json.dumps(
        {
            "extractor": {
                "user-agent": "auto",
                "gofile": {
                    "api-token": None,
                    "recursive": True,
                    "salt": "old_salt_123",
                },
            }
        },
        indent=4,
    )
    conf_file.write_text(initial_content, encoding="utf-8")

    test_salt = "12af056dacea0b"
    test_ua = "Mozilla/5.0 (CustomUA) Chrome/151.0.0.0"

    success = update_gdl_conf_gofile(conf_file, salt=test_salt, user_agent=test_ua)
    assert success is True

    updated_text = conf_file.read_text(encoding="utf-8")
    data = json.loads(updated_text)

    assert data["extractor"]["gofile"]["salt"] == test_salt
    assert data["extractor"]["user-agent"] == test_ua


def test_patch_gallery_dl_gofile():
    test_salt = "feedbeef123456"
    patch_gallery_dl_gofile(test_salt)

    assert os.environ.get("GOFILE_WT_SALT") == test_salt

    import gallery_dl.extractor.gofile as gofile_mod

    extractor = gofile_mod.GofileFolderExtractor(MagicMock())
    extractor.session = MagicMock()
    extractor.session.headers = {"User-Agent": DEFAULT_USER_AGENT}
    extractor.api_token = "mock_token"

    token = extractor._generate_website_token("en-US")
    assert isinstance(token, str)
    assert len(token) == 64  # SHA-256 hex string

    tw = int(time.time() / 14400)
    expected_data = f"{DEFAULT_USER_AGENT}::en-US::mock_token::{tw}::{test_salt}"
    expected_hash = hashlib.sha256(expected_data.encode()).hexdigest()
    assert token == expected_hash


def test_sync_gofile_salt(tmp_path: Path):
    with patch("app.downloader.gallery_dl.gofile_helper.fetch_gofile_salt", return_value="12af056dacea0b"):
        salt, results = sync_gofile_salt(auto_fetch=True)
        assert salt == "12af056dacea0b"
        assert isinstance(results, dict)


def test_update_all_gdl_configs_with_user_dirs(tmp_path: Path):
    auth_dir = tmp_path / "auth"
    user_1_dir = auth_dir / "12345"
    user_2_dir = auth_dir / "67890"
    user_1_dir.mkdir(parents=True, exist_ok=True)
    user_2_dir.mkdir(parents=True, exist_ok=True)

    template_content = json.dumps({"extractor": {"gofile": {"salt": None}}}, indent=4)
    (user_1_dir / "gallery-dl.conf").write_text(template_content, encoding="utf-8")
    (user_2_dir / "gallery-dl.conf").write_text(template_content, encoding="utf-8")

    with patch("app.config.settings.auth_dir", auth_dir):
        results = update_all_gdl_configs(salt="new_salt_abc")
        assert len(results) >= 2
        user_1_conf = json.loads((user_1_dir / "gallery-dl.conf").read_text(encoding="utf-8"))
        user_2_conf = json.loads((user_2_dir / "gallery-dl.conf").read_text(encoding="utf-8"))
        assert user_1_conf["extractor"]["gofile"]["salt"] == "new_salt_abc"
        assert user_2_conf["extractor"]["gofile"]["salt"] == "new_salt_abc"


@pytest.mark.asyncio
async def test_gdlconf_sync_callback_execution():
    from unittest.mock import AsyncMock
    from app.handlers.gdlconf import register_gdlconf_handlers

    mock_app = MagicMock()
    callback_handler = None
    command_handler = None

    def mock_on_callback_query(f):
        def decorator(fn):
            nonlocal callback_handler
            callback_handler = fn
            return fn
        return decorator

    def mock_on_message(f):
        def decorator(fn):
            nonlocal command_handler
            command_handler = fn
            return fn
        return decorator

    mock_app.on_callback_query = mock_on_callback_query
    mock_app.on_message = mock_on_message

    register_gdlconf_handlers(mock_app)
    assert callback_handler is not None

    mock_query = AsyncMock()
    mock_query.data = "gdlconf:sync_gofile"
    mock_query.from_user.id = 12345
    mock_query.message.chat.id = 12345

    with patch("app.handlers.gdlconf.sync_gofile_salt", return_value=("12af056dacea0b", {})):
        await callback_handler(mock_app, mock_query)
        mock_query.answer.assert_called()
        mock_query.message.reply_text.assert_called()


@pytest.mark.asyncio
async def test_ipv6_downloader_configuration(tmp_path: Path):

    import socket
    from app.config import settings
    from app.downloader.gallery_dl.core import _build_cmd
    from app.downloader.direct.core import get_aiohttp_connector

    with patch.object(settings, "force_ipv6", True), patch.object(settings, "source_address", None):
        cmd = _build_cmd(["https://example.com/item"], tmp_path)
        assert "--source-address" in cmd
        idx = cmd.index("--source-address")
        assert cmd[idx + 1] == "::"

        conn = get_aiohttp_connector()
        assert conn._family == socket.AF_INET6

    with patch.object(settings, "force_ipv6", False), patch.object(settings, "source_address", "2001:db8::1"):
        cmd = _build_cmd(["https://example.com/item"], tmp_path)
        assert "--source-address" in cmd
        idx = cmd.index("--source-address")
        assert cmd[idx + 1] == "2001:db8::1"



