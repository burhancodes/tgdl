from __future__ import annotations

import asyncio
import logging
from pathlib import Path

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
from ..manager import (
    _password_prompt_events,
    _password_prompt_messages,
    queue_manager,
    store,
)
from ..manager.status.compiler import (
    compile_split_prompt_text,
    compile_unzip_download_status_text,
)
from ..utils.archive import (
    ARCHIVE_EXT,
    archive_session_store,
    get_split_archive_info,
    handle_multi_cancel_cb,
    handle_multi_document,
    handle_multi_start_cb,
    normalize_split_archive_filenames,
    start_multi_unzip_session,
)

log = logging.getLogger(__name__)


def compile_split_session_text(prefix: str, ext: str, parts: dict[int, Message]) -> str:
    sorted_parts = sorted(parts.keys())
    parts_list = []
    max_part = max(sorted_parts) if sorted_parts else 0

    for i in range(1, max_part + 2):
        if i in parts:
            filename = parts[i].document.file_name
            parts_list.append(f"**Part {i}**: `{filename}`")
        else:
            if i == 1 or i <= max_part:
                parts_list.append(f"**Part {i}**: _Waiting for file..._")
            else:
                break

    parts_str = "\n".join(parts_list)

    text = (
        f"**Split Archive Session**\n"
        f"- **Base Pattern**: `{prefix}.*`\n\n"
        f"**Instructions:**\n"
        f"Please upload/forward the remaining parts of this archive to this chat.\n\n"
        f"**Parts Received:**\n"
        f"{parts_str}\n\n"
        f"When all parts are uploaded, click **Start Extraction** below."
    )
    return text


