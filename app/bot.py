from __future__ import annotations

import asyncio
import logging
import logging.handlers
import time

from pyrogram import Client, idle

from .config import settings
from .handlers import register_all_handlers
from .utils.telegram import delete_status

log = logging.getLogger("tgdl_bot")


import json


class JsonFormatter(logging.Formatter):
    """JSON log formatter for production observability."""

    def __init__(self, is_debug: bool = False):
        super().__init__()
        self.is_debug = is_debug

    def format(self, record: logging.LogRecord) -> str:
        log_obj = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(record.created)),
            "level": record.levelname,
            "name": record.name,
            "message": record.getMessage(),
        }
        if self.is_debug:
            log_obj["file"] = record.filename
            log_obj["line"] = record.lineno
            log_obj["func"] = record.funcName
        if record.exc_info:
            log_obj["exception"] = self.formatException(record.exc_info)
        return json.dumps(log_obj)


def setup_logging() -> None:
    level_str = settings.log_level.upper()
    level = getattr(logging, level_str, logging.INFO)
    is_debug = level_str == "DEBUG"

    fmt: logging.Formatter
    if settings.log_format.lower() == "json":
        fmt = JsonFormatter(is_debug=is_debug)
    else:
        if is_debug:
            fmt = logging.Formatter(
                "%(asctime)s %(levelname)s %(name)s [%(filename)s:%(lineno)d in %(funcName)s]: %(message)s"
            )
        else:
            fmt = logging.Formatter("%(asctime)s %(levelname)s %(name)s: %(message)s")

    root = logging.getLogger()
    root.setLevel(level)

    console = logging.StreamHandler()
    console.setFormatter(fmt)
    root.addHandler(console)

    try:
        settings.log_dir.mkdir(parents=True, exist_ok=True)
        file_handler = logging.handlers.RotatingFileHandler(
            settings.log_dir / "bot.log", maxBytes=10_000_000, backupCount=5
        )
        file_handler.setFormatter(fmt)
        root.addHandler(file_handler)
    except Exception as exc:
        logging.warning("Could not initialize file logging at %s: %s. Falling back to console logging.", settings.log_dir / "bot.log", exc)

    if level_str != "DEBUG":
        logging.getLogger("pyrogram").setLevel(logging.WARNING)

    if level_str == "WARNING":
        noisy_loggers = ["pyrogram", "aiosqlite", "asyncio", "httpx", "aiohttp", "urllib3"]
        for logger_name in noisy_loggers:
            logging.getLogger(logger_name).setLevel(logging.WARNING)
        log.warning("Logging initialized in WARNING-only mode — INFO/DEBUG logs are suppressed")


async def log_upload(job_id: int, filename: str) -> None:
    log_path = settings.log_dir / "uploads.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)

    def append_to_file():
        with open(log_path, "a", encoding="utf-8") as f:
            f.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} - Job #{job_id} - Uploaded: {filename}\n")

    await asyncio.to_thread(append_to_file)


async def main() -> None:
    setup_logging()
    settings.validate_credentials()
    settings.data_dir.mkdir(parents=True, exist_ok=True)
    app = Client(
        "tgdl_bot",
        api_id=settings.tg_api_id,
        api_hash=settings.tg_api_hash,
        bot_token=settings.tg_bot_token,
        workdir=str(settings.data_dir),
    )
    register_all_handlers(app)

    await app.start()

    try:
        from pyrogram.types import BotCommand
        await app.set_bot_commands([
            BotCommand("m", "Mirror file/URL to GoFile, FileDitch & Pixeldrain"),
            BotCommand("dl", "Download direct HTTP link"),
            BotCommand("aria", "Download link or torrent using aria2 engine"),
            BotCommand("tor", "Download torrent or magnet link"),
            BotCommand("ts", "Search torrents across indexers & plugins"),
            BotCommand("gdl", "Batch download URLs from replied .txt file"),
            BotCommand("mega", "Download file or folder from Mega.nz"),
            BotCommand("gdlconf", "Manage user gallery-dl configuration"),
            BotCommand("gd2tg", "Download Google Drive link to Telegram"),
            BotCommand("gofile", "Upload replied media to GoFile"),
            BotCommand("fileditch", "Upload replied media to FileDitch"),
            BotCommand("pdup", "Upload replied media to Pixeldrain"),
            BotCommand("patch", "Decompile, patch, sign & upload Telegram APK"),
            BotCommand("setkeystore", "Set or manage your JKS keystore for signing"),
            BotCommand("unzip", "Download & extract archive"),
            BotCommand("status", "Show active tasks & status"),
            BotCommand("cancel", "Cancel active or queued jobs"),
            BotCommand("help", "Show command help guide"),
            BotCommand("start", "Start bot & get welcome message"),
        ])
        log.info("Bot commands set successfully on Telegram.")
    except Exception as e:
        log.warning("Failed to set bot commands: %s", e)

    from .downloader.aria2c.torrent import initiate_search_tools
    from .manager import cleanup_orphaned_directories, queue_manager, store
    await initiate_search_tools()
    await store.open()
    await cleanup_orphaned_directories()
    await queue_manager.start(app, store)

    log.info("Bot is active and listening for messages.")
    await idle()

    log.info("Shutting down bot...")
    await queue_manager.stop()
    await store.close()
    await delete_status()
    await app.stop()


if __name__ == "__main__":
    asyncio.run(main())
