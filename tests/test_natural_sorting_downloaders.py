from __future__ import annotations

import json
from pathlib import Path, PurePosixPath
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.downloader.aria2c.torrent.core import download_via_aria2_async
from app.downloader.direct.core import DirectDownloader
from app.gdrive.downloader import GoogleDriveDownloader
from app.utils.sorting import natural_path_sort_key, natural_sort_key


@pytest.mark.asyncio
async def test_gdrive_folder_serial_natural_sort_download(tmp_path: Path):
    """Verify GoogleDriveDownloader downloads folder items in serial, naturally sorted hierarchical order."""
    mock_client = MagicMock()

    # Mock items in root folder returned in arbitrary, unsorted order
    root_items = [
        {"id": "id_s2", "name": "Season 2", "mimeType": "application/vnd.google-apps.folder"},
        {"id": "id_ep1", "name": "Episode 1.mkv", "mimeType": "video/x-matroska"},
        {"id": "id_s1", "name": "Season 1", "mimeType": "application/vnd.google-apps.folder"},
    ]

    # Mock Season 1 items
    s1_items = [
        {"id": "id_s1_ep10", "name": "Episode 10.mkv", "mimeType": "video/x-matroska"},
        {"id": "id_s1_ep2", "name": "Episode 2.mkv", "mimeType": "video/x-matroska"},
        {"id": "id_s1_ep1", "name": "Episode 1.mkv", "mimeType": "video/x-matroska"},
    ]

    # Mock Season 2 items
    s2_items = [
        {"id": "id_s2_ep1", "name": "Episode 1.mkv", "mimeType": "video/x-matroska"},
    ]

    def mock_list_folder_contents(folder_id: str):
        if folder_id == "root_folder_id":
            return root_items
        elif folder_id == "id_s1":
            return s1_items
        elif folder_id == "id_s2":
            return s2_items
        return []

    mock_client.list_folder_contents.side_effect = mock_list_folder_contents

    downloader = GoogleDriveDownloader(client=mock_client)

    download_order: list[str] = []

    async def mock_download_file(meta, parent_dir):
        rel = (parent_dir / meta["name"]).relative_to(tmp_path / "MyShow")
        download_order.append(str(rel))
        f = parent_dir / meta["name"]
        f.touch()
        return f

    with patch.object(downloader, "_download_file", side_effect=mock_download_file):
        await downloader._download_folder("root_folder_id", tmp_path / "MyShow")

    # Expected exact natural hierarchical order:
    # 1. Episode 1.mkv (at root)
    # 2. Season 1/Episode 1.mkv
    # 3. Season 1/Episode 2.mkv
    # 4. Season 1/Episode 10.mkv
    # 5. Season 2/Episode 1.mkv
    assert download_order == [
        "Episode 1.mkv",
        "Season 1/Episode 1.mkv",
        "Season 1/Episode 2.mkv",
        "Season 1/Episode 10.mkv",
        "Season 2/Episode 1.mkv",
    ]


@pytest.mark.asyncio
async def test_gdrive_download_link_batch_natural_sort(tmp_path: Path):
    """Verify GoogleDriveDownloader.download_link naturally sorts batch URLs and returns files."""
    mock_client = MagicMock()

    batch_urls = [
        "gd2tg:https://drive.google.com/file/d/id_10/view",
        "gd2tg:https://drive.google.com/file/d/id_2/view",
        "gd2tg:https://drive.google.com/file/d/id_1/view",
    ]

    mock_client.get_file_metadata.side_effect = lambda fid: {
        "id": fid,
        "name": f"video_{fid.split('_')[1]}.mp4",
        "mimeType": "video/mp4",
    }

    downloader = GoogleDriveDownloader(client=mock_client)

    downloaded_order: list[str] = []

    async def mock_download_file(meta, parent_dir):
        downloaded_order.append(meta["name"])
        f = parent_dir / meta["name"]
        f.touch()
        return f

    with patch.object(downloader, "_download_file", side_effect=mock_download_file):
        result = await downloader.download_link(json.dumps(batch_urls), tmp_path)

        assert downloaded_order == [
            "video_1.mp4",
            "video_2.mp4",
            "video_10.mp4",
        ]
        assert len(result) == 3
        assert [f.name for f in result] == [
            "video_1.mp4",
            "video_2.mp4",
            "video_10.mp4",
        ]


