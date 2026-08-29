from __future__ import annotations

import json
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...db import Job
    from ..state import JobState

from .messaging import format_size, make_progress_bar
from ...downloader.gofile import is_gofile_url


def make_marquee_bar(width: int = 10) -> str:
    import time
    pos = int(time.time() * 2) % (width * 2 - 2)
    if pos >= width:
        pos = (width * 2 - 2) - pos
    bar = ["○"] * width
    bar[pos] = "●"
    return "".join(bar)

from urllib.parse import parse_qs, unquote, urlparse


def format_magnet_display(magnet_url: str, max_len: int = 24) -> str:
    cleaned = magnet_url.strip()
    display_name = ""
    try:
        if "?" in cleaned:
            qs = parse_qs(cleaned.split("?", 1)[1])
            if qs.get("dn"):
                display_name = unquote(qs["dn"][0])
            elif qs.get("xt"):
                xt_val = qs["xt"][0]
                display_name = f"magnet:{xt_val}"
    except Exception:
        pass

    if not display_name:
        display_name = cleaned

    if len(display_name) > max_len:
        display_name = f"{display_name[:max_len-3]}..."

    return f"[{display_name}]({cleaned})"

def shorten_url_text(url: str, max_len: int = 24) -> str:
    cleaned = url.strip()
    if not (cleaned.startswith("http://") or cleaned.startswith("https://")):
        if len(cleaned) > max_len:
            return f"{cleaned[:max_len-3]}..."
        return cleaned

    try:
        parsed = urlparse(cleaned)
        domain = parsed.netloc or (parsed.path.split("/")[0] if parsed.path else "")
        path = parsed.path.rstrip("/")
        filename = path.split("/")[-1] if path else ""

        if len(cleaned) <= max_len:
            return cleaned

        if filename:
            short = f"{domain}/.../{filename}"
            if len(short) <= max_len:
                return short
            ext = Path(filename).suffix
            stem = Path(filename).stem
            avail = max_len - len(domain) - len(ext) - 7
            if avail >= 4:
                short_fn = f"{stem[:avail]}..{ext}"
            else:
                short_fn = f"{filename[:8]}..{ext}" if len(filename) > 10 else filename
            short = f"{domain}/.../{short_fn}"
            if len(short) > max_len:
                short = f"{short[:max_len-3]}..."
            return short

        short = f"{domain}{path}"
        if len(short) > max_len:
            return f"{short[:max_len-3]}..."
        return short
    except Exception:
        if len(cleaned) > max_len:
            return f"{cleaned[:max_len-3]}..."
        return cleaned


def format_url_display(url_json: str, current_url: str | None = None) -> str:
    def _clean(u: str) -> str:
        u_str = str(u).strip()
        if u_str.startswith("direct:"):
            return u_str[len("direct:"):]
        if u_str.startswith("mirror:"):
            return u_str[len("mirror:"):]
        return u_str

    def _format_single(u: str, suffix: str = "") -> str:
        clean_u = _clean(u)
        suffix_str = f" {suffix}" if suffix else ""
        if clean_u.startswith("http://") or clean_u.startswith("https://"):
            short_text = shorten_url_text(clean_u, max_len=24)
            return f"[{short_text}]({clean_u}){suffix_str}"
        else:
            short_text = shorten_url_text(clean_u, max_len=24)
            return f"`{short_text}`{suffix_str}"

    try:
        urls = json.loads(url_json)
        if isinstance(urls, list):
            clean_urls = [_clean(u) for u in urls if str(u).strip()]
            if current_url:
                clean_curr = _clean(current_url)
                if clean_curr in clean_urls:
                    idx = clean_urls.index(clean_curr) + 1
                    return _format_single(clean_curr, f"(Item {idx} of {len(clean_urls)})")
                return _format_single(clean_curr)
            if len(clean_urls) == 1:
                return _format_single(clean_urls[0])
            if len(clean_urls) > 1:
                return _format_single(clean_urls[0], f"(+ {len(clean_urls) - 1} more)")
    except Exception:
        pass

    target_u = current_url or url_json
    return _format_single(target_u)


