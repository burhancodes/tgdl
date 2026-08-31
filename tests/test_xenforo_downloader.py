from __future__ import annotations

import asyncio
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.downloader.xenforo import (
    DownloadResult,
    XenForoDownloader,
    XenForoDownloadError,
    download_xenforo_post,
    get_cookies_path,
    get_user_cookies_path,
    is_simpcity_url,
    is_xenforo_url,
    load_user_cookies_text,
    run_with_progress,
)


def test_is_xenforo_url():
    assert is_xenforo_url("https://simpcity.su/threads/model-name.12345/")
    assert is_xenforo_url("https://simpcity.cr/threads/model-name.12345/page-2")
    assert is_xenforo_url("https://simpcity.is/threads/model-name.12345/post-67890")
    assert is_xenforo_url("https://simpcity.cz/posts/67890/")
    assert is_xenforo_url("xenforo:https://someforum.com/threads/item.100/")
    assert is_xenforo_url("simpcity:https://simpcity.top/threads/item.100/")
    assert is_xenforo_url("forum:https://otherforum.org/threads/post.1/")
    assert is_xenforo_url("fpd:https://simpcity.su/threads/abc.1/")
    assert not is_xenforo_url("https://google.com/search?q=test")
    assert not is_xenforo_url("https://pixeldrain.com/u/abc123")


def test_is_simpcity_url():
    assert is_simpcity_url("https://simpcity.su/threads/model.123/")
    assert is_simpcity_url("https://simpcity.cr/threads/model.123/")
    assert is_simpcity_url("simpcity:https://simpcity.st/threads/model.123/")
    assert not is_simpcity_url("https://otherforum.com/threads/model.123/")


def test_cookie_paths(tmp_path: Path):
    with patch("app.downloader.xenforo.core.settings") as mock_settings:
        mock_settings.auth_dir = tmp_path / "auth"
        user_cookies = get_user_cookies_path(123456)
        assert user_cookies == tmp_path / "auth" / "123456" / "cookies.txt"

        # When no file exists
        assert get_cookies_path(123456) is None

        # Create user cookies file
        user_cookies.parent.mkdir(parents=True, exist_ok=True)
        user_cookies.write_text("# Netscape cookies\nsimpcity.su\tTRUE\t/\tTRUE\t0\txf_user\tuser_token\n")

        assert get_cookies_path(123456) == user_cookies
        content = load_user_cookies_text(123456)
        assert content is not None
        assert "xf_user" in content


@pytest.mark.asyncio
async def test_xenforo_downloader_scrape():
    downloader = XenForoDownloader(dest_dir=Path("/tmp/test"), user_id=999, passwords=["testpass"])

    mock_rpc_response = {
        "threadTitle": "Sample Thread",
        "url": "https://simpcity.su/threads/sample.12345/",
        "totalPosts": 1,
        "totalResources": 1,
        "posts": [
            {
                "postId": "12345",
                "postNumber": "1",
                "pageNumber": 1,
                "spoilers": ["testpass"],
                "resources": [
                    {
                        "host": "Bunkr",
                        "originalUrl": "https://bunkr.cr/v/sample",
                        "resolvedUrl": "https://cdn.cr/file.mp4?token=abc",
                        "filename": "sample_video.mp4",
                        "folderName": "Sample Thread/Post #1",
                    }
                ],
            }
        ],
    }

    with patch("app.downloader.xenforo.core._rpc_call", new_callable=AsyncMock) as mock_rpc:
        mock_rpc.return_value = mock_rpc_response
        with patch("app.downloader.xenforo.core.start_magnetio_daemon", new_callable=AsyncMock):
            res = await downloader.scrape("https://simpcity.su/threads/sample.12345/")

            assert res["threadTitle"] == "Sample Thread"
            assert len(res["posts"]) == 1
            mock_rpc.assert_called_once()
            call_args = mock_rpc.call_args[0]
            assert call_args[0] == "xenforo.scrape"
            assert call_args[1]["url"] == "https://simpcity.su/threads/sample.12345/"
            assert call_args[1]["user_id"] == "999"
            assert call_args[1]["passwords"] == ["testpass"]


@pytest.mark.asyncio
async def test_xenforo_downloader_download(tmp_path: Path):
    downloader = XenForoDownloader(dest_dir=tmp_path, user_id=123)

    mock_scrape = {
        "threadTitle": "Cool Model",
        "posts": [
            {
                "postId": "1001",
                "postNumber": "1",
                "pageNumber": 1,
                "resources": [
                    {
                        "host": "Bunkr",
                        "resolvedUrl": "https://example.com/video1.mp4",
                        "filename": "video1.mp4",
                        "folderName": "Cool Model/Post #1",
                    },
                    {
                        "host": "JPGX",
                        "resolvedUrl": "https://example.com/photo1.jpg",
                        "filename": "photo1.jpg",
                        "folderName": "Cool Model/Post #1",
                    },
                ],
            }
        ],
    }

    class MockStreamReader:
        def __init__(self, content: bytes):
            self._content = content
            self._yielded = False

        async def iter_chunked(self, size: int):
            if not self._yielded:
                self._yielded = True
                yield self._content

    class MockResponse:
        def __init__(self, url: str, content: bytes, status: int = 200):
            self.url = url
            self.status = status
            self.headers = {"Content-Length": str(len(content)), "Content-Type": "application/octet-stream"}
            self.content = MockStreamReader(content)

        async def __aenter__(self):
            return self

        async def __aexit__(self, exc_type, exc, tb):
            pass

    def mock_get(url, **kwargs):
        if "video1.mp4" in url:
            return MockResponse(url, b"fake_mp4_binary_content")
        return MockResponse(url, b"fake_jpg_binary_content")

    mock_session = MagicMock()
    mock_session.get.side_effect = mock_get
    mock_session.__aenter__ = AsyncMock(return_value=mock_session)
    mock_session.__aexit__ = AsyncMock(return_value=None)

    progress_calls = []

    def on_progress(count: int, filename: str | None, current_url: str | None):
        progress_calls.append((count, filename, current_url))

    downloader.on_progress = on_progress

    with patch.object(downloader, "scrape", new_callable=AsyncMock) as mock_scrape_call:
        mock_scrape_call.return_value = mock_scrape
        with patch("aiohttp.ClientSession", return_value=mock_session):
            with patch("app.downloader.xenforo.core.is_url_private_ip", new_callable=AsyncMock, return_value=False):
                result = await downloader.download("https://simpcity.su/threads/cool-model.123/")

                assert result.ok is True
                assert len(result.files) == 2
                assert len(progress_calls) == 2

                file1 = tmp_path / "Cool Model" / "Post #1" / "video1.mp4"
                file2 = tmp_path / "Cool Model" / "Post #1" / "photo1.jpg"

                assert file1.exists()
                assert file1.read_bytes() == b"fake_mp4_binary_content"
                assert file2.exists()
                assert file2.read_bytes() == b"fake_jpg_binary_content"


@pytest.mark.asyncio
async def test_run_with_progress_and_helper(tmp_path: Path):
    mock_result = DownloadResult(ok=True, files=[tmp_path / "file1.mp4"])

    with patch("app.downloader.xenforo.core.XenForoDownloader.download", new_callable=AsyncMock) as mock_dl:
        mock_dl.return_value = mock_result
        res = await run_with_progress(
            "https://simpcity.su/threads/sample.123/",
            tmp_path,
            extra_args=["--password", "secret123", "--max-pages", "2"],
            user_id=555,
        )

        assert res.ok is True
        assert len(res.files) == 1
        mock_dl.assert_called_once()
