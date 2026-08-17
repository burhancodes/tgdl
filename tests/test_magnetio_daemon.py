from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

import app.downloader.aria2c.torrent.magnetio_daemon as daemon_mod
from app.downloader.aria2c.torrent.magnetio_daemon import (
    _probe_health,
    get_active_rpc_secret,
    get_active_rpc_url,
    get_free_port,
    start_magnetio_daemon,
    stop_magnetio_daemon,
)


def test_get_free_port() -> None:
    port = get_free_port()
    assert isinstance(port, int)
    assert 1024 <= port <= 65535


def test_get_active_rpc_url_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(daemon_mod, "MAGNETIO_URL", None)
    url = get_active_rpc_url()
    assert url is not None
    assert "/rpc" in url


def test_get_active_rpc_secret_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(daemon_mod, "MAGNETIO_SECRET", "custom-secret")
    assert get_active_rpc_secret() == "custom-secret"


@pytest.mark.asyncio
async def test_probe_health_success() -> None:
    mock_resp = MagicMock()
    mock_resp.status = 200
    mock_resp.json = AsyncMock(return_value={"status": "ok", "service": "magnetio-scraper"})
    mock_resp.__aenter__ = AsyncMock(return_value=mock_resp)
    mock_resp.__aexit__ = AsyncMock(return_value=None)

    mock_session = MagicMock()
    mock_session.get = MagicMock(return_value=mock_resp)
    mock_session.__aenter__ = AsyncMock(return_value=mock_session)
    mock_session.__aexit__ = AsyncMock(return_value=None)

    with patch("aiohttp.ClientSession", return_value=mock_session):
        ok = await _probe_health("http://127.0.0.1:8080/rpc", timeout_sec=0.5)
        assert ok is True


@pytest.mark.asyncio
async def test_start_magnetio_daemon_existing_remote(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(daemon_mod, "MAGNETIO_PROC", None)
    monkeypatch.setattr(daemon_mod, "MAGNETIO_URL", None)

    with patch("app.downloader.aria2c.torrent.magnetio_daemon._probe_health", new_callable=AsyncMock) as mock_probe:
        mock_probe.return_value = True
        await start_magnetio_daemon()
        assert daemon_mod.MAGNETIO_URL is not None
        assert daemon_mod.MAGNETIO_PROC is None  # Did not need to spawn subprocess


@pytest.mark.asyncio
async def test_start_and_stop_magnetio_daemon_local(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(daemon_mod, "MAGNETIO_PROC", None)
    monkeypatch.setattr(daemon_mod, "MAGNETIO_URL", None)
    monkeypatch.setattr(daemon_mod, "MAGNETIO_PORT", None)

    mock_proc = MagicMock()
    mock_proc.returncode = None
    mock_proc.wait = AsyncMock(return_value=0)
    mock_proc.terminate = MagicMock()
    mock_proc.kill = MagicMock()

    with (
        patch("app.downloader.aria2c.torrent.magnetio_daemon._probe_health", side_effect=[False, True]),
        patch("shutil.which", return_value="/usr/bin/node"),
        patch("pathlib.Path.exists", return_value=True),
        patch("asyncio.create_subprocess_exec", new_callable=AsyncMock) as mock_subproc,
        patch("builtins.open", MagicMock()),
    ):
        mock_subproc.return_value = mock_proc
        await start_magnetio_daemon()

        assert daemon_mod.MAGNETIO_PORT is not None
        assert daemon_mod.MAGNETIO_URL == f"http://127.0.0.1:{daemon_mod.MAGNETIO_PORT}/rpc"
        assert daemon_mod.MAGNETIO_PROC == mock_proc

        await stop_magnetio_daemon()
        assert daemon_mod.MAGNETIO_PROC is None
        assert daemon_mod.MAGNETIO_URL is None