def compile_split_prompt_text(job_id: str, url_or_target: str, is_torrent: bool = False, is_unzip: bool = False) -> str:
    if is_torrent:
        title = f"**Torrent Job #{job_id} Registered**"
        target_label = "Target"
    elif is_unzip:
        title = f"**Job #{job_id} Registered**"
        target_label = "Archive"
    else:
        title = f"**Job #{job_id} Registered**"
        target_label = "URL"

    display = format_url_display(url_or_target) if not (is_torrent or is_unzip) else url_or_target
    return (
        f"> {title}\n"
        f"> • **__{target_label}__**: {display}\n\n"
        "__Do you want to split files larger than 2GB for this job?__"
    )


def compile_queued_status_text(job_id: str, url: str, args_display: str) -> str:
    cleaned_url = url
    if url.startswith("[") and url.endswith("]"):
        try:
            import json
            parsed = json.loads(url)
            if parsed and isinstance(parsed, list):
                cleaned_url = parsed[0]
        except Exception:
            pass

    is_torrent = (
        cleaned_url.startswith("magnet:") or
        cleaned_url.startswith("torrent:") or
        cleaned_url.endswith(".torrent") or
        "magnet:?xt=" in cleaned_url
    )
    if is_torrent:
        if cleaned_url.startswith("torrent:"):
            torrent_path = cleaned_url[len("torrent:"):]
            name = Path(torrent_path).name
            return (
                f"**Task #{job_id} Queued**\n"
                f"> • **__Torrent__**: __`{name}`__\n"
                f"> • **__Engine__**: __`aria2c`__"
            )
        else:
            magnet_display = format_magnet_display(cleaned_url)
            return (
                f"**Task #{job_id} Queued**\n"
                f"> • **__Magnet__**: {magnet_display}\n"
                f"> • **__Engine__**: __`aria2c`__"
            )

    is_patch = (
        cleaned_url.startswith("patch:") or
        '"patch:' in cleaned_url or
        "['patch:" in cleaned_url
    )
    if is_patch:
        return (
            f"**APK Patch Job Queued** `#${job_id}`\n"
            f"> • **__Engine__**: __`APKEditor + tgpatcher + apksigner`__\n"
            f"> • **__Status__**: __`Waiting for available worker...`__"
        )

    is_gdrive = (
        cleaned_url.startswith("gdrive:") or
        cleaned_url.startswith("gd2tg:") or
        "drive.google.com" in cleaned_url or
        "docs.google.com" in cleaned_url
    )
    if is_gdrive:
        gdrive_disp = cleaned_url
        for prefix in ("gdrive:", "gd2tg:"):
            gdrive_disp = gdrive_disp.removeprefix(prefix)
        gdrive_disp = gdrive_disp[:50] + "..." if len(gdrive_disp) > 50 else gdrive_disp
        return (
            f"**Task #{job_id} Queued**\n"
            f"> • **__Type__**: __`Google Drive Download`__\n"
            f"> • **__Engine__**: __`Google Drive API`__\n"
            f"> • **__Link__**: __`{gdrive_disp}`__{args_display}"
        )

    is_mega = (
        cleaned_url.startswith("mega:") or
        '"mega:' in cleaned_url or
        "['mega:" in cleaned_url or
        "mega.nz" in cleaned_url or
        "mega.co.nz" in cleaned_url or
        "mega.io" in cleaned_url
    )
    if is_mega:
        mega_disp = cleaned_url
        mega_disp = mega_disp.removeprefix("mega:")
        mega_disp = mega_disp[:50] + "..." if len(mega_disp) > 50 else mega_disp
        return (
            f"**Task #{job_id} Queued**\n"
            f"> • **__Type__**: __`Mega.nz Download`__\n"
            f"> • **__Engine__**: __`Mega API`__\n"
            f"> • **__Link__**: __`{mega_disp}`__{args_display}"
        )

    is_gofile = (
        cleaned_url.startswith("gofile:") or
        cleaned_url.startswith("gf:") or
        cleaned_url.startswith("gf2tg:") or
        cleaned_url.startswith("gfdl:") or
        is_gofile_url(cleaned_url)
    )
    if is_gofile:
        gf_disp = cleaned_url
        for pfx in ("gofile:", "gf:", "gf2tg:", "gfdl:"):
            gf_disp = gf_disp.removeprefix(pfx)
        gf_disp = gf_disp[:50] + "..." if len(gf_disp) > 50 else gf_disp
        return (
            f"**Task #{job_id} Queued**\n"
            f"> • **__Type__**: __`GoFile Download`__\n"
            f"> • **__Engine__**: __`GoFile Bypass`__\n"
            f"> • **__Link__**: __`{gf_disp}`__{args_display}"
        )


    is_direct = (
        cleaned_url.startswith("direct:") or
        '"direct:' in cleaned_url or
        "['direct:" in cleaned_url or
        "direct:" in url
    )

    engine_name = "Direct HTTP Downloader" if is_direct else "gallery-dl"

    return (
        f"**Task #{job_id} Queued**\n"
        f"> • **__URL__**: {format_url_display(url)}{args_display}\n"
        f"> • **__Engine__**: __`{engine_name}`__"
    )


