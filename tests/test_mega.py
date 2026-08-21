from __future__ import annotations

import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest

from app.handlers.download import _parse_flags
from app.mega import MegaClient, MegaDownloader, is_mega_url


def test_is_mega_url():
    """Verify is_mega_url identifies MEGA URLs correctly."""
    assert is_mega_url("https://mega.nz/file/ABC#123") is True
    assert is_mega_url("https://mega.nz/folder/XYZ#456") is True
    assert is_mega_url("https://mega.co.nz/#!ABC!123") is True
    assert is_mega_url("https://mega.io/file/ABC#123") is True
    assert is_mega_url("mega:https://mega.nz/file/ABC#123") is True
    assert is_mega_url("https://example.com/file.zip") is False
    assert is_mega_url("") is False


def test_mega_client_parse_url():
    """Verify MegaClient parses MEGA URLs correctly."""
    client = MegaClient()
    info = client.parse_url("https://mega.nz/file/samplehandle#samplekey")
    assert info.is_folder is False
    assert info.public_handle == "samplehandle"
    assert info.public_key == "samplekey"

    folder_info = client.parse_url("mega:https://mega.nz/folder/folderhandle#folderkey")
    assert folder_info.is_folder is True
    assert folder_info.public_handle == "folderhandle"
    assert folder_info.public_key == "folderkey"


@pytest.mark.asyncio
async def test_mega_client_login():
    """Verify MegaClient calls login on MegaNzClient."""
    mock_raw_client = MagicMock()
    mock_raw_client.logged_in = False
    mock_raw_client.login = AsyncMock()

    client = MegaClient()
    client._client = mock_raw_client

    await client.ensure_logged_in()
    mock_raw_client.login.assert_awaited_once_with()



def test_parse_mega_flags():
    """Verify _parse_flags parses /mega flags (-m, -tg, -uz, -p, urls)."""
    tokens = ["/mega", "-m", "-tg", "-uz", "-p", "secret123", "https://mega.nz/file/ABC#123"]
    is_m, is_tg, uz, pwd, urls = _parse_flags(tokens)
    assert is_m is True
    assert is_tg is True
    assert uz is True
    assert pwd == "secret123"
    assert urls == ["https://mega.nz/file/ABC#123"]


@pytest.mark.asyncio
async def test_mega_downloader_progress_callback():
    """Verify MegaDownloader invokes progress_callback when progress hooks trigger."""
    progress_updates = []

    def on_progress(downloaded: int, speed: float, filename: str):
        progress_updates.append((downloaded, speed, filename))

    mock_client = MagicMock(spec=MegaClient)
    mock_client.download_url = AsyncMock()

    downloader = MegaDownloader(client=mock_client, progress_callback=on_progress)

    with tempfile.TemporaryDirectory() as tmpdir:
        dest = Path(tmpdir)

        # Test progress hook factory directly
        factory = downloader._create_hook_factory()
        with factory("test_file.bin", 1000, "DOWN") as hook:
            hook(500)
            hook(500)

        assert len(progress_updates) == 2
        assert progress_updates[0][0] == 500
        assert progress_updates[0][2] == "test_file.bin"
        assert progress_updates[1][0] == 1000

        # Test download_link dispatch
        await downloader.download_link("https://mega.nz/file/ABC#123", dest)
        mock_client.download_url.assert_awaited_once_with("https://mega.nz/file/ABC#123", output_dir=dest)


def test_user_mega_credentials_save_get_delete():
    """Verify saving, retrieving, and deleting per-user MEGA credentials."""
    from app.mega import (
        delete_user_mega_credentials,
        get_user_mega_credentials,
        save_user_mega_credentials,
    )

    user_id = 999888
    delete_user_mega_credentials(user_id)

    email, pwd = get_user_mega_credentials(user_id)
    assert email is None
    assert pwd is None

    save_user_mega_credentials(user_id, "user99@mega.test", "SecretPass123")
    email, pwd = get_user_mega_credentials(user_id)
    assert email == "user99@mega.test"
    assert pwd == "SecretPass123"

    deleted = delete_user_mega_credentials(user_id)
    assert deleted is True
    email, pwd = get_user_mega_credentials(user_id)
    assert email is None
    assert pwd is None


def test_natural_sorting_utilities():
    """Verify natural sorting key and natural path sorting key."""
    from pathlib import PurePosixPath
    from app.utils.sorting import natural_path_sort_key, natural_sort_key

    # Test string natural sorting
    names = ["file10.txt", "file1.txt", "file2.txt", "file20.txt", "file3.txt"]
    sorted_names = sorted(names, key=natural_sort_key)
    assert sorted_names == ["file1.txt", "file2.txt", "file3.txt", "file10.txt", "file20.txt"]

    # Test hierarchical path natural sorting
    paths = [
        PurePosixPath("folder2/file10.txt"),
        PurePosixPath("folder1/file10.txt"),
        PurePosixPath("folder1/file2.txt"),
        PurePosixPath("folder10/file1.txt"),
        PurePosixPath("folder1/file1.txt"),
    ]
    sorted_paths = sorted(paths, key=natural_path_sort_key)
    assert [str(p) for p in sorted_paths] == [
        "folder1/file1.txt",
        "folder1/file2.txt",
        "folder1/file10.txt",
        "folder2/file10.txt",
        "folder10/file1.txt",
    ]


@pytest.mark.asyncio
async def test_mega_client_download_public_folder_serial_order():
    """Verify MegaClient.download_public_folder downloads files in strictly serial, naturally sorted order."""
    from pathlib import PurePosixPath
    from unittest.mock import AsyncMock, MagicMock

    # Create mock nodes with random IDs in unordered fashion
    class DummyNode:
        def __init__(self, node_id: str, rel_path: str):
            self.id = node_id
            self._crypto = MagicMock()
            self.rel_path = PurePosixPath(rel_path)

    nodes = [
        DummyNode("id_10", "subfolder/file10.mp4"),
        DummyNode("id_1", "subfolder/file1.mp4"),
        DummyNode("id_2", "subfolder/file2.mp4"),
        DummyNode("id_root", "root_file.mp4"),
    ]

    mock_fs = MagicMock()
    mock_fs.files_from.return_value = nodes
    mock_fs.relative_path.side_effect = lambda nid: next(n.rel_path for n in nodes if n.id == nid)

    client = MegaClient()
    client._client = MagicMock()
    client._client.logged_in = True
    client.get_public_filesystem = AsyncMock(return_value=mock_fs)

    # Track order of download calls
    downloaded_order = []

    async def mock_download_file(file_info, crypto, output_path):
        downloaded_order.append(str(output_path))
        return output_path

    client._client._core.request_file_info = AsyncMock(return_value=MagicMock())
    client._client._core.download_file = AsyncMock(side_effect=mock_download_file)

    with tempfile.TemporaryDirectory() as tmpdir:
        dest = Path(tmpdir)
        results = await client.download_public_folder("sample_handle", "sample_key", output_dir=dest)

        assert len(results.success) == 4
        assert len(results.fails) == 0

        # Verify exact natural sorted order from beginning to end
        assert downloaded_order == [
            str(dest / "root_file.mp4"),
            str(dest / "subfolder/file1.mp4"),
            str(dest / "subfolder/file2.mp4"),
            str(dest / "subfolder/file10.mp4"),
        ]


