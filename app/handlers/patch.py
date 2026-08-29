from __future__ import annotations

import json
import logging
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
from ..db import JobStatus
from ..manager import queue_manager, store
from ..manager.status.messaging import safe_send

log = logging.getLogger(__name__)


def resolve_pixeldrain_url(url: str) -> str:
    """Converts pixeldrain share link into a direct download API link."""
    if "pixeldrain.com/u/" in url:
        return url.replace("pixeldrain.com/u/", "pixeldrain.com/api/file/")
    return url


def register_patch_handlers(app: Client) -> None:

    @app.on_message(filters.command(["setkeystore", "keystore"]) & authorized_filter)
    async def set_keystore_cmd(_, message: Message) -> None:
        user_id = message.from_user.id if getattr(message, "from_user", None) and message.from_user else None
        if not user_id or user_id <= 0:
            await message.reply_text("Could not determine your user ID. Keystore configuration must be performed by an identified user.")
            return

        user_dir = (settings.auth_dir / str(user_id)).resolve()
        user_dir.mkdir(parents=True, exist_ok=True)

        ks_file = (user_dir / "keystore.jks").resolve()
        cfg_file = (user_dir / "keystore_config.json").resolve()

        text = (message.text or message.caption or "").strip()
        parts = text.split(maxsplit=3)
        args = parts[1:] if len(parts) > 1 else []

        # Handle deletion
        if args and args[0].lower() in ("del", "delete", "remove", "clear"):
            for f in user_dir.glob("*.jks"):
                f.unlink(missing_ok=True)
            for f in user_dir.glob("*.keystore"):
                f.unlink(missing_ok=True)
            if cfg_file.is_file():
                cfg_file.unlink(missing_ok=True)
            await message.reply_text("✓ Your personal JKS keystore and credentials have been deleted.")
            return

        # Check if user attached or replied with a .jks file document
        target_doc_msg = None
        if message.document:
            target_doc_msg = message
        elif message.reply_to_message and message.reply_to_message.document:
            target_doc_msg = message.reply_to_message

        if target_doc_msg:
            doc = target_doc_msg.document
            fname = doc.file_name or "keystore.jks"
            if not (fname.lower().endswith(".jks") or fname.lower().endswith(".keystore")):
                await message.reply_text("Please upload a valid `.jks` or `.keystore` file.")
                return

            if len(args) < 2:
                await message.reply_text(
                    "**Missing passwords/alias!**\n\n"
                    "Usage when replying to/attaching `.jks` file:\n"
                    "`/setkeystore <store_password> <key_alias> [key_password]`"
                )
                return

            store_pass = args[0]
            key_alias = args[1]
            key_pass = args[2] if len(args) > 2 else store_pass

            # Download document to user directory using absolute path
            status_msg = await message.reply_text("Downloading and saving your JKS keystore...")
            try:
                dl_res = await target_doc_msg.download(file_name=str(ks_file))
                dl_p = Path(dl_res).resolve() if dl_res else ks_file
                if dl_p.is_file() and dl_p != ks_file:
                    import shutil
                    shutil.move(str(dl_p), str(ks_file))

                cfg_data = {
                    "store_pass": store_pass,
                    "key_alias": key_alias,
                    "key_pass": key_pass,
                }
                cfg_file.write_text(json.dumps(cfg_data, indent=2), encoding="utf-8")
                await status_msg.edit_text(
                    f"✓ **JKS Keystore Saved Successfully!**\n\n"
                    f"- **Keystore File**: `{ks_file.name}`\n"
                    f"- **Key Alias**: `{key_alias}`\n\n"
                    f"You can now use `/patch` to decompile, patch, sign, and upload APKs."
                )
            except Exception as e:
                log.exception("Failed saving keystore for user %s: %s", user_id, e)
                await status_msg.edit_text(f"Failed to save keystore: `{e}`")
            return

        # No document provided: show current status or instructions
        ks_info = settings.get_user_keystore_info(user_id)
        if ks_info:
            ks_path = ks_info["keystore_path"]
            alias = ks_info["key_alias"]
            await message.reply_text(
                f"**JKS Keystore Status**: Set (`{ks_path.name}`)\n"
                f"- **Key Alias**: `{alias}`\n\n"
                f"**To update your keystore:**\n"
                f"Reply to a new `.jks` file with:\n"
                f"`/setkeystore <store_password> <key_alias> [key_password]`\n\n"
                f"**To remove your keystore:**\n"
                f"`/setkeystore delete`"
            )
        else:
            await message.reply_text(
                "**JKS Keystore Setup**\n\n"
                "To sign patched APKs, please upload your `.jks` keystore:\n\n"
                "1. Upload or reply to your `.jks` file with:\n"
                "`/setkeystore <store_password> <key_alias> [key_password]`\n\n"
                "Example:\n"
                "`/setkeystore mySecretPass myKeyAlias mySecretPass`"
            )

    @app.on_message(filters.command("patch") & authorized_filter)
    async def patch_cmd(_, message: Message) -> None:
        chat_id = message.chat.id
        user_id = message.from_user.id if getattr(message, "from_user", None) and message.from_user else None
        if not user_id or user_id <= 0:
            await message.reply_text("Could not determine your user ID. APK patching must be performed by an identified user.")
            return

        # Check if user has a keystore configured
        ks_info = settings.get_user_keystore_info(user_id)
        if not ks_info:
            await message.reply_text(
                "**JKS Keystore Not Found!**\n\n"
                "Before patching APKs, please set up your JKS keystore for signing.\n\n"
                "Reply to your `.jks` file with:\n"
                "`/setkeystore <store_password> <key_alias> [key_password]`"
            )
            return

        raw_text = (message.text or message.caption or "").strip()
        parts = raw_text.split(maxsplit=1)
        arg_text = parts[1].strip() if len(parts) > 1 else ""

        target_url = None
        reply_msg_id = None
        original_filename = "app.apk"

        # 1. Check if replying to a message with document/file
        reply_msg = message.reply_to_message
        if reply_msg and (reply_msg.document or reply_msg.video or reply_msg.audio):
            reply_msg_id = reply_msg.id
            if reply_msg.document and reply_msg.document.file_name:
                original_filename = reply_msg.document.file_name
            elif reply_msg.video and reply_msg.video.file_name:
                original_filename = reply_msg.video.file_name
        # 2. Check if current message has attached document
        elif message.document:
            reply_msg_id = message.id
            if message.document.file_name:
                original_filename = message.document.file_name
        # 3. Check if arg_text is a URL
        elif arg_text.startswith(("http://", "https://")):
            target_url = resolve_pixeldrain_url(arg_text)
            # Try parsing original filename from URL path
            path_filename = Path(arg_text.split("?")[0]).name
            if path_filename and path_filename.lower().endswith(".apk"):
                original_filename = path_filename

        if not target_url and not reply_msg_id:
            await message.reply_text(
                "**Invalid usage!**\n\n"
                "Please use `/patch` in one of the following ways:\n"
                "1. Reply to an APK file message with `/patch`\n"
                "2. Send an APK file with caption `/patch`\n"
                "3. Use `/patch <URL>` (e.g. direct link or Pixeldrain link)"
            )
            return

        display_name = original_filename
        job_url_val = f"patch:{target_url}" if target_url else f"patch:reply_{reply_msg_id}"

        args_dict = {
            "user_id": user_id,
            "original_filename": original_filename,
        }
        if reply_msg_id:
            args_dict["reply_message_id"] = reply_msg_id
        if target_url:
            args_dict["target_url"] = target_url

        args_json = json.dumps(args_dict)

        job = await store.create_job(chat_id, job_url_val, split_large_files=1, args=args_json)
        await store.update_progress(job.id, status=JobStatus.QUEUED)

        keyboard = InlineKeyboardMarkup([
            [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
        ])
        from ..manager.status.compiler import compile_queued_status_text
        queued_text = compile_queued_status_text(job.id, job_url_val, "")

        status_msg = await safe_send(
            app,
            chat_id,
            queued_text,
            reply_markup=keyboard,
            link_preview_options=LinkPreviewOptions(is_disabled=True)
        )

        if status_msg:
            await store.set_status_message(job.id, status_msg.id)

        await queue_manager.add_job(job.id)