def compile_unzip_download_status_text(job_id: str, filename: str, current: int, total: int) -> str:
    pct = current * 100.0 / total if total > 0 else 0.0
    bar = make_progress_bar(pct)
    return (
        f"**Task #{job_id} Active**\n\n"
        f"> • **Archive**: `{filename}`\n"
        f"> • **Progress**: `{pct:.1f}%` `[{bar}]`\n"
        f"> • **Downloaded**: `{format_size(current)} / {format_size(total)}`"
    )


def compile_archive_prompt_text(job_id: str, filename: str) -> str:
    return (
        f"**Archive Handling Prompt**\n\n"
        f"> • **File**: `{filename}`\n\n"
        "> __Choose whether to upload the archive file only or extract its contents and upload both:__"
    )


def compile_archive_choice_status_text(job_id: str, filename: str, choice_str: str) -> str:
    return (
        f"**Archive Action Confirmed**\n\n"
        f"> • **File**: `{filename}`\n"
        f"> • **Selection**: `{choice_str}`\n\n"
        "> __Processing selected operation...__"
    )


def compile_conversion_prompt_text(job_id: str, filename: str) -> str:
    return (
        f"**Media Conversion Prompt**\n\n"
        f"> • **File**: `{filename}`\n\n"
        "> __Convert video to MKV container first or upload original document?__"
    )


def compile_audio_conversion_prompt_text(job_id: str, filename: str) -> str:
    return (
        f"**Audio Processing Prompt**\n\n"
        f"> • **File**: `{filename}`\n\n"
        "> __Convert audio to MP3 with Pedalboard mastering or upload original?__"
    )


def compile_conversion_choice_status_text(job_id: str, filename: str, choice_str: str) -> str:
    return (
        f"**Media Action Confirmed**\n\n"
        f"> • **File**: `{filename}`\n"
        f"> • **Selection**: `{choice_str}`"
    )


def compile_extraction_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Archive Extraction Active**\n\n"
        f"> • **Extracting**: `{filename}`"
    )


def compile_conversion_running_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Media Remuxing Active**\n\n"
        f"> • **Remuxing**: `{filename}` to MKV container..."
    )


def compile_audio_conversion_running_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Audio Processing Active**\n\n"
        f"> • **Mastering**: `{filename}` to MP3..."
    )


def compile_conversion_failed_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Conversion Failed**\n\n"
        f"> • **Notice**: Failed to transcode `{filename}`. Uploading original file."
    )


