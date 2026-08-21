from __future__ import annotations

import logging
from pathlib import Path

import aiohttp
from mega.client import MegaNzClient
from mega.download import DownloadResults
from mega.filesystem import FileSystem

from ..config import settings
from ..utils.sorting import natural_path_sort_key

log = logging.getLogger(__name__)

MEGA_DOMAINS = ("mega.nz", "mega.co.nz", "mega.io")


def is_mega_url(url: str) -> bool:
    """Check if the provided string or URL is a MEGA URL."""
    if not url:
        return False
    clean_url = url.strip()
    if clean_url.startswith("mega:"):
        return True
    return any(domain in clean_url for domain in MEGA_DOMAINS)


class MegaClient:
    """Wrapper around mega.client.MegaNzClient for MEGA operations."""

    def __init__(
        self,
        session: aiohttp.ClientSession | None = None,
        user_agent: str | None = None,
    ) -> None:
        self._client = MegaNzClient(session=session, user_agent=user_agent)

    @property
    def raw_client(self) -> MegaNzClient:
        return self._client

    @property
    def logged_in(self) -> bool:
        return self._client.logged_in

    async def login(
        self,
        email: str | None = None,
        password: str | None = None,
        user_id: int | str | None = None,
    ) -> None:
        """Log into MEGA account using provided credentials, user credentials, global settings, or anonymous fallback."""
        if self._client.logged_in:
            return

        target_email = email
        target_password = password

        if not target_email or not target_password:
            if user_id:
                from .auth import get_user_mega_credentials
                u_email, u_pass = get_user_mega_credentials(user_id)
                if u_email and u_pass:
                    target_email = u_email
                    target_password = u_pass

        if not target_email or not target_password:
            target_email = settings.mega_email
            target_password = settings.mega_password

        if target_email and target_password:
            log.info("Logging into MEGA account (%s)...", target_email)
            await self._client.login(target_email, target_password)
        else:
            log.info("Logging into MEGA with a temporary anonymous account...")
            await self._client.login()

    async def ensure_logged_in(
        self,
        email: str | None = None,
        password: str | None = None,
        user_id: int | str | None = None,
    ) -> None:
        """Ensure client is logged into a MEGA session before performing operations."""
        if not self._client.logged_in:
            await self.login(email, password, user_id=user_id)

    async def close(self) -> None:
        """Close the underlying client session."""
        try:
            await self._client.aclose()
        except Exception as e:
            log.debug("Error closing MegaClient: %s", e)

    async def aclose(self) -> None:
        await self.close()

    async def __aenter__(self) -> MegaClient:
        return self

    async def __aexit__(self, exc_type, exc_val, exc_tb) -> None:
        await self.close()

    def parse_url(self, url: str):
        """Parse a MEGA URL into PublicURLInfo."""
        clean_url = url.strip()
        clean_url = clean_url.removeprefix("mega:")
        return self._client.parse_url(clean_url)

    async def get_public_filesystem(
        self, public_handle: str, public_key: str
    ) -> FileSystem:
        """Get the filesystem representation of a public folder."""
        await self.ensure_logged_in()
        return await self._client.get_public_filesystem(public_handle, public_key)

    async def download_public_file(
        self,
        public_handle: str,
        public_key: str,
        output_dir: str | Path | None = None,
    ) -> Path:
        """Download a single public file."""
        await self.ensure_logged_in()
        return await self._client.download_public_file(
            public_handle, public_key, output_dir=output_dir
        )

    async def download_public_folder(
        self,
        public_handle: str,
        public_key: str,
        output_dir: str | Path | None = None,
        root_id: str | None = None,
    ) -> DownloadResults:
        """Download a public folder preserving directory structure in serial natural sorted order."""
        await self.ensure_logged_in()
        fs = await self.get_public_filesystem(public_handle, public_key)

        base_path = Path(output_dir or ".")
        folder_url = f"https://mega.nz/folder/{public_handle}#{public_key}"

        nodes = list(fs.files_from(root_id))
        nodes.sort(key=lambda n: natural_path_sort_key(fs.relative_path(n.id)))

        results: dict[str, Path | Exception] = {}
        for file in nodes:
            web_url = f"{folder_url}/file/{file.id}"
            output_path = base_path / fs.relative_path(file.id)
            try:
                file_info = await self._client._core.request_file_info(file.id, public_handle)
                result = await self._client._core.download_file(file_info, file._crypto, output_path)
                results[file.id] = result
            except Exception as exc:
                log.error("Unable to download %s to '%s': %s", web_url, output_path, exc)
                results[file.id] = exc

        res = DownloadResults.split(results)
        if results and len(res.success) == 0 and len(res.fails) > 0:
            first_err = next(iter(res.fails.values()))
            raise first_err
        return res

    async def download_url(
        self,
        url: str,
        output_dir: str | Path | None = None,
    ) -> DownloadResults:
        """Download a public file or folder by URL in serial natural sorted order."""
        await self.ensure_logged_in()
        clean_url = url.strip()
        clean_url = clean_url.removeprefix("mega:")
        info = self.parse_url(clean_url)
        if not info.is_folder:
            try:
                result = await self.download_public_file(info.public_handle, info.public_key, output_dir)
            except Exception as exc:
                result = exc
            res = DownloadResults.split({info.public_handle: result})
            if isinstance(result, Exception):
                raise result
            return res

        return await self.download_public_folder(
            info.public_handle, info.public_key, output_dir=output_dir, root_id=info.selected_node
        )