def register_unzip_handlers(app: Client) -> None:

    @app.on_message(filters.command("unzip") & authorized_filter)
    async def unzip_cmd(_, message: Message) -> None:
        raw_text = (message.text or "").strip()
        parts = raw_text.split(maxsplit=1)
        args_text = parts[1].strip() if len(parts) > 1 else ""
        lowered_args = args_text.lower()

        if lowered_args == "multi" or lowered_args.startswith("multi "):
            password = args_text[5:].strip() if len(args_text) > 5 else None
            password = password or None
            await start_multi_unzip_session(message, password=password)
            return

        if lowered_args == "split" or lowered_args.startswith("split "):
            password = args_text[5:].strip() if len(args_text) > 5 else None
            password = password or None
            chat_id = message.chat.id
            user_id = message.from_user.id if message.from_user else chat_id
            session_key = chat_id

            if archive_session_store.has_split_session(session_key):
                old_session = archive_session_store.pop_split_session(session_key)
                if old_session and old_session.get("timeout_task"):
                    old_session["timeout_task"].cancel()
                if old_session and "status_msg" in old_session:
                    try:
                        await old_session["status_msg"].edit_text("**Session replaced by a new one.**")
                    except Exception:
                        # expected: status message already deleted or unchanged
                        pass

            def get_split_session_keyboard(c_id: int, u_id: int) -> InlineKeyboardMarkup:
                return InlineKeyboardMarkup([
                    [
                        InlineKeyboardButton("Start Extraction", callback_data=f"split_start:{c_id}:{u_id}"),
                        InlineKeyboardButton("Cancel", callback_data=f"split_cancel:{c_id}:{u_id}")
                    ]
                ])

            status_msg = await message.reply_text(
                "**Split Archive Session Started**\n\n"
                "Please send or forward the split archive parts (e.g. `.001`, `.002`, or `.part1.rar` files) to this chat.\n\n"
                "**Waiting for files...**",
                reply_markup=get_split_session_keyboard(chat_id, user_id)
            )

            async def split_session_timeout(c_id: int, delay: int = 300):
                await asyncio.sleep(delay)
                s_key = c_id
                if archive_session_store.has_split_session(s_key):
                    session = archive_session_store.pop_split_session(s_key)
                    if session:
                        try:
                            await session["status_msg"].edit_text("**Split Archive Session Expired** (Timeout due to inactivity).")
                        except Exception:
                            pass

            timeout_task = asyncio.create_task(split_session_timeout(chat_id))
            archive_session_store.set_split_session(session_key, {
                "user_id": user_id,
                "password": password,
                "parts": {},
                "prefix": None,
                "ext": None,
                "status_msg": status_msg,
                "timeout_task": timeout_task,
            })
            return

        if not message.reply_to_message or not message.reply_to_message.document:
            await message.reply_text("Reply to an archive with `/unzip [password]` or use `/unzip split [password]` or `/unzip multi [password]`.")
            return

        doc = message.reply_to_message.document
        ext = Path(doc.file_name).suffix.lower()
        if ext not in ARCHIVE_EXT:
            await message.reply_text(f"File `{doc.file_name}` is not a supported archive format.")
            return

        password = args_text or None
        target_url = f"unzip:{doc.file_name}"
        import json
        unzip_args = {"reply_message_id": message.reply_to_message.id, "password": password}
        sender_uid = getattr(message.from_user, "id", None) if getattr(message, "from_user", None) and message.from_user else None
        if sender_uid and sender_uid > 0:
            unzip_args["user_id"] = sender_uid
        args_json = json.dumps(unzip_args)
        job = await store.create_job(message.chat.id, target_url, split_large_files=1, args=args_json)
        await store.update_progress(job.id, status="waiting")

        keyboard = InlineKeyboardMarkup([
            [
                InlineKeyboardButton("Yes, split them", callback_data=f"split_yes:{job.id}"),
                InlineKeyboardButton("No, skip them", callback_data=f"split_no:{job.id}")
            ],
            [
                InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")
            ]
        ])

        prompt_text = compile_split_prompt_text(job.id, doc.file_name, is_unzip=True)
        status_msg = await message.reply_text(
            prompt_text,
            reply_markup=keyboard,
            link_preview_options=LinkPreviewOptions(is_disabled=True)
        )
        await store.set_status_message(job.id, status_msg.id)

    @app.on_message(filters.text, group=-1)
    async def password_reply_listener(_, message: Message) -> None:
        if not message.reply_to_message:
            return

        prompt_msg_id = message.reply_to_message.id
        if prompt_msg_id not in _password_prompt_messages:
            return

        info = _password_prompt_messages.get(prompt_msg_id)
        if info:
            job_id, archive_id, chat_id = info
            if job_id in _password_prompt_events and archive_id in _password_prompt_events[job_id]:
                event, data = _password_prompt_events[job_id][archive_id]
                data["password"] = message.text.strip()
                event.set()

    @app.on_message(group=-2)
    async def document_intercept_listener(_, message: Message) -> None:
        if not message.document:
            return

        chat_id = message.chat.id
        handled_multi = await handle_multi_document(message)
        if handled_multi:
            return

        if not archive_session_store.has_split_session(chat_id):
            return

        session = archive_session_store.get_split_session(chat_id)
        if message.from_user and message.from_user.id != session["user_id"]:
            return

        doc = message.document
        filename = doc.file_name

        split_info = get_split_archive_info(filename)
        if not split_info:
            return
        prefix = split_info["prefix"]
        ext = split_info.get("ext", "")
        part_num = split_info["part"]

        if session["prefix"] is None:
            session["prefix"] = prefix
            session["ext"] = ext
        elif session["prefix"] != prefix:
            await message.reply_text(f"File `{filename}` doesn't match the current split archive pattern (`{session['prefix']}.*`).")
            return

        session["parts"][part_num] = message
        if session.get("timeout_task"):
            session["timeout_task"].cancel()

        async def reset_timeout():
            await asyncio.sleep(300)
            if archive_session_store.has_split_session(chat_id):
                s = archive_session_store.pop_split_session(chat_id)
                if s:
                    try:
                        await s["status_msg"].edit_text("**Split Archive Session Expired** (Timeout due to inactivity).")
                    except Exception:
                        # expected: status message already deleted or unchanged
                        pass

        session["timeout_task"] = asyncio.create_task(reset_timeout())
        updated_text = compile_split_session_text(session["prefix"], session["ext"], session["parts"])
        try:
            await session["status_msg"].edit_text(
                updated_text,
                reply_markup=session["status_msg"].reply_markup
            )
        except Exception:
            # expected: status message already deleted or unchanged
            pass

    @app.on_callback_query(filters.regex(r"^split_cancel:(-?\d+):(-?\d+)$"))
    async def split_cancel_cb(_, query: CallbackQuery) -> None:
        match = query.matches[0]
        chat_id = int(match.group(1))
        user_id = int(match.group(2))

        if query.from_user.id != user_id and query.message.chat.id != chat_id:
            await query.answer("You are not authorized to cancel this session.", show_alert=True)
            return

        session = archive_session_store.pop_split_session(chat_id)
        if session and session.get("timeout_task"):
            session["timeout_task"].cancel()

        await query.answer("Split archive session cancelled.")
        await query.message.edit_text("**Split Archive Session Cancelled.**")

    @app.on_callback_query(filters.regex(r"^split_start:(-?\d+):(-?\d+)$"))
    async def split_start_cb(_, query: CallbackQuery) -> None:
        match = query.matches[0]
        chat_id = int(match.group(1))
        user_id = int(match.group(2))

        if query.from_user.id != user_id and query.message.chat.id != chat_id:
            await query.answer("You are not authorized to start this session.", show_alert=True)
            return

        session = archive_session_store.pop_split_session(chat_id)
        if not session or not session["parts"]:
            await query.answer("No split archive parts received yet!", show_alert=True)
            return

        if session.get("timeout_task"):
            session["timeout_task"].cancel()

        await query.answer("Starting extraction...")
        asyncio.create_task(run_split_archive_download_and_extract(session, query.message, store, queue_manager))

    @app.on_callback_query(filters.regex(r"^multi_cancel:(-?\d+):(-?\d+)$"))
    async def multi_cancel_cb(client: Client, query: CallbackQuery) -> None:
        await handle_multi_cancel_cb(client, query)

    @app.on_callback_query(filters.regex(r"^multi_start:(-?\d+):(-?\d+)$"))
    async def multi_start_cb(client: Client, query: CallbackQuery) -> None:
        await handle_multi_start_cb(client, query, store, queue_manager)