def compile_audio_conversion_failed_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Audio Processing Failed**\n\n"
        f"> • **Notice**: Failed to process `{filename}`. Uploading original file."
    )


def compile_extraction_failed_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Extraction Failed**\n\n"
        f"> • **Notice**: Failed to extract `{filename}`."
    )


def compile_extraction_success_status_text(job_id: str, filename: str) -> str:
    return (
        f"**Archive Extracted**\n\n"
        f"> • **Notice**: Successfully extracted `{filename}`."
    )


def format_user_args(args_raw: str | None) -> str:
    if not args_raw:
        return ""
    try:
        data = json.loads(args_raw)
        if isinstance(data, list):
            user_flags = [str(item) for item in data if item]
            return " ".join(user_flags)
        elif isinstance(data, dict):
            user_flags = []
            fmt = data.get("archive_format")
            if fmt:
                user_flags.append(f"-{fmt}")
            if data.get("mirror_pixeldrain"):
                user_flags.append("-pd")
            extra = data.get("custom_args") or data.get("extra_args")
            if isinstance(extra, list):
                user_flags.extend([str(x) for x in extra])
            elif isinstance(extra, str) and extra:
                user_flags.append(extra)
            return " ".join(user_flags)
    except Exception:
        return str(args_raw).strip()
    return ""


