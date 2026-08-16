from __future__ import annotations

import asyncio
import json
import logging
import shutil
import uuid
from pathlib import Path

from pyrogram import Client, filters
from pyrogram.types import (
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
)

from ..auth import authorized_filter
from ..config import settings
from ..manager import queue_manager, store
from ..manager.status.compiler import compile_queued_status_text
from ..manager.status.messaging import safe_send
from ..uploader import upload_to_fileditch, upload_to_gofile, upload_to_pixeldrain

log = logging.getLogger(__name__)


def _parse_flags(text_tokens: list[str]) -> tuple[bool, bool, bool, str | None, list[str]]:
    is_mirror = False
    upload_tg = False
    unzip = False
    password = None
    urls = []

    i = 1
    while i < len(text_tokens):
        token = text_tokens[i].strip()
        if not token:
            i += 1
            continue

        low = token.lower()
        if low in ("-m", "-mirror", "--mirror"):
            is_mirror = True
        elif low in ("-tg", "--tg"):
            upload_tg = True
        elif low in ("-uz", "-unzip", "--unzip"):
            unzip = True
        elif low in ("-p", "-pass", "--pass", "--password"):
            if i + 1 < len(text_tokens) and not text_tokens[i + 1].startswith("-"):
                password = text_tokens[i + 1].strip()
                i += 1
        elif any(low.startswith(prefix) for prefix in ("-p=", "-pass=", "--pass=", "--password=")):
            password = token.split("=", 1)[1].strip() or None
        elif token.startswith(("http://", "https://", "magnet:")):
            urls.append(token)
        i += 1

    return is_mirror, upload_tg, unzip, password, urls


def _parse_aria_flags(text_tokens: list[str]) -> dict[str, Any]:
    options: dict[str, Any] = {}
    flag_map = {
        "-c": "max-connection-per-server",
        "--connections": "max-connection-per-server",
        "-s": "split",
        "--split": "split",
        "--min-split-size": "min-split-size",
        "--max-tries": "max-tries",
        "--retry-wait": "retry-wait",
        "--header": "header",
        "--ua": "user-agent",
        "--referer": "referer",
        "--proxy": "all-proxy",
        "--checksum": "checksum",
        "--out": "out",
        "--speed": "max-download-limit",
    }

    def _set_opt(k: str, v: str) -> None:
        if k == "header":
            options.setdefault("header", [])
            if isinstance(options["header"], list):
                options["header"].append(v)
            else:
                options["header"] = [options["header"], v]
        elif k in options:
            if isinstance(options[k], list):
                options[k].append(v)
            else:
                options[k] = [options[k], v]
        else:
            options[k] = v

    i = 1
    while i < len(text_tokens):
        token = text_tokens[i].strip()
        if not token:
            i += 1
            continue

        matched_key = None
        inline_val = None

        if "=" in token and token.startswith("-"):
            parts = token.split("=", 1)
            flag_candidate = parts[0].lower()
            if flag_candidate in flag_map:
                matched_key = flag_map[flag_candidate]
                inline_val = parts[1]
                _set_opt(matched_key, inline_val)
                i += 1
                continue
            elif flag_candidate == "--opt":
                if "=" in parts[1]:
                    opt_k, opt_v = parts[1].split("=", 1)
                    _set_opt(opt_k.strip(), opt_v.strip())
                i += 1
                continue

        token_low = token.lower()
        if token_low in flag_map:
            matched_key = flag_map[token_low]
            if i + 1 < len(text_tokens) and not text_tokens[i + 1].startswith("-"):
                inline_val = text_tokens[i + 1].strip()
                i += 1
            if matched_key and inline_val is not None:
                _set_opt(matched_key, inline_val)
        elif token_low == "--opt":
            if i + 1 < len(text_tokens) and not text_tokens[i + 1].startswith("-"):
                opt_expr = text_tokens[i + 1].strip()
                i += 1
                if "=" in opt_expr:
                    opt_k, opt_v = opt_expr.split("=", 1)
                    _set_opt(opt_k.strip(), opt_v.strip())

        i += 1

    if "max-download-limit" not in options:
        if settings.global_download_speed_limit and str(settings.global_download_speed_limit).strip().lower() not in ("none", "0", ""):
            options["max-download-limit"] = str(settings.global_download_speed_limit)

    return options