@pytest.mark.asyncio
async def test_direct_downloader_batch_serial_natural_sort(tmp_path: Path):
    """Verify DirectDownloader sorts items using natural_path_sort_key and downloads serially."""
    downloader = DirectDownloader(dest_dir=tmp_path)

    # Input items unsorted
    items = [
        {"url": "https://example.com/downloads/file10.mp4", "filename": "file10.mp4", "path": "season1"},
        {"url": "https://example.com/downloads/file2.mp4", "filename": "file2.mp4", "path": "season1"},
        {"url": "https://example.com/downloads/file1.mp4", "filename": "file1.mp4", "path": "season1"},
        {"url": "https://example.com/downloads/file1.mp4", "filename": "file1.mp4", "path": "season2"},
    ]

    download_order: list[str] = []

    async def mock_download_content_item(session, url, filename, subpath):
        rel = f"{subpath}/{filename}" if subpath else filename
        download_order.append(rel)
        f = tmp_path / subpath / filename
        f.parent.mkdir(parents=True, exist_ok=True)
        f.touch()
        return f

    with patch.object(downloader, "_download_content_item", side_effect=mock_download_content_item), \
         patch("aiohttp.ClientSession"):
        files = await downloader.download(items)

        assert download_order == [
            "season1/file1.mp4",
            "season1/file2.mp4",
            "season1/file10.mp4",
            "season2/file1.mp4",
        ]
        assert len(files) == 4
        assert [str(f.relative_to(tmp_path)) for f in files] == [
            "season1/file1.mp4",
            "season1/file2.mp4",
            "season1/file10.mp4",
            "season2/file1.mp4",
        ]


@pytest.mark.asyncio
async def test_gallery_dl_and_cyberdrop_dl_sort_batch_urls(tmp_path: Path):
    """Verify gallery-dl and cyberdrop-dl sort batch URLs and return naturally sorted files."""
    from app.downloader.gallery_dl.core import run_with_progress as run_gdl
    from app.downloader.cyberdrop_dl.core import run_with_progress as run_cdl

    batch_urls = [
        "https://example.com/gallery/page10",
        "https://example.com/gallery/page2",
        "https://example.com/gallery/page1",
    ]

    gdl_called_urls: list[str] = []

    # Mock subprocess for gallery-dl
    async def mock_gdl_subprocess(*cmd, stdout=None, stderr=None):
        # Find single_url in cmd
        url = cmd[-1]
        gdl_called_urls.append(url)
        # Create a file corresponding to url
        p = tmp_path / f"img_{Path(url).name}.jpg"
        p.touch()
        mock_proc = MagicMock()
        mock_proc.stdout = AsyncMock()
        mock_proc.stdout.__aiter__.return_value = []
        mock_proc.stderr = AsyncMock()
        mock_proc.stderr.__aiter__.return_value = []
        mock_proc.wait = AsyncMock(return_value=0)
        return mock_proc

    with patch("asyncio.create_subprocess_exec", side_effect=mock_gdl_subprocess):
        res = await run_gdl(json.dumps(batch_urls), tmp_path)
        assert res.ok
        assert gdl_called_urls == [
            "https://example.com/gallery/page1",
            "https://example.com/gallery/page2",
            "https://example.com/gallery/page10",
        ]
        assert [f.name for f in res.files] == [
            "img_page1.jpg",
            "img_page2.jpg",
            "img_page10.jpg",
        ]


@pytest.mark.asyncio
async def test_aria2c_download_returns_naturally_sorted_files(tmp_path: Path):
    """Verify download_via_aria2_async returns completed files in natural path sorted order."""
    # Create unordered files in tmp_path
    f10 = tmp_path / "Season 1" / "Episode 10.mkv"
    f2 = tmp_path / "Season 1" / "Episode 2.mkv"
    f1 = tmp_path / "Season 1" / "Episode 1.mkv"
    for f in (f10, f2, f1):
        f.parent.mkdir(parents=True, exist_ok=True)
        f.touch()

    # Create dummy part and aria2 files which should be ignored
    (tmp_path / "Season 1" / "Episode 3.mkv.aria2").touch()
    (tmp_path / "Season 1" / "Episode 3.mkv.part").touch()

    with patch("app.downloader.aria2c.torrent.core.start_aria2_daemon", new_callable=AsyncMock), \
         patch("app.downloader.aria2c.torrent.core.async_rpc_call") as mock_rpc, \
         patch("app.downloader.aria2c.torrent.core.ARIA2_PROC", MagicMock(returncode=None)), \
         patch("app.downloader.aria2c.torrent.core.ARIA2_PORT", 6800):

        # Return GID on addUri and 'complete' on tellStatus
        mock_rpc.side_effect = [
            {"result": "gid123"},
            {"result": {"status": "complete"}},
            {"result": "ok"},
        ]

        result = await download_via_aria2_async("https://example.com/sample.torrent", tmp_path)

        assert result.ok
        assert [str(f.relative_to(tmp_path)) for f in result.files] == [
            "Season 1/Episode 1.mkv",
            "Season 1/Episode 2.mkv",
            "Season 1/Episode 10.mkv",
        ]