def compile_job_status_text(job: Job, job_state: JobState) -> str:
    cleaned_url = job.url
    if job.url.startswith("[") and job.url.endswith("]"):
        try:
            parsed = json.loads(job.url)
            if parsed and isinstance(parsed, list):
                cleaned_url = parsed[0]
        except Exception:
            pass

    is_patch = (
        cleaned_url.startswith("patch:") or
        '"patch:' in cleaned_url or
        "['patch:" in cleaned_url
    )
    if is_patch:
        orig_filename = "app.apk"
        if job.args:
            try:
                a_data = json.loads(job.args)
                if isinstance(a_data, dict) and a_data.get("original_filename"):
                    orig_filename = a_data["original_filename"]
            except Exception:
                pass

        if orig_filename.lower().endswith(".apk"):
            out_name = f"{orig_filename[:-4]}_patched.apk"
        else:
            out_name = f"{orig_filename}_patched.apk"

        stage = job_state.current_download_file or "Initializing patch pipeline..."
        marquee = make_marquee_bar()

        lines = [
            f"**APK Patching Pipeline** `#${job.id}`\n",
            f"> • **__Input File__**: __`{orig_filename}`__",
            f"> • **__Output Target__**: __`{out_name}`__",
            f"> • **__Engine__**: __`APKEditor + tgpatcher`__",
            f"> • **__Signer__**: __`JKS Keystore`__\n",
        ]

        if not job_state.downloader_done.is_set():
            dl_bytes_str = format_size(job_state.total_downloaded_bytes)
            lines.append(
                f"**Pipeline Metrics**\n"
                f"> • **__Stage__**: __`{stage}`__\n"
                f"> • **__State__**: __`[{marquee}]`__\n"
                f"> • **__Processed Size__**: __`{dl_bytes_str}`__"
            )
        else:
            lines.append(
                f"**Uploader Metrics**\n"
                f"> • **__File__**: __`{out_name}`__\n"
                f"> • **__Status__**: __`Uploading patched APK to Telegram... [{marquee}]`__"
            )

        return "\n".join(lines)

    is_torrent = (
        cleaned_url.startswith("magnet:") or
        cleaned_url.startswith("torrent:") or
        cleaned_url.endswith(".torrent") or
        "magnet:?xt=" in cleaned_url
    )

    is_gdrive = (
        cleaned_url.startswith("gdrive:") or
        cleaned_url.startswith("gd2tg:") or
        "drive.google.com" in cleaned_url or
        "docs.google.com" in cleaned_url
    )

    is_mega = (
        cleaned_url.startswith("mega:") or
        '"mega:' in cleaned_url or
        "['mega:" in cleaned_url or
        "mega.nz" in cleaned_url or
        "mega.co.nz" in cleaned_url or
        "mega.io" in cleaned_url
    )

    is_gofile = (
        cleaned_url.startswith("gofile:") or
        cleaned_url.startswith("gf:") or
        cleaned_url.startswith("gf2tg:") or
        cleaned_url.startswith("gfdl:") or
        is_gofile_url(cleaned_url)
    )

    is_direct = (
        cleaned_url.startswith("direct:") or
        '"direct:' in cleaned_url or
        "['direct:" in cleaned_url or
        job.url.startswith("direct:") or
        '"direct:' in job.url
    )

    split_str = "Enabled (2GB)" if job.split_large_files else "Disabled"

    lines = [
        f"**Task #{job.id} Details**\n",
        f"> • **__Status__**: __`{job.status.upper()}`__",
    ]

    if is_torrent:
        torrent_name = getattr(job_state, "torrent_name", None)
        if torrent_name:
            lines.append(f"> • **__Torrent__**: __`{torrent_name}`__")
        elif cleaned_url.startswith("torrent:"):
            torrent_path = cleaned_url[len("torrent:"):]
            name = Path(torrent_path).name
            lines.append(f"> • **__File__**: __`{name}`__")
        else:
            magnet_display = format_magnet_display(cleaned_url)
            lines.append(f"> • **__Magnet__**: {magnet_display}")
    else:
        cur_url = getattr(job_state, "current_download_url", None)
        lines.append(f"> • **__Target__**: {format_url_display(job.url, current_url=cur_url)}")
        user_args_str = format_user_args(job.args)
        if user_args_str:
            lines.append(f"> • **__Args__**: __`{user_args_str}`__")

    lines.append(f"> • **__Auto Split__**: __`{split_str}`__\n")

    if not job_state.downloader_done.is_set():
        dl_speed_str = format_size(job_state.download_speed)
        dl_bytes_str = format_size(job_state.total_downloaded_bytes)
        dl_tool = "Google Drive API" if is_gdrive else ("Mega API" if is_mega else ("GoFile Bypass" if is_gofile else ("aria2c" if is_torrent else ("Direct HTTP Downloader" if is_direct else ("Pyrogram Downloader" if cleaned_url.startswith("unzip:") else "gallery-dl")))))

        if is_gdrive or is_mega or is_gofile:
            marquee = make_marquee_bar()
            lines.append(
                f"**Downloader Metrics**\n"
                f"> • **__Engine__**: __`{dl_tool}`__\n"
                f"> • **__State__**: __`[{marquee}]`__\n"
                f"> • **__Downloaded__**: __`{dl_bytes_str}`__\n"
                f"> • **__Speed__**: __`{dl_speed_str}/s`__"
            )
            if job_state.current_download_file:
                lines.append(f"> • **__Current__**: __`{job_state.current_download_file}`__")

        elif is_torrent:
            bar = make_progress_bar(job_state.download_pct)
            seeders = getattr(job_state, "torrent_seeders", 0)
            peers = getattr(job_state, "torrent_peers", 0)
            lines.append(
                f"**Downloader Metrics**\n"
                f"> • **__Engine__**: __`{dl_tool}`__\n"
                f"> • **__Progress__**: __`{job_state.download_pct:.1f}%` `[{bar}]`__\n"
                f"> • **__Downloaded__**: __`{dl_bytes_str}`__\n"
                f"> • **__Speed__**: __`{dl_speed_str}/s`__\n"
                f"> • **__Swarm__**: __`Seeders: {seeders} | Leechers: {peers}`__"
            )
        elif is_direct:
            dl_pct = job_state.download_pct
            total_bytes = getattr(job_state, "total_expected_bytes", 0)
            if dl_pct > 0 or total_bytes > 0:
                bar = make_progress_bar(dl_pct)
                total_str = format_size(total_bytes) if total_bytes > 0 else "Unknown"
                lines.append(
                    f"**Downloader Metrics**\n"
                    f"> • **__Engine__**: __`{dl_tool}`__\n"
                    f"> • **__Progress__**: __`{dl_pct:.1f}%` `[{bar}]`__\n"
                    f"> • **__Downloaded__**: __`{dl_bytes_str} / {total_str}`__\n"
                    f"> • **__Speed__**: __`{dl_speed_str}/s`__"
                )
            else:
                marquee = make_marquee_bar()
                lines.append(
                    f"**Downloader Metrics**\n"
                    f"> • **__Engine__**: __`{dl_tool}`__\n"
                    f"> • **__State__**: __`[{marquee}]`__\n"
                    f"> • **__Downloaded__**: __`{dl_bytes_str}`__\n"
                    f"> • **__Speed__**: __`{dl_speed_str}/s`__"
                )
            if job_state.current_download_file:
                lines.append(f"> • **__Current__**: __`{job_state.current_download_file}`__")
        else:
            dl_pct = job_state.download_pct
            total_bytes = getattr(job_state, "total_expected_bytes", 0)
            if dl_pct > 0 or total_bytes > 0:
                bar = make_progress_bar(dl_pct)
                total_str = format_size(total_bytes) if total_bytes > 0 else "Unknown"
                lines.append(
                    f"**Downloader Metrics**\n"
                    f"> • **__Engine__**: __`{dl_tool}`__\n"
                    f"> • **__Progress__**: __`{dl_pct:.1f}%` `[{bar}]`__\n"
                    f"> • **__Downloaded__**: __`{dl_bytes_str} / {total_str}`__\n"
                    f"> • **__Speed__**: __`{dl_speed_str}/s`__"
                )
            else:
                marquee = make_marquee_bar()
                lines.append(
                    f"**Downloader Metrics**\n"
                    f"> • **__Engine__**: __`{dl_tool}`__\n"
                    f"> • **__Downloaded Count__**: __`{job_state.download_count}`__\n"
                    f"> • **__State__**: __`[{marquee}]`__\n"
                    f"> • **__Total Size__**: __`{dl_bytes_str}`__\n"
                    f"> • **__Speed__**: __`{dl_speed_str}/s`__"
                )
            if job_state.current_download_file:
                lines.append(f"> • **__Current__**: __`{job_state.current_download_file}`__")

    if getattr(job_state, "is_archiving", False):
        if not job_state.downloader_done.is_set():
            lines.append("")
        import shutil
        archiver_tool = "7z" if shutil.which("7z") else ("zip" if shutil.which("zip") else "zipfile")
        fmt = getattr(job_state, "archive_format", "ZIP") or "ZIP"
        lines.append(
            f"**Archive Compression**\n"
            f"> • **__Engine__**: __`{archiver_tool}`__\n"
            f"> • **__Format__**: __`{fmt.upper()}`__\n"
            f"> • **__Status__**: __`Compressing downloaded folder structure...`__"
        )
    elif getattr(job_state, "is_converting", False):
        if not job_state.downloader_done.is_set():
            lines.append("")
        conv_file = getattr(job_state, "conversion_file", "media file")
        lines.append(
            f"**Media Processing**\n"
            f"> • **__Engine__**: __`PyAV`__\n"
            f"> • **__Converting__**: __`{conv_file}`__\n"
            f"> • **__Status__**: __`Remuxing to MKV container...`__"
        )

    web_mirror_info = getattr(job_state, "web_mirror_info", None)
    uploaded_count = getattr(job_state, "sent", 0)
    if isinstance(uploaded_count, (set, list)):
        uploaded_count = len(uploaded_count)
    skipped_count = len(getattr(job_state, "skipped", [])) if isinstance(getattr(job_state, "skipped", []), (list, set)) else getattr(job_state, "skipped", 0)

    if web_mirror_info:
        if not job_state.downloader_done.is_set() or getattr(job_state, "is_archiving", False) or getattr(job_state, "is_converting", False):
            lines.append("")

        lines.append("**Mirror Metrics**")
        host_labels = [
            ("gofile", "GoFile"),
            ("fileditch", "FileDitch"),
            ("pixeldrain", "Pixeldrain")
        ]
        for idx, (key, label) in enumerate(host_labels):
            tree = "├" if idx < len(host_labels) - 1 else "└"
            info = web_mirror_info.get(key, {})
            st = info.get("status", "pending")
            url = info.get("url") or info.get("link")
            if st == "done" and url:
                lines.append(f"> {tree} **__[{label}]({url})__**: `{url}`")
            elif st == "uploading":
                pct = info.get("pct", 0.0)
                spd = info.get("speed", 0.0)
                bar = make_progress_bar(pct)
                spd_str = f"{format_size(spd)}/s" if spd > 0 else "0 B/s"
                lines.append(f"> {tree} **__{label}__**: `{bar}` **{pct:.1f}%** ({spd_str})")
            elif st == "skipped":
                lines.append(f"> {tree} **__{label}__**: `Skipped (>10GB)`")
            elif st == "failed":
                err = info.get("error", "Failed")
                lines.append(f"> {tree} **__{label}__**: `{err}`")
            else:
                lines.append(f"> {tree} **__{label}__**: `Pending`")
    else:
        is_uploader_active = (
            not job_state.uploader_done.is_set() and (
                job_state.downloader_done.is_set() or
                uploaded_count > 0 or
                len(job_state.uploaded_filenames) > 0 or
                len(job_state.uploading_files) > 0 or
                bool(job_state.current_upload_file)
            )
        )

        if is_uploader_active:
            if not job_state.downloader_done.is_set() or getattr(job_state, "is_archiving", False) or getattr(job_state, "is_converting", False):
                lines.append("")

            ul_speed_str = format_size(job_state.upload_speed)
            total_files_disp = job.total_files if job.total_files > 0 else 'Calculating'

            lines.append(
                f"**Uploader Metrics**\n"
                f"> • **__Engine__**: __`Pyrogram Uploader`__\n"
                f"> • **__Files Uploaded__**: __`{uploaded_count} / {total_files_disp}`__\n"
                f"> • **__Files Skipped__**: __`{skipped_count}`__"
            )
            if job_state.current_upload_file:
                bar = make_progress_bar(job_state.current_upload_pct)
                lines.append(
                    f"> • **__Current File__**: __`{job_state.current_upload_file}`__\n"
                    f"> • **__Progress__**: __`{job_state.current_upload_pct:.1f}%` `[{bar}]`__\n"
                    f"> • **__Speed__**: __`{ul_speed_str}/s`__"
                )

    from ...config import settings

    if settings.show_system_stats_on_job_card:
        if lines:
            lines.append("")
        from .status_utils import (
            get_readable_file_size,
            get_readable_time,
            get_system_stats_snapshot,
        )
        stats = get_system_stats_snapshot()
        cpu_str = f"{stats['cpu_percent']}%"
        ram_str = f"{stats['ram_percent']}%"
        disk_str = get_readable_file_size(stats['disk_free_bytes'])
        uptime_str = get_readable_time(stats['uptime_seconds'])
        net_sent_str = get_readable_file_size(stats['net_sent_bytes_since_start'])
        net_recv_str = get_readable_file_size(stats['net_recv_bytes_since_start'])

        lines.append(
            f"**Server Resources**\n"
            f"> • **__CPU__**: __`{cpu_str}`__ | **__RAM__**: __`{ram_str}`__\n"
            f"> • **__Disk Free__**: __`{disk_str}`__\n"
            f"> • **__Server Uptime__**: __`{uptime_str}`__\n"
            f"> • **__NET I/O__**: __`↑ {net_sent_str} | ↓ {net_recv_str}`__"
        )

    return "\n".join(lines)
