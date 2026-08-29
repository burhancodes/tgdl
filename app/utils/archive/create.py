from __future__ import annotations

import asyncio
import logging
import os
import shutil
import zipfile
from pathlib import Path

import patoolib
from patoolib.util import PatoolError

from ...config import settings
from ..sorting import natural_path_sort_key

log = logging.getLogger(__name__)


async def create_archive_async(
    folder_path: Path,
    output_archive: Path,
    archive_format: str = "zip",
    password: str | None = None
) -> bool:
    """Creates a compressed archive from folder_path using patoolib.create_archive.

    Returns True if creation succeeded, False otherwise.
    """
    folder_path = Path(folder_path)
    output_archive = Path(output_archive)
    output_archive.parent.mkdir(parents=True, exist_ok=True)

    if not folder_path.exists():
        log.error("Source path %s does not exist for archive creation", folder_path)
        return False

    fmt = archive_format.lower().lstrip("-")
    if not output_archive.name.lower().endswith(f".{fmt}"):
        output_archive = output_archive.parent / f"{output_archive.name}.{fmt}"

    # Collect files to archive
    if folder_path.is_dir():
        filenames = [str(p) for p in folder_path.rglob("*") if p.is_file()]
    else:
        filenames = [str(folder_path)]

    if not filenames:
        log.warning("No files found in %s to archive", folder_path)
        return False

    log.info("Creating %s archive at %s using patool", fmt, output_archive)

    def _create() -> None:
        kwargs = {
            "verbosity": -1
        }
        if password:
            kwargs["password"] = password

        # patoolib.create_archive(archive, filenames, ...)
        # Pass files relative to folder_path parent if dir
        patoolib.create_archive(str(output_archive), filenames, **kwargs)

    try:
        await asyncio.to_thread(_create)
        return output_archive.exists() and output_archive.stat().st_size > 0
    except PatoolError as pe:
        log.warning("patool creation failed (%s), attempting fallback: %s", fmt, pe)
    except Exception as e:
        log.warning("patool creation error (%s), attempting fallback: %s", fmt, e)

    # Fallback 1: 7z CLI if available
    cmd7z = shutil.which("7z") or shutil.which("7zz") or shutil.which("7za")
    if cmd7z and folder_path.is_dir():
        try:
            type_flag = "-tzip" if fmt == "zip" else "-t7z"
            proc = await asyncio.create_subprocess_exec(
                cmd7z, "a", type_flag, "-y", str(output_archive), ".",
                cwd=str(folder_path),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE
            )
            await proc.communicate()
            if proc.returncode == 0 and output_archive.exists():
                return True
        except Exception as e:
            log.warning("7z fallback failed: %s", e)

    # Fallback 2: python zipfile if zip format
    if fmt == "zip" and folder_path.is_dir():
        try:
            def _zipfile_create():
                with zipfile.ZipFile(output_archive, "w", zipfile.ZIP_DEFLATED) as zf:
                    for root, _, files in os.walk(folder_path):
                        for file in files:
                            full_path = Path(root) / file
                            arcname = full_path.relative_to(folder_path)
                            zf.write(full_path, arcname)

            await asyncio.to_thread(_zipfile_create)
            return output_archive.exists() and output_archive.stat().st_size > 0
        except Exception as ze:
            log.exception("Python zipfile fallback failed: %s", ze)

    return False


