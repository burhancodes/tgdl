from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import shutil
import time
from pathlib import Path
from typing import Any

from pyrogram import Client, filters
from pyrogram.types import (
    CallbackQuery,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
)

from ..auth import authorized_filter
from ..config import settings
from ..downloader import (
    get_cookies_path,
    get_gdl_config_path,
    get_user_cookies_path,
    get_user_gdl_config_path,
)
from ..downloader.gallery_dl.gofile_helper import (
    DEFAULT_FALLBACK_SALT,
    fetch_gofile_salt,
    get_browser_user_agent,
    sync_gofile_salt,
    update_gdl_conf_gofile,
)


log = logging.getLogger(__name__)

SAFE_POSTPROCESSOR_NAMES = {
    "metadata", "mtime", "content", "zip", "ugoira",
    "directory", "filename", "classify", "squeezer", "db"
}
DANGEROUS_KEYS_OR_NAMES = {
    "exec", "python", "cmd", "command", "subprocess", "shell", "script"
}


def _scan_obj_for_dangerous_directives(obj: Any) -> str | None:
    """Recursively checks a dict/list structure for dangerous keys or non-allowlisted postprocessors."""
    if isinstance(obj, dict):
        for k, v in obj.items():
            k_lower = str(k).lower()
            if k_lower in DANGEROUS_KEYS_OR_NAMES:
                return f"Forbidden configuration key directive '{k}' detected."

            if k_lower in ("postprocessor", "postprocessors"):
                pp_list = v if isinstance(v, list) else [v] if isinstance(v, dict) else []
                for item in pp_list:
                    if isinstance(item, dict):
                        name = str(item.get("name", "")).lower()
                        if not name:
                            return "Postprocessor entry missing required 'name' field."
                        if name in DANGEROUS_KEYS_OR_NAMES:
                            return f"Forbidden postprocessor '{name}' detected. Executable/script postprocessors are prohibited."
                        if name not in SAFE_POSTPROCESSOR_NAMES:
                            return f"Unrecognized or unsafe postprocessor '{name}' detected. Only safe postprocessors ({', '.join(sorted(SAFE_POSTPROCESSOR_NAMES))}) are allowed."

            err = _scan_obj_for_dangerous_directives(v)
            if err:
                return err

    elif isinstance(obj, list):
        for elem in obj:
            err = _scan_obj_for_dangerous_directives(elem)
            if err:
                return err

    return None


def _strip_comments(json_str: str) -> str:
    """Strips '#' key comment lines and standard single/multi-line comments for JSON validation."""
    lines = []
    for line in json_str.splitlines():
        stripped = line.strip()
        if stripped.startswith("#"):
            continue
        lines.append(line)
    text = "\n".join(lines)
    text = re.sub(r",\s*([\]}])", r"\1", text)
    return text


def validate_gdl_conf(content: str) -> tuple[bool, str, dict | None]:
    """Validates if content is a valid gallery-dl configuration structure and checks for dangerous directives."""
    if not content.strip():
        return False, "Configuration file is empty.", None

    clean_text = _strip_comments(content)
    try:
        data = json.loads(clean_text)
    except json.JSONDecodeError as e:
        return False, f"JSON Parsing Error: {e}", None

    if not isinstance(data, dict):
        return False, "Root element of gallery-dl configuration must be a JSON object (`{...}`).", None

    err = _scan_obj_for_dangerous_directives(data)
    if err:
        return False, f"Security Validation Error: {err}", None

    return True, "Valid gallery-dl configuration.", data


def _get_config_info(user_id: int) -> tuple[Path, bool, str, dict]:
    """Returns (active_path, is_user_specific, file_size_str, parsed_dict)."""
    user_conf = get_user_gdl_config_path(user_id)
    is_user_specific = user_conf.exists() and user_conf.is_file()
    active_path = get_gdl_config_path(user_id) or settings.gdl_config_path

    parsed_dict = {}
    size_str = "0 B"

    if active_path and active_path.exists():
        try:
            size_bytes = active_path.stat().st_size
            if size_bytes < 1024:
                size_str = f"{size_bytes} B"
            else:
                size_str = f"{size_bytes / 1024:.1f} KB"

            content = active_path.read_text(encoding="utf-8", errors="ignore")
            _, _, parsed_dict = validate_gdl_conf(content)
            parsed_dict = parsed_dict or {}
        except Exception as e:
            log.warning("Failed reading config info from %s: %s", active_path, e)

    return active_path, is_user_specific, size_str, parsed_dict