async def _create_and_enqueue_job(
    client: Client,
    chat_id: int,
    target_url: str,
    message: Message,
    display_text: str,
    is_mirror: bool = False,
    upload_tg: bool = False,
    unzip: bool = False,
    password: str | None = None,
    engine: str | None = None,
    aria_options: dict | None = None,
) -> None:
    active_jobs = queue_manager.get_active_jobs_for_chat(chat_id)
    if settings.max_jobs_per_chat > 0 and len(active_jobs) >= settings.max_jobs_per_chat:
        await message.reply_text(
            f"**Queue Limit Reached**: You have {len(active_jobs)} active or queued job(s). "
            f"Maximum allowed per chat is {settings.max_jobs_per_chat}."
        )
        return

    args_dict = {}
    if is_mirror:
        args_dict["is_mirror"] = True
    if upload_tg:
        args_dict["upload_tg"] = True
    if unzip:
        args_dict["unzip"] = True
    if password:
        args_dict["password"] = password
    if engine:
        args_dict["engine"] = engine
    if aria_options:
        args_dict["aria_options"] = aria_options
    args_json = json.dumps(args_dict) if args_dict else None
    job = await store.create_job(chat_id, target_url, split_large_files=1, args=args_json)
    await store.update_progress(job.id, status="queued")
    await queue_manager.add_job(job.id)
    await asyncio.sleep(0.4)

    client_obj = client or getattr(message, "_client", getattr(message, "client", None))
    db_j = await store.get_job(job.id)
    if db_j and db_j.status == "queued" and job.id not in queue_manager.jobs:
        queued_text = compile_queued_status_text(job.id, display_text, "")
        keyboard = InlineKeyboardMarkup([
            [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
        ])
        status_msg = await safe_send(
            client_obj,
            chat_id,
            queued_text,
            reply_markup=keyboard,
            link_preview_options=LinkPreviewOptions(is_disabled=True)
        )
        if status_msg:
            await store.set_status_message(job.id, status_msg.id)

            async def auto_delete_queued():
                await asyncio.sleep(10)
                try:
                    cur_j = await store.get_job(job.id)
                    if cur_j and cur_j.status == "queued" and client_obj:
                        await client_obj.delete_messages(chat_id, status_msg.id)
                except Exception:
                    # expected: queued status message already deleted
                    pass

            asyncio.create_task(auto_delete_queued())


def register_download_handlers(app: Client) -> None:

    @app.on_message(filters.command(["m", "mirror"]) & authorized_filter)
    async def mirror_cmd(client: Client, message: Message) -> None:
        target_url = None
        display_text = "Mirror"

        text_tokens = message.text.split() if message.text else []
        upload_tg = "-tg" in text_tokens

        link_tokens = [
            t.strip() for t in text_tokens[1:]
            if t.strip() not in ("-tg", "-m", "-mirror")
        ]
        raw_link = link_tokens[0] if link_tokens else None

        if message.reply_to_message:
            reply_msg = message.reply_to_message
            media = (
                reply_msg.document
                or reply_msg.video
                or reply_msg.audio
                or reply_msg.photo
                or reply_msg.voice
                or reply_msg.video_note
                or reply_msg.sticker
                or reply_msg.animation
            )
            if media:
                target_url = f"mirror_tg:{reply_msg.chat.id}:{reply_msg.id}"
                file_name = getattr(media, "file_name", None) or f"tg_media_{reply_msg.id}"
                prefix = "Mirror [TG]" if upload_tg else "Mirror"
                display_text = f"{prefix}: Telegram file `{file_name}`"
            elif not raw_link and (reply_msg.text or reply_msg.caption):
                reply_text = reply_msg.text or reply_msg.caption
                for token in reply_text.split():
                    token = token.strip()
                    if token.startswith(("http://", "https://")):
                        raw_link = token
                        break

        if not target_url and raw_link:
            target_url = f"mirror:{raw_link}"
            prefix = "Mirror [TG]" if upload_tg else "Mirror"
            display_text = f"{prefix}: {raw_link}"

        if not target_url:
            await message.reply_text("Provide a URL or reply to a Telegram message with `/m [-tg]` or `/mirror [-tg]`.")
            return

        await _create_and_enqueue_job(
            client, message.chat.id, target_url, message, display_text, is_mirror=True, upload_tg=upload_tg
        )

    @app.on_message(filters.command(["direct", "dl"]) & authorized_filter)
    async def direct_cmd(client: Client, message: Message) -> None:
        text_tokens = message.text.split() if message.text else []
        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)
        urls = []

        if message.reply_to_message:
            reply_msg = message.reply_to_message

            if reply_msg.document and (
                reply_msg.document.file_name.endswith(".txt") or
                (reply_msg.document.mime_type and reply_msg.document.mime_type.startswith("text/"))
            ):
                temp_path = await reply_msg.download()
                if temp_path and Path(temp_path).exists():
                    try:
                        content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")
                        for line in content.splitlines():
                            line = line.strip()
                            if line.startswith(("http://", "https://")):
                                urls.append(line)
                    except Exception as e:
                        log.warning("Failed reading replied txt file for direct_cmd: %s", e)
                    finally:
                        Path(temp_path).unlink(missing_ok=True)

            reply_text = reply_msg.text or reply_msg.caption
            if reply_text and not urls:
                for token in reply_text.split():
                    token = token.strip()
                    if token.startswith(("http://", "https://")):
                        urls.append(token)

        if not urls:
            urls = parsed_urls

        if not urls:
            await message.reply_text(
                "Provide a direct URL or reply to a text/message containing URLs:\n"
                "• `/direct [-m|-mirror] [-tg] [-uz] [-p password] <url>` or `/dl [-uz] [-p password] <url>`\n"
                "• Reply with `/direct [-uz]` or `/dl [-uz]` to a text message or `.txt` file containing URLs."
            )
            return

        urls_json = json.dumps([f"direct:{u}" for u in urls]) if len(urls) > 1 else f"direct:{urls[0]}"
        prefix_parts = []
        if is_mirror:
            prefix_parts.append("mirror")
        if unzip:
            prefix_parts.append("unzip")
        prefix_str = f" [{', '.join(prefix_parts)}]" if prefix_parts else ""
        prefix = f"direct{prefix_str}:"
        display_text = f"{prefix} `{urls[0]}`" if len(urls) == 1 else f"{prefix} `{urls[0]}` (+ {len(urls) - 1} more)"
        await _create_and_enqueue_job(
            client, message.chat.id, urls_json, message, display_text,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password
        )

    @app.on_message(filters.command(["gallerydl", "gdl"]) & authorized_filter)
    async def gdl_cmd(client: Client, message: Message) -> None:
        text_tokens = message.text.split() if message.text else []
        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)
        urls = []

        if message.reply_to_message:
            reply_msg = message.reply_to_message

            if reply_msg.document and (
                reply_msg.document.file_name.endswith(".txt") or
                (reply_msg.document.mime_type and reply_msg.document.mime_type.startswith("text/"))
            ):
                temp_path = await reply_msg.download()
                if temp_path and Path(temp_path).exists():
                    try:
                        content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")
                        for line in content.splitlines():
                            line = line.strip()
                            if line.startswith(("http://", "https://")):
                                urls.append(line)
                    except Exception as e:
                        log.warning("Failed reading replied txt file: %s", e)
                    finally:
                        Path(temp_path).unlink(missing_ok=True)

            reply_text = reply_msg.text or reply_msg.caption
            if reply_text and not urls:
                for token in reply_text.split():
                    token = token.strip()
                    if token.startswith(("http://", "https://")):
                        urls.append(token)

        if not urls:
            urls = parsed_urls

        if not urls:
            await message.reply_text(
                "Provide a URL or reply to a text/message containing URLs:\n"
                "• `/gdl [-m|-mirror] [-tg] [-uz] [-p password] <url>`\n"
                "• Reply with `/gdl [-uz]` to a text message or `.txt` file containing URLs."
            )
            return

        urls_json = json.dumps(urls) if len(urls) > 1 else urls[0]
        prefix_parts = []
        if is_mirror:
            prefix_parts.append("mirror")
        if unzip:
            prefix_parts.append("unzip")
        prefix_str = f" [{', '.join(prefix_parts)}]" if prefix_parts else ""
        prefix = f"gallery-dl{prefix_str}:"
        display_text = f"{prefix} `{urls[0]}`" if len(urls) == 1 else f"{prefix} `{urls[0]}` (+ {len(urls) - 1} more)"
        await _create_and_enqueue_job(
            client, message.chat.id, urls_json, message, display_text,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password
        )

    @app.on_message(filters.command(["cyberdropdl", "cdl"]) & authorized_filter)
    async def cdl_cmd(client: Client, message: Message) -> None:
        text_tokens = message.text.split() if message.text else []
        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)
        urls = []

        if message.reply_to_message:
            reply_msg = message.reply_to_message

            if reply_msg.document and (
                reply_msg.document.file_name.endswith(".txt") or
                (reply_msg.document.mime_type and reply_msg.document.mime_type.startswith("text/"))
            ):
                temp_path = await reply_msg.download()
                if temp_path and Path(temp_path).exists():
                    try:
                        content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")
                        for line in content.splitlines():
                            line = line.strip()
                            if line.startswith(("http://", "https://")):
                                urls.append(line)
                    except Exception as e:
                        log.warning("Failed reading replied txt file for cdl_cmd: %s", e)
                    finally:
                        Path(temp_path).unlink(missing_ok=True)

            reply_text = reply_msg.text or reply_msg.caption
            if reply_text and not urls:
                for token in reply_text.split():
                    token = token.strip()
                    if token.startswith(("http://", "https://")):
                        urls.append(token)

        if not urls:
            urls = parsed_urls

        if not urls:
            await message.reply_text(
                "Provide a URL or reply to a text/message containing URLs:\n"
                "• `/cdl [-m|-mirror] [-tg] [-uz] [-p password] <url>`\n"
                "• Reply with `/cdl [-uz]` to a text message or `.txt` file containing URLs."
            )
            return

        urls_json = json.dumps([f"cdl:{u}" for u in urls]) if len(urls) > 1 else f"cdl:{urls[0]}"
        prefix_parts = []
        if is_mirror:
            prefix_parts.append("mirror")
        if unzip:
            prefix_parts.append("unzip")
        prefix_str = f" [{', '.join(prefix_parts)}]" if prefix_parts else ""
        prefix = f"cyberdrop-dl{prefix_str}:"
        display_text = f"{prefix} `{urls[0]}`" if len(urls) == 1 else f"{prefix} `{urls[0]}` (+ {len(urls) - 1} more)"
        await _create_and_enqueue_job(
            client, message.chat.id, urls_json, message, display_text,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password, engine="cyberdrop-dl"
        )

    @app.on_message(filters.command(["mega", "meganz"]) & authorized_filter)
    async def mega_cmd(client: Client, message: Message) -> None:
        text_tokens = message.text.split() if message.text else []
        user_id = message.from_user.id if message.from_user else message.chat.id

        if len(text_tokens) > 1:
            first_arg = text_tokens[1].strip()
            low_arg = first_arg.lower()

            if low_arg in ("-logout", "logout", "--logout", "-delete", "delete"):
                from ..mega import delete_user_mega_credentials
                deleted = delete_user_mega_credentials(user_id)
                if deleted:
                    await message.reply_text("**MEGA Login Credentials Removed**\nYour user-level account credentials have been deleted. Reverting to anonymous session.")
                else:
                    await message.reply_text("No saved user-level MEGA login credentials found.")
                return

            elif low_arg in ("-account", "account", "--account", "-me", "me", "-status", "status"):
                from ..mega import get_user_mega_credentials
                email, _ = get_user_mega_credentials(user_id)
                if email:
                    await message.reply_text(f"**MEGA Login Status**\n• **User ID**: `{user_id}`\n• **Email**: `{email}`\nSubsequent downloads will use this account.")
                else:
                    await message.reply_text("**MEGA Login Status**\nNo user-level account logged in. Using temporary anonymous session.\n\nUse `/mega -login email:password` to log into your account.")
                return

            elif low_arg in ("-login", "login", "--login") or low_arg.startswith(("-login=", "--login=", "login=")):
                from ..mega import MegaClient, save_user_mega_credentials
                raw_login_str = None
                if "=" in first_arg:
                    raw_login_str = first_arg.split("=", 1)[1].strip()
                elif len(text_tokens) > 2:
                    raw_login_str = text_tokens[2].strip()

                email = None
                password = None
                if raw_login_str:
                    if ":" in raw_login_str:
                        email, password = raw_login_str.split(":", 1)
                    elif len(text_tokens) > 3 and not raw_login_str.startswith("-"):
                        email = raw_login_str
                        password = text_tokens[3].strip()

                if not email or not password:
                    await message.reply_text(
                        "Please provide your MEGA email and password:\n"
                        "• `/mega -login email:password` or `/mega -login email password`\n"
                        "• `/mega -logout` to remove credentials\n"
                        "• `/mega -account` to check login status"
                    )
                    return

                status_msg = await message.reply_text("Authenticating with MEGA servers...")
                try:
                    async with MegaClient() as mega:
                        await mega.login(email.strip(), password.strip())

                    save_user_mega_credentials(user_id, email.strip(), password.strip())
                    await status_msg.edit_text(
                        f"**MEGA Account Logged In**\n"
                        f"• **User ID**: `{user_id}`\n"
                        f"• **Email**: `{email.strip()}`\n\n"
                        f"Saved user credentials. All subsequent `/mega` downloads will automatically use your account."
                    )
                except Exception as e:
                    log.warning("Failed MEGA login for user %s (%s): %s", user_id, email, e)
                    await status_msg.edit_text(f"**MEGA Login Failed**\n{e}")
                return

        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)
        urls = []

        if message.reply_to_message:
            reply_msg = message.reply_to_message

            if reply_msg.document and (
                reply_msg.document.file_name.endswith(".txt") or
                (reply_msg.document.mime_type and reply_msg.document.mime_type.startswith("text/"))
            ):
                temp_path = await reply_msg.download()
                if temp_path and Path(temp_path).exists():
                    try:
                        content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")
                        for line in content.splitlines():
                            line = line.strip()
                            if line.startswith(("http://", "https://")):
                                urls.append(line)
                    except Exception as e:
                        log.warning("Failed reading replied txt file for mega_cmd: %s", e)
                    finally:
                        Path(temp_path).unlink(missing_ok=True)

            reply_text = reply_msg.text or reply_msg.caption
            if reply_text and not urls:
                for token in reply_text.split():
                    token = token.strip()
                    if token.startswith(("http://", "https://")):
                        urls.append(token)

        if not urls:
            urls = parsed_urls

        if not urls:
            await message.reply_text(
                "Provide a MEGA URL or reply to a text/message containing MEGA URLs:\n"
                "• `/mega [-m|-mirror] [-tg] [-uz] [-p password] <mega_url>`\n"
                "• `/mega -login <email:password>` to log into your account\n"
                "• Reply with `/mega [-m] [-tg] [-uz]` to a text message or `.txt` file containing MEGA links."
            )
            return

        urls_json = json.dumps([f"mega:{u}" for u in urls]) if len(urls) > 1 else f"mega:{urls[0]}"
        prefix_parts = []
        if is_mirror:
            prefix_parts.append("mirror")
        if unzip:
            prefix_parts.append("unzip")
        prefix_str = f" [{', '.join(prefix_parts)}]" if prefix_parts else ""
        prefix = f"mega{prefix_str}:"
        display_text = f"{prefix} `{urls[0]}`" if len(urls) == 1 else f"{prefix} `{urls[0]}` (+ {len(urls) - 1} more)"
        await _create_and_enqueue_job(
            client, message.chat.id, urls_json, message, display_text,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password
        )


    @app.on_message(filters.command("tor") & authorized_filter)
    async def tor_cmd(client: Client, message: Message) -> None:
        raw_text = message.text or message.caption or ""
        text_tokens = raw_text.split() if raw_text else []
        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)

        target_url = None

        if message.reply_to_message and message.reply_to_message.document:
            doc = message.reply_to_message.document
            if (doc.file_name and doc.file_name.endswith(".torrent")) or (doc.mime_type and "torrent" in doc.mime_type):
                temp_path = await message.reply_to_message.download()
                if temp_path:
                    torrents_dir = settings.data_dir / "torrents"
                    torrents_dir.mkdir(parents=True, exist_ok=True)
                    dest_path = torrents_dir / f"{uuid.uuid4()}.torrent"
                    try:
                        shutil.move(temp_path, dest_path)
                        target_url = f"torrent:{dest_path.absolute()}"
                    except Exception as e:
                        log.exception("Failed to save replied torrent file")
                        await message.reply_text(f"Failed to save torrent file: {e}")
                        return
                else:
                    await message.reply_text("Failed to download replied torrent file.")
                    return

        if not target_url:
            if not parsed_urls:
                await message.reply_text(
                    "Send a magnet link, reply to a `.torrent` file, or use `/tor [-m|-mirror] [-tg] [-uz] [-p password] <magnet/url>`."
                )
                return

            if len(parsed_urls) > 1:
                await message.reply_text("Please provide only one magnet link or torrent URL per `/tor` command.")
                return

            target_url = parsed_urls[0]

        url_display = target_url
        if target_url.startswith("magnet:"):
            url_display = target_url[:60] + "..." if len(target_url) > 60 else target_url
        elif target_url.startswith("torrent:"):
            url_display = "local torrent file"

        await _create_and_enqueue_job(
            client, message.chat.id, target_url, message, url_display,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password
        )


    @app.on_message(filters.command("aria") & authorized_filter)
    async def aria_cmd(client: Client, message: Message) -> None:
        raw_text = message.text or message.caption or ""
        text_tokens = raw_text.split() if raw_text else []
        is_mirror, upload_tg, unzip, password, parsed_urls = _parse_flags(text_tokens)
        aria_opts = _parse_aria_flags(text_tokens)

        target_url = None

        if message.reply_to_message and message.reply_to_message.document:
            doc = message.reply_to_message.document
            if (doc.file_name and doc.file_name.endswith(".torrent")) or (doc.mime_type and "torrent" in doc.mime_type):
                temp_path = await message.reply_to_message.download()
                if temp_path:
                    torrents_dir = settings.data_dir / "torrents"
                    torrents_dir.mkdir(parents=True, exist_ok=True)
                    dest_path = torrents_dir / f"{uuid.uuid4()}.torrent"
                    try:
                        shutil.move(temp_path, dest_path)
                        target_url = f"torrent:{dest_path.absolute()}"
                    except Exception as e:
                        log.exception("Failed to save replied torrent file for aria_cmd")
                        await message.reply_text(f"Failed to save torrent file: {e}")
                        return
                else:
                    await message.reply_text("Failed to download replied torrent file.")
                    return

        if not target_url:
            urls = []
            if message.reply_to_message:
                reply_msg = message.reply_to_message
                if reply_msg.document and (
                    reply_msg.document.file_name.endswith(".txt") or
                    (reply_msg.document.mime_type and reply_msg.document.mime_type.startswith("text/"))
                ):
                    temp_path = await reply_msg.download()
                    if temp_path and Path(temp_path).exists():
                        try:
                            content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")
                            for line in content.splitlines():
                                line = line.strip()
                                if line.startswith(("http://", "https://", "ftp://", "magnet:")):
                                    urls.append(line)
                        except Exception as e:
                            log.warning("Failed reading replied txt file for aria_cmd: %s", e)
                        finally:
                            Path(temp_path).unlink(missing_ok=True)

                reply_text = reply_msg.text or reply_msg.caption
                if reply_text and not urls:
                    for token in reply_text.split():
                        token = token.strip()
                        if token.startswith(("http://", "https://", "ftp://", "magnet:")):
                            urls.append(token)

            if not urls:
                urls = parsed_urls

            if not urls:
                await message.reply_text(
                    "Provide a URL or magnet link, reply to a `.torrent` file, or use `/aria [flags] <url/magnet>`."
                )
                return

            target_url = urls[0] if len(urls) == 1 else json.dumps(urls)

        check_urls = []
        if target_url.startswith(("http://", "https://", "ftp://")):
            check_urls.append(target_url)
        elif target_url.startswith("["):
            try:
                parsed_list = json.loads(target_url)
                if isinstance(parsed_list, list):
                    for u in parsed_list:
                        if isinstance(u, str) and u.startswith(("http://", "https://", "ftp://")):
                            check_urls.append(u)
            except Exception:
                pass

        if not settings.allow_private_network_urls:
            from ..downloader.direct.core import is_url_private_ip
            for check_u in check_urls:
                if await is_url_private_ip(check_u):
                    log.warning("SSRF protection blocked URL %s (resolves to private/reserved IP)", check_u)
                    await message.reply_text(f"Access to private/internal network URL '{check_u}' is prohibited.")
                    return

        url_display = target_url
        if target_url.startswith("magnet:"):
            url_display = target_url[:60] + "..." if len(target_url) > 60 else target_url
        elif target_url.startswith("torrent:"):
            url_display = "local torrent file"

        prefix_parts = []
        if is_mirror:
            prefix_parts.append("mirror")
        if unzip:
            prefix_parts.append("unzip")
        prefix_str = f" [{', '.join(prefix_parts)}]" if prefix_parts else ""
        display_text = f"aria2{prefix_str}: {url_display}"

        await _create_and_enqueue_job(
            client, message.chat.id, target_url, message, display_text,
            is_mirror=is_mirror, upload_tg=upload_tg, unzip=unzip, password=password,
            engine="aria2", aria_options=aria_opts
        )



    @app.on_message(filters.command("pdup") & authorized_filter)
    async def pdup_cmd(_, message: Message) -> None:
        if not message.reply_to_message:
            await message.reply_text("Please reply to a media message to upload it to Pixeldrain.")
            return

        reply_msg = message.reply_to_message
        if not (reply_msg.document or reply_msg.video or reply_msg.photo or reply_msg.audio or reply_msg.voice):
            await message.reply_text("Replied message does not contain a supported media file.")
            return

        status_msg = await message.reply_text("Downloading media file for Pixeldrain upload...")
        temp_dir = settings.data_dir / "temp_pdup"
        temp_dir.mkdir(parents=True, exist_ok=True)

        file_path_str = await reply_msg.download(file_name=str(temp_dir) + "/")
        if not file_path_str or not Path(file_path_str).exists():
            await status_msg.edit_text("Failed to download media file from Telegram.")
            return

        local_path = Path(file_path_str)
        try:
            await status_msg.edit_text(f"Uploading `{local_path.name}` to Pixeldrain...")
            domain = settings.pixeldrain_domain or "pixeldrain.com"
            res, _ = await upload_to_pixeldrain(
                local_path,
                api_key=settings.pixeldrain_api_key,
                domain=domain
            )

            if isinstance(res, dict) and res.get("id"):
                pd_url = f"https://{domain}/u/{res['id']}"
                await status_msg.edit_text(
                    f"**[Pixeldrain Upload Complete]({pd_url})**\n"
                    f"**File**: `{local_path.name}`",
                    link_preview_options=LinkPreviewOptions(is_disabled=True)
                )
            else:
                err = res.get("error") if isinstance(res, dict) else "Unknown error"
                await status_msg.edit_text(f"Failed to upload to Pixeldrain: {err}")
        except Exception as e:
            log.exception("Error uploading file to Pixeldrain")
            await status_msg.edit_text(f"Pixeldrain upload failed: {e}")
        finally:
            if local_path.exists():
                local_path.unlink(missing_ok=True)

    @app.on_message(filters.command(["gfup", "gofile"]) & authorized_filter)
    async def gfup_cmd(_, message: Message) -> None:
        if not message.reply_to_message:
            await message.reply_text("Please reply to a media message with `/gfup` or `/gofile` to upload it to GoFile.")
            return

        reply_msg = message.reply_to_message
        if not (reply_msg.document or reply_msg.video or reply_msg.photo or reply_msg.audio or reply_msg.voice):
            await message.reply_text("Replied message does not contain a supported media file.")
            return

        status_msg = await message.reply_text("Downloading media file for GoFile upload...")
        temp_dir = settings.data_dir / "temp_gfup"
        temp_dir.mkdir(parents=True, exist_ok=True)

        file_path_str = await reply_msg.download(file_name=str(temp_dir) + "/")
        if not file_path_str or not Path(file_path_str).exists():
            await status_msg.edit_text("Failed to download media file from Telegram.")
            return

        local_path = Path(file_path_str)
        try:
            await status_msg.edit_text(f"Uploading `{local_path.name}` to GoFile...")
            res, _ = await upload_to_gofile(local_path)

            if isinstance(res, dict) and res.get("status") == "ok":
                gf_url = res.get("data", {}).get("downloadPage")
                await status_msg.edit_text(
                    f"**[GoFile Upload Complete]({gf_url})**\n"
                    f"**File**: `{local_path.name}`",
                    link_preview_options=LinkPreviewOptions(is_disabled=True)
                )
            else:
                err = res.get("error") if isinstance(res, dict) else "Unknown error"
                await status_msg.edit_text(f"Failed to upload to GoFile: {err}")
        except Exception as e:
            log.exception("Error uploading file to GoFile")
            await status_msg.edit_text(f"GoFile upload failed: {e}")
        finally:
            if local_path.exists():
                local_path.unlink(missing_ok=True)

    @app.on_message(filters.command(["fdup", "fileditch"]) & authorized_filter)
    async def fdup_cmd(_, message: Message) -> None:
        if not message.reply_to_message:
            await message.reply_text("Please reply to a media message with `/fdup` or `/fileditch` to upload it to FileDitch.")
            return

        reply_msg = message.reply_to_message
        if not (reply_msg.document or reply_msg.video or reply_msg.photo or reply_msg.audio or reply_msg.voice):
            await message.reply_text("Replied message does not contain a supported media file.")
            return

        status_msg = await message.reply_text("Downloading media file for FileDitch upload...")
        temp_dir = settings.data_dir / "temp_fdup"
        temp_dir.mkdir(parents=True, exist_ok=True)

        file_path_str = await reply_msg.download(file_name=str(temp_dir) + "/")
        if not file_path_str or not Path(file_path_str).exists():
            await status_msg.edit_text("Failed to download media file from Telegram.")
            return

        local_path = Path(file_path_str)
        try:
            await status_msg.edit_text(f"Uploading `{local_path.name}` to FileDitch...")
            res, _ = await upload_to_fileditch(local_path)

            if isinstance(res, dict) and res.get("success") is True:
                fd_url = res.get("url")
                await status_msg.edit_text(
                    f"**[FileDitch Upload Complete]({fd_url})**\n"
                    f"**File**: `{local_path.name}`",
                    link_preview_options=LinkPreviewOptions(is_disabled=True)
                )
            else:
                err = res.get("error") if isinstance(res, dict) else "Unknown error"
                await status_msg.edit_text(f"Failed to upload to FileDitch: {err}")
        except Exception as e:
            log.exception("Error uploading file to FileDitch")
            await status_msg.edit_text(f"FileDitch upload failed: {e}")
        finally:
            if local_path.exists():
                local_path.unlink(missing_ok=True)

    @app.on_message(filters.command(["gd2tg"]) & authorized_filter)
    async def gd2tg_cmd(client: Client, message: Message) -> None:
        parts = message.text.split(maxsplit=1)
        if len(parts) < 2:
            await message.reply_text("Provide a Google Drive link: `/gd2tg <gdrive_link>`.")
            return

        raw_link = parts[1].strip()
        link = f"gd2tg:{raw_link}"
        await _create_and_enqueue_job(client, message.chat.id, link, message, raw_link)