async def archive_folder_async(
    folder_path: Path,
    archive_format: str = "zip",
    max_part_size_mb: int = 1900,
    mirror_pixeldrain: bool = False,
    job_state = None
) -> tuple[list[Path], list[tuple[str, str]]]:
    """Compresses a folder into single or split .zip / .7z archives.

    If mirror_pixeldrain is True, uploads the archive to Pixeldrain in the background.
    Returns (telegram_file_paths, pixeldrain_links).
    """
    pd_links: list[tuple[str, str]] = []
    if not folder_path.exists() or not folder_path.is_dir():
        log.warning("Path %s is not a directory or does not exist. Skipping archive.", folder_path)
        return ([folder_path] if folder_path.exists() else []), pd_links

    parent_dir = folder_path.parent
    folder_name = folder_path.name
    fmt = archive_format.lower().lstrip("-")
    if fmt not in ("zip", "7z"):
        fmt = "zip"

    output_archive = parent_dir / f"{folder_name}.{fmt}"

    log.info("Archiving folder '%s' into %s format...", folder_name, fmt)

    success = await create_archive_async(folder_path, output_archive, archive_format=fmt)

    if not success or not output_archive.exists():
        log.error("Failed to archive folder %s. Keeping uncompressed files.", folder_path)
        return list(folder_path.rglob("*")), pd_links

    archive_size = output_archive.stat().st_size
    pixeldrain_max_bytes = 10 * 1024 * 1024 * 1024  # 10 GB limit
    limit_bytes = max_part_size_mb * 1024 * 1024
    is_split = archive_size > limit_bytes
    upload_unsplit_to_pd = mirror_pixeldrain and not is_split and archive_size <= pixeldrain_max_bytes
    upload_parts_to_pd = mirror_pixeldrain and is_split

    telegram_archives: list[Path] = []
    if is_split:
        log.info("Archive '%s' (%.2f MB) exceeds %d MB limit. Splitting into volumes for Telegram upload...",
                 output_archive.name, archive_size / (1024 * 1024), max_part_size_mb)

        cmd7z = shutil.which("7z") or shutil.which("7zz") or shutil.which("7za")
        if cmd7z:
            type_flag = "-tzip" if fmt == "zip" else "-t7z"
            parts_prefix = parent_dir / f"{folder_name}_parts.{fmt}"
            proc = await asyncio.create_subprocess_exec(
                cmd7z, "a", type_flag, f"-v{max_part_size_mb}m", "-y", str(parts_prefix), str(output_archive),
                cwd=str(parent_dir),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE
            )
            await proc.communicate()
            if proc.returncode == 0:
                output_archive.unlink(missing_ok=True)
                prefix_name = f"{folder_name}_parts.{fmt}"
                for p in sorted(parent_dir.iterdir(), key=natural_path_sort_key):
                    if p.is_file() and (p.name == prefix_name or p.name.startswith(f"{prefix_name}.")):
                        telegram_archives.append(p)

        if not telegram_archives and shutil.which("split"):
            split_prefix = f"{output_archive.name}."
            proc = await asyncio.create_subprocess_exec(
                "split", "-b", f"{max_part_size_mb}m", "-d", str(output_archive), str(parent_dir / split_prefix),
                cwd=str(parent_dir),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE
            )
            await proc.communicate()
            if proc.returncode == 0:
                output_archive.unlink(missing_ok=True)
                for p in sorted(parent_dir.iterdir(), key=natural_path_sort_key):
                    if p.is_file() and p.name.startswith(split_prefix):
                        telegram_archives.append(p)

    if not telegram_archives:
        telegram_archives = [output_archive]

    if (upload_unsplit_to_pd or upload_parts_to_pd) and telegram_archives:
        files_to_upload = telegram_archives if upload_parts_to_pd else [output_archive]
        log.info(
            "Archive '%s' (%.2f GB) requires mirroring. Spawning background task to upload %d file(s) to Pixeldrain...",
            output_archive.name, archive_size / (1024**3), len(files_to_upload)
        )
        pd_temp_dir = parent_dir / f"{folder_name}_pd_temp"
        try:
            pd_temp_dir.mkdir(parents=True, exist_ok=True)
            copied_files = []
            for p in files_to_upload:
                copied_p = pd_temp_dir / p.name
                await asyncio.to_thread(shutil.copy2, p, copied_p)
                copied_files.append(copied_p)

            async def upload_bg():
                from ...uploader import upload_to_pixeldrain
                domain = settings.pixeldrain_domain or "pixeldrain.com"
                for path in copied_files:
                    try:
                        res, pd_logs = await upload_to_pixeldrain(path, api_key=settings.pixeldrain_api_key, domain=domain)
                        if isinstance(res, dict) and res.get("id"):
                            pd_url = f"https://{domain}/u/{res['id']}"
                            log.info("Successfully mirrored '%s' to Pixeldrain in background: %s", path.name, pd_url)
                            if job_state is not None:
                                job_state.pixeldrain_links.append((path.name, pd_url))
                            else:
                                pd_links.append((path.name, pd_url))
                        else:
                            log.warning("Pixeldrain upload response missing id for %s: %s", path.name, res)
                    except Exception as pe:
                        log.exception("Failed to upload %s to Pixeldrain in background: %s", path.name, pe)

                try:
                    shutil.rmtree(pd_temp_dir, ignore_errors=True)
                except Exception:
                    # expected: temp directory already removed
                    pass

            asyncio.create_task(upload_bg())
        except Exception as e:
            log.exception("Failed to initialize background Pixeldrain upload: %s", e)

    shutil.rmtree(folder_path, ignore_errors=True)
    log.info("Successfully archived '%s' into %d Telegram file(s). Original folder deleted.", folder_name, len(telegram_archives))
    return telegram_archives, pd_links


async def archive_all_folders_in_dir(
    target_dir: Path,
    archive_format: str = "zip",
    mirror_pixeldrain: bool = False,
    job_state = None
) -> tuple[list[Path], list[tuple[str, str]]]:
    """Iterates through target_dir and archives each folder individually.

    If top-level files exist alongside or without subfolders, they are also archived.
    Returns (telegram_file_paths, pixeldrain_links).
    """
    final_paths: list[Path] = []
    all_pd_links: list[tuple[str, str]] = []

    if not target_dir.exists() or not target_dir.is_dir():
        return final_paths, all_pd_links

    subdirs = [p for p in target_dir.iterdir() if p.is_dir()]
    top_files = [p for p in target_dir.iterdir() if p.is_file()]

    for item in subdirs:
        archives, pd_links = await archive_folder_async(
            item, archive_format=archive_format, mirror_pixeldrain=mirror_pixeldrain, job_state=job_state
        )
        final_paths.extend(archives)
        all_pd_links.extend(pd_links)

    if top_files and archive_format:
        top_files_dir = target_dir / "Files"
        top_files_dir.mkdir(exist_ok=True)
        for f in top_files:
            try:
                f.rename(top_files_dir / f.name)
            except Exception as e:
                log.warning("Could not move %s into %s: %s", f.name, top_files_dir.name, e)

        archives, pd_links = await archive_folder_async(
            top_files_dir, archive_format=archive_format, mirror_pixeldrain=mirror_pixeldrain, job_state=job_state
        )
        final_paths.extend(archives)
        all_pd_links.extend(pd_links)

    return final_paths, all_pd_links