def _get_cookies_info(user_id: int) -> tuple[Path | None, bool, str]:
    """Returns (active_cookies_path, is_user_specific, size_str)."""
    user_cookies = get_user_cookies_path(user_id)
    is_user_specific = user_cookies.exists() and user_cookies.is_file()
    active_cookies = get_cookies_path(user_id)

    size_str = "0 B"
    if active_cookies and active_cookies.exists():
        size_bytes = active_cookies.stat().st_size
        if size_bytes < 1024:
            size_str = f"{size_bytes} B"
        else:
            size_str = f"{size_bytes / 1024:.1f} KB"

    return active_cookies, is_user_specific, size_str


def build_gdlconf_text(user_id: int) -> tuple[str, InlineKeyboardMarkup]:
    active_path, is_user_specific, size_str, data = _get_config_info(user_id)
    active_cookies, is_user_cookies, cookies_size_str = _get_cookies_info(user_id)

    scope_str = "**User-Specific** (`auth/{user_id}/gallery-dl.conf`)" if is_user_specific else "**Global Default** (`gallery-dl.conf`)"
    mtime_str = "N/A"
    if active_path and active_path.exists():
        mtime_str = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(active_path.stat().st_mtime))

    if is_user_cookies:
        cookies_str = f"**User-Specific** (`auth/{user_id}/cookies.txt`, `{cookies_size_str}`)"
    elif active_cookies and active_cookies.exists():
        cookies_str = f"**Global Default** (`cookies.txt`, `{cookies_size_str}`)"
    else:
        cookies_str = "**Not Configured**"

    extractors = []
    if isinstance(data, dict) and "extractor" in data and isinstance(data["extractor"], dict):
        ext_dict = data["extractor"]
        for k, v in ext_dict.items():
            if isinstance(v, dict) and not k.startswith("#"):
                extractors.append(k)

    ext_summary = ", ".join(f"`{e}`" for e in extractors[:10]) if extractors else "None specified"
    if len(extractors) > 10:
        ext_summary += f" (+ {len(extractors) - 10} more)"

    gofile_salt = None
    if isinstance(data, dict) and "extractor" in data and isinstance(data["extractor"], dict):
        gofile_conf = data["extractor"].get("gofile")
        if isinstance(gofile_conf, dict):
            gofile_salt = gofile_conf.get("salt")
    gofile_salt = gofile_salt or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT

    text = (
        "**gallery-dl Configuration & Cookies Status**\n\n"
        f"• **Config Scope**: {scope_str}\n"
        f"• **Config Path**: `{active_path}`\n"
        f"• **Config Size**: `{size_str}`\n"
        f"• **Last Modified**: `{mtime_str}`\n"
        f"• **GoFile Salt**: `{gofile_salt}`\n"
        f"• **Configured Sites**: {ext_summary}\n"
        f"• **Cookies File**: {cookies_str}\n\n"
        "**Usage Commands:**\n"
        "• Reply to a `.conf` / `.json` file with `/gdlconf` to upload custom config.\n"
        "• Reply to a `cookies.txt` file with `/gdlconf` to upload custom cookies.\n"
        "• `/gdlconf get` — Download current configuration file.\n"
        "• `/gdlconf cookies get` — Download current `cookies.txt` file.\n"
        "• `/gdlconf gofile` — Check GoFile salt and token status.\n"
        "• `/gdlconf gofile sync` — Fetch live GoFile salt & update configs.\n"
        "• `/gdlconf delete` — Delete custom user config.\n"
        "• `/gdlconf cookies delete` — Delete custom user `cookies.txt`.\n"
        "• `/gdlconf reset` — Reset config to default template."
    )

    buttons = [
        [
            InlineKeyboardButton("Download Conf", callback_data="gdlconf:get"),
            InlineKeyboardButton("Refresh", callback_data="gdlconf:view"),
        ]
    ]

    cookie_row = []
    if active_cookies and active_cookies.exists():
        cookie_row.append(InlineKeyboardButton("Download Cookies", callback_data="gdlconf:get_cookies"))
    if is_user_cookies:
        cookie_row.append(InlineKeyboardButton("Delete Cookies", callback_data="gdlconf:delete_cookies"))
    if cookie_row:
        buttons.append(cookie_row)

    buttons.append([InlineKeyboardButton("Sync GoFile Salt", callback_data="gdlconf:sync_gofile")])

    if is_user_specific:
        buttons.append([InlineKeyboardButton("Delete Custom Conf", callback_data="gdlconf:delete")])
    else:
        buttons.append([InlineKeyboardButton("Create User Template", callback_data="gdlconf:reset")])

    keyboard = InlineKeyboardMarkup(buttons)
    return text, keyboard