async def run_split_archive_download_and_extract(
    session: dict,
    status_msg: Message,
    store,
    queue_manager
) -> None:
    import json

    from ..db import JobStatus
    parts: dict[int, Message] = session["parts"]
    password: str | None = session["password"]
    chat_id = status_msg.chat.id
    sorted_part_nums = sorted(parts.keys())
    total_parts = len(sorted_part_nums)

    if not sorted_part_nums:
        return

    first_part_msg = parts[sorted_part_nums[0]]
    first_filename = first_part_msg.document.file_name or "archive.001"
    display_name = f"unzip:{first_filename}"

    args_dict = {"reply_message_id": first_part_msg.id}
    if password:
        args_dict["password"] = password
    args_json = json.dumps(args_dict)

    job = await store.create_job(chat_id, display_name, split_large_files=1, args=args_json)
    await store.update_progress(job.id, status=JobStatus.DOWNLOADING)

    dest_dir = (settings.downloads_dir / job.download_dir).resolve()
    dest_dir.mkdir(parents=True, exist_ok=True)

    keyboard = InlineKeyboardMarkup([
        [InlineKeyboardButton("Cancel", callback_data=f"cancel_job:{job.id}")]
    ])

    try:
        await status_msg.edit_text(
            f"**Split Archive Pipeline Started** ({total_parts} parts)\n"
            "------------------------------------\n"
            "Downloading all split archive parts..."
        )
    except Exception:
        # expected: status message already deleted or unchanged
        pass

    import time
    last_edit_time = 0.0
    for idx, part_num in enumerate(sorted_part_nums, start=1):
        part_msg = parts[part_num]
        part_filename = part_msg.document.file_name or f"part_{part_num}"
        target_file = dest_dir / part_filename

        async def on_part_download_progress(current, total):
            nonlocal last_edit_time
            now = time.time()
            if now - last_edit_time < 2.0 and current != total:
                return
            last_edit_time = now
            try:
                progress_name = f"{part_filename} ({idx}/{total_parts})"
                await status_msg.edit_text(
                    compile_unzip_download_status_text(job.id, progress_name, current, total),
                    reply_markup=keyboard
                )
            except Exception:
                # expected: status message already deleted or unchanged
                pass

        log.info("Split-unzip pipeline [%s/%s]: Downloading %s for job #%s...", idx, total_parts, part_filename, job.id)
        await part_msg.download(file_name=str(target_file), progress=on_part_download_progress)

    normalize_split_archive_filenames(dest_dir)

    log.info("Split-unzip pipeline: All %s parts downloaded for job #%s. Enqueuing for extraction...", total_parts, job.id)
    await store.db.execute(
        "UPDATE jobs SET status = ?, split_large_files = ? WHERE id = ?",
        (JobStatus.QUEUED, 1, job.id)
    )
    await store.db.commit()
    await queue_manager.add_job(job.id)