def _get_default_template_path() -> Path | None:
    candidates = [
        Path(__file__).parent.parent / "downloader" / "gallery_dl" / "gallery-dl.conf",
        settings.gdl_config_path,
        Path("./gallery-dl.conf"),
    ]
    for c in candidates:
        if c and c.exists() and c.is_file():
            return c
    return None


def register_gdlconf_handlers(app: Client) -> None:

    @app.on_message(filters.command(["gdlconf", "gdl_config"]) & authorized_filter)
    async def gdlconf_cmd(_, message: Message) -> None:
        user_id = message.from_user.id if message.from_user else message.chat.id
        args = message.text.split(maxsplit=1)
        subcommand = args[1].strip().lower() if len(args) > 1 else ""

        # Case 1: User replied to a document file to set configuration or cookies
        if message.reply_to_message and message.reply_to_message.document:
            doc = message.reply_to_message.document
            file_name = (doc.file_name or "").lower()

            status_msg = await message.reply_text("Downloading & processing uploaded document...")
            temp_path = await message.reply_to_message.download()
            if not temp_path or not Path(temp_path).exists():
                await status_msg.edit_text("Failed to download document file.")
                return

            try:
                content = Path(temp_path).read_text(encoding="utf-8", errors="ignore")

                # Detect if file is Netscape cookies.txt or requested as cookies
                is_cookies_file = (
                    file_name == "cookies.txt"
                    or file_name.endswith(".cookies")
                    or subcommand in ("cookies", "cookie")
                    or "# netscape http cookie file" in content.lower()
                    or "# http cookie file" in content.lower()
                )

                if is_cookies_file:
                    if not content.strip():
                        await status_msg.edit_text("Uploaded cookies file is empty.")
                        return

                    user_cookies_path = get_user_cookies_path(user_id)
                    user_cookies_path.parent.mkdir(parents=True, exist_ok=True)
                    user_cookies_path.write_text(content, encoding="utf-8")
                    os.chmod(user_cookies_path, 0o600)

                    await status_msg.edit_text(
                        f"**Saved user cookies file!**\n"
                        f"Saved to: `auth/{user_id}/cookies.txt`\n\n"
                        f"All subsequent `/gdl` downloads will automatically use your custom cookies."
                    )
                    return

                # Otherwise handle as gallery-dl JSON config file
                ok, err_msg, parsed = validate_gdl_conf(content)
                if not ok:
                    await status_msg.edit_text(f"**Invalid File Format**:\n`{err_msg}`\n\nTo upload cookies, ensure the file is named `cookies.txt` or contains Netscape cookie headers.")
                    return

                user_conf_path = get_user_gdl_config_path(user_id)
                user_conf_path.parent.mkdir(parents=True, exist_ok=True)
                user_conf_path.write_text(content, encoding="utf-8")
                os.chmod(user_conf_path, 0o600)
                update_gdl_conf_gofile(user_conf_path)

                await status_msg.edit_text(
                    f"**Saved user gallery-dl configuration!**\n"
                    f"Saved to: `auth/{user_id}/gallery-dl.conf`\n\n"
                    f"All subsequent `/gdl` downloads for your user account will use your custom settings."
                )
            except Exception as e:
                log.exception("Failed to save gallery-dl file for user %s", user_id)
                await status_msg.edit_text(f"Failed to save file: `{e}`")
            finally:
                Path(temp_path).unlink(missing_ok=True)
            return

        # Case 2: Subcommands for cookies
        if subcommand in ("cookies get", "get cookies", "cookies download"):
            cookies_path = get_cookies_path(user_id)
            if cookies_path and cookies_path.exists():
                await message.reply_document(
                    document=str(cookies_path),
                    caption=f"**gallery-dl Cookies File** (`{cookies_path.name}`)"
                )
            else:
                await message.reply_text("No `cookies.txt` file configured.")
            return

        elif subcommand in ("cookies delete", "delete cookies", "cookies remove"):
            user_cookies = get_user_cookies_path(user_id)
            if user_cookies.exists():
                user_cookies.unlink(missing_ok=True)
                await message.reply_text("Custom user `cookies.txt` deleted!")
            else:
                await message.reply_text("You do not have a custom `cookies.txt` saved.")
            return

        # Case 3: Subcommands for GoFile Salt Sync
        elif subcommand in ("gofile", "salt", "gofile salt", "gofile status"):
            active_path, is_user_specific, _, data = _get_config_info(user_id)
            gofile_conf = data.get("extractor", {}).get("gofile", {}) if isinstance(data, dict) else {}
            curr_salt = (
                gofile_conf.get("salt")
                if isinstance(gofile_conf, dict)
                else None
            ) or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT
            ua = get_browser_user_agent()

            msg_text = (
                "**GoFile Compatibility & Salt Status**\n\n"
                f"• **Active Salt**: `{curr_salt}`\n"
                f"• **Browser User-Agent**: `{ua}`\n"
                f"• **Target Config**: `{active_path}`\n\n"
                "Use `/gdlconf gofile sync` to re-apply the active salt to all configs."
            )
            btn = InlineKeyboardMarkup([
                [InlineKeyboardButton("Sync Config Salt", callback_data="gdlconf:sync_gofile")],
                [InlineKeyboardButton("Back to GDL Conf", callback_data="gdlconf:view")],
            ])
            await message.reply_text(msg_text, reply_markup=btn, link_preview_options=LinkPreviewOptions(is_disabled=True))
            return

        elif subcommand in ("gofile sync", "gofile update", "sync gofile", "updatesalt", "sync"):
            status_m = await message.reply_text("Synchronizing GoFile salt across configurations...")
            try:
                salt, results = await asyncio.to_thread(sync_gofile_salt)
                updated_names = [Path(p).name for p, ok in results.items() if ok]
                await status_m.edit_text(
                    f"**GoFile Salt Synchronized!**\n\n"
                    f"• **Active Salt**: `{salt}`\n"
                    f"• **Browser UA**: `{get_browser_user_agent()}`\n"
                    f"• **Updated Configs**: {', '.join(f'`{n}`' for n in updated_names) if updated_names else 'Default template'}\n\n"
                    f"Free GoFile downloads will now use the updated token generation."
                )
            except Exception as e:
                log.exception("Error syncing GoFile salt")
                await status_m.edit_text(f"Failed to sync GoFile salt: `{e}`")
            return

        # Case 4: Standard Subcommands for config
        if subcommand in ("get", "download"):
            active_path = get_gdl_config_path(user_id) or _get_default_template_path()
            if active_path and active_path.exists():
                await message.reply_document(
                    document=str(active_path),
                    caption=f"**gallery-dl Configuration File** (`{active_path.name}`)"
                )
            else:
                await message.reply_text("Configuration file not found.")
            return

        elif subcommand in ("delete", "remove"):
            user_conf = get_user_gdl_config_path(user_id)
            if user_conf.exists():
                user_conf.unlink(missing_ok=True)
                await message.reply_text("Custom user `gallery-dl.conf` deleted! Reverted to default configuration.")
            else:
                await message.reply_text("You do not have a custom `gallery-dl.conf` saved. Currently using default configuration.")
            return

        elif subcommand in ("reset", "init"):
            default_template = _get_default_template_path()

            if default_template and default_template.exists():
                user_conf = get_user_gdl_config_path(user_id)
                user_conf.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(default_template, user_conf)
                await message.reply_text(f"Reset user config to default template: `auth/{user_id}/gallery-dl.conf`")
            else:
                await message.reply_text("Default template `gallery-dl.conf` not found.")
            return

        # Default view
        text, keyboard = build_gdlconf_text(user_id)
        await message.reply_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))

    @app.on_callback_query(filters.regex(r"^gdlconf:") & authorized_filter)
    async def gdlconf_callback(_, query: CallbackQuery) -> None:
        user_id = query.from_user.id if query.from_user else query.message.chat.id
        action = query.data.split(":")[1]

        if action == "view":
            text, keyboard = build_gdlconf_text(user_id)
            try:
                await query.message.edit_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))
            except Exception:
                pass
            await query.answer("Refreshed status")

        elif action == "sync_gofile":
            await query.answer("Syncing GoFile salt...", show_alert=False)
            try:
                salt, _ = await asyncio.to_thread(sync_gofile_salt)
                text, keyboard = build_gdlconf_text(user_id)
                try:
                    await query.message.edit_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))
                except Exception:
                    pass
                await query.message.reply_text(f"**GoFile Salt Synchronized!**\nActive salt: `{salt}`")
            except Exception as e:
                log.exception("Callback error syncing GoFile salt")
                await query.answer(f"Sync failed: {e}", show_alert=True)


        elif action == "get":
            active_path = get_gdl_config_path(user_id) or _get_default_template_path()
            if active_path and active_path.exists():
                await query.message.reply_document(
                    document=str(active_path),
                    caption=f"**gallery-dl Configuration File** (`{active_path.name}`)"
                )
                await query.answer("Sending document...")
            else:
                await query.answer("Configuration file not found", show_alert=True)

        elif action == "get_cookies":
            cookies_path = get_cookies_path(user_id)
            if cookies_path and cookies_path.exists():
                await query.message.reply_document(
                    document=str(cookies_path),
                    caption=f"**gallery-dl Cookies File** (`{cookies_path.name}`)"
                )
                await query.answer("Sending cookies document...")
            else:
                await query.answer("Cookies file not found", show_alert=True)

        elif action == "delete_cookies":
            user_cookies = get_user_cookies_path(user_id)
            if user_cookies.exists():
                user_cookies.unlink(missing_ok=True)
                await query.answer("Custom cookies.txt deleted!", show_alert=True)
            else:
                await query.answer("No custom cookies file found", show_alert=True)

            text, keyboard = build_gdlconf_text(user_id)
            try:
                await query.message.edit_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))
            except Exception:
                # expected: message text already up to date or deleted
                pass

        elif action == "delete":
            user_conf = get_user_gdl_config_path(user_id)
            if user_conf.exists():
                user_conf.unlink(missing_ok=True)
                await query.answer("Custom configuration deleted!", show_alert=True)
            else:
                await query.answer("No custom configuration found", show_alert=True)

            text, keyboard = build_gdlconf_text(user_id)
            try:
                await query.message.edit_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))
            except Exception:
                # expected: message text already up to date or deleted
                pass

        elif action == "reset":
            default_template = _get_default_template_path()

            if default_template and default_template.exists():
                user_conf = get_user_gdl_config_path(user_id)
                user_conf.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(default_template, user_conf)
                await query.answer("User config reset to default template!", show_alert=True)
            else:
                await query.answer("Default template not found", show_alert=True)

            text, keyboard = build_gdlconf_text(user_id)
            try:
                await query.message.edit_text(text, reply_markup=keyboard, link_preview_options=LinkPreviewOptions(is_disabled=True))
            except Exception:
                # expected: message text already up to date or deleted
                pass
