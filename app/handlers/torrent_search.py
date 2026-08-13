from __future__ import annotations

import html
import logging

from pyrogram import Client, filters
from pyrogram.handlers import CallbackQueryHandler, MessageHandler
from pyrogram.types import (
    CallbackQuery,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    Message,
)

from app.telegraph import telegraph_helper
from ..downloader.aria2c.torrent import (
    SITES,
    MagnetioRPCError,
    format_search_results_html,
    search_torrents,
)

log = logging.getLogger(__name__)

PROVIDER_ALIASES: dict[str, str] = {
    "yts": "yts",
    "eztv": "eztv",
    "tpb": "thepiratebay",
    "piratebay": "thepiratebay",
    "thepiratebay": "thepiratebay",
    "tgx": "torrentgalaxy",
    "torrentgalaxy": "torrentgalaxy",
    "1337x": "leetx",
    "1337": "leetx",
    "leetx": "leetx",
    "kat": "kickasstorrents",
    "kickass": "kickasstorrents",
    "kickasstorrents": "kickasstorrents",
    "nyaa": "nyaa",
    "nyaasi": "nyaa",
    "lime": "limetorrents",
    "limetorrents": "limetorrents",
    "bitsearch": "bitsearch",
    "bt4g": "bt4g",
    "btdig": "btdig",
    "glo": "glotorrents",
    "glotorrents": "glotorrents",
    "torlock": "torlock",
    "td": "torrentdownloads",
    "torrentdownloads": "torrentdownloads",
    "rarbg": "therarbg",
    "therarbg": "therarbg",
    "subsplease": "subsplease",
    "animetosho": "animetosho",
    "tosho": "animetosho",
    "neko": "nekobt",
    "nekobt": "nekobt",
    "rutor": "rutor",
    "rutracker": "rutracker",
    "animesaturn": "animesaturn",
    "torznab": "torznab",
}

def parse_search_query_and_providers(cmd_text: str) -> tuple[str, list[str] | None]:
    """Parses command string for search query and provider flags (e.g. -yts, -tpb, -1337x, -p=nyaa).

    Returns:
        tuple[query, selected_providers]
    """
    parts = cmd_text.strip().split()
    if not parts or len(parts) <= 1:
        return "", None

    args = parts[1:]
    selected_providers: list[str] = []
    query_tokens: list[str] = []

    for arg in args:
        val = arg.strip()
        if val.startswith("-p=") or val.startswith("--provider="):
            p_val = val.split("=", 1)[1].strip().lower()
            canonical = PROVIDER_ALIASES.get(p_val, p_val)
            if canonical not in selected_providers:
                selected_providers.append(canonical)
            continue

        clean_arg = val.lstrip("-").lower()
        if val.startswith("-") and (clean_arg in PROVIDER_ALIASES or (SITES and clean_arg in SITES)):
            canonical = PROVIDER_ALIASES.get(clean_arg, clean_arg)
            if canonical not in selected_providers:
                selected_providers.append(canonical)
            continue

        query_tokens.append(arg)

    query = " ".join(query_tokens).strip()
    providers = selected_providers if selected_providers else None
    return query, providers

def build_search_keyboard(user_id: int, mode: str = "main") -> InlineKeyboardMarkup:
    """Builds inline keyboards for torrent search modes."""
    buttons = []

    if mode == "main":
        if SITES and len(SITES) > 1:
            return build_search_keyboard(user_id, "api_sites")

        buttons.append([
            InlineKeyboardButton("Trending", callback_data=f"torser:{user_id}:apitrend"),
            InlineKeyboardButton("Recent", callback_data=f"torser:{user_id}:apirecent"),
        ])
        buttons.append([InlineKeyboardButton("Cancel", callback_data=f"torser:{user_id}:cancel")])

    elif mode == "api_sites":
        site_btns = []
        if SITES:
            for site_key, site_name in SITES.items():
                site_btns.append(InlineKeyboardButton(site_name, callback_data=f"torser:{user_id}:apisearch:{site_key}"))
        for i in range(0, len(site_btns), 2):
            buttons.append(site_btns[i:i + 2])
        buttons.append([InlineKeyboardButton("Cancel", callback_data=f"torser:{user_id}:cancel")])

    return InlineKeyboardMarkup(buttons)


async def handle_torrent_search(client: Client, message: Message) -> None:
    """Command handler for /torsearch, /ts, and /search."""
    user_id = message.from_user.id if message.from_user else message.chat.id
    cmd_text = message.text or ""
    query, selected_providers = parse_search_query_and_providers(cmd_text)

    if not query:
        kb = build_search_keyboard(user_id, "main")
        hint_msg = (
            "<b>Torrent Search</b>\n"
            "Send a search term with provider flags, e.g.:\n"
            "• <code>/ts Avatar</code> (all providers)\n"
            "• <code>/ts -yts Avatar</code> (YTS only)\n"
            "• <code>/ts -tpb -1337x Oppenheimer</code> (TPB + 1337x)\n"
            "• <code>/ts -nyaa Naruto</code> (Nyaa anime)"
        )
        await message.reply_text(hint_msg, reply_markup=kb if SITES and len(SITES) > 1 else None)
        return

    safe_query = html.escape(query)

    if selected_providers:
        provider_names = [SITES.get(p, p.capitalize()) if SITES else p.capitalize() for p in selected_providers]
        site_label = ", ".join(provider_names)
    else:
        site_label = "All Providers"

    status_msg = await message.reply_text(f"<b>Searching torrents ({html.escape(site_label)}) for:</b> <code>{safe_query}</code>...")
    try:
        results = await search_torrents(query, site=selected_providers or "all", method="apisearch")
        telegraph_url = await telegraph_helper.generate_telegraph_page(results, query, site_label)
        if telegraph_url:
            reply_kb = InlineKeyboardMarkup([[InlineKeyboardButton("VIEW", url=telegraph_url)]])
            msg = f"<b>Found {len(results)} result(s) for <i>{safe_query}</i>\nSource: <i>{html.escape(site_label)}</i></b>"
            await status_msg.edit_text(msg, reply_markup=reply_kb)
        else:
            formatted_html = format_search_results_html(results, query, site_label)
            await status_msg.edit_text(formatted_html, disable_web_page_preview=True)
    except MagnetioRPCError as e:
        log.warning("Torrent search backend unavailable: %s", e)
        await status_msg.edit_text("<b>Search backend is unavailable right now, try again shortly.</b>")
    except Exception as e:
        log.exception("Torrent search failed: %s", e)
        await status_msg.edit_text(f"<b>Search error:</b> {e}")


async def handle_torrent_search_callback(client: Client, callback: CallbackQuery) -> None:
    """Callback query handler for torrent search buttons."""
    data = callback.data or ""
    if not data.startswith("torser:"):
        return

    parts = data.split(":")
    if len(parts) < 3:
        await callback.answer("Invalid callback data", show_alert=True)
        return

    target_user_id = int(parts[1])
    action = parts[2]

    user_id = callback.from_user.id if callback.from_user else callback.message.chat.id
    if user_id != target_user_id:
        await callback.answer("This search menu is not yours!", show_alert=True)
        return

    if action == "cancel":
        await callback.answer("Cancelled search.")
        await callback.message.edit_text("Search cancelled.")
        return

    if action == "apisearch" and len(parts) == 3:
        await callback.answer()
        kb = build_search_keyboard(target_user_id, "api_sites")
        await callback.message.edit_text("<b>Select Search API Site:</b>", reply_markup=kb)
        return

    if action == "plugin" and len(parts) == 3:
        await callback.answer()
        kb = build_search_keyboard(target_user_id, "plugin_sites")
        await callback.message.edit_text("<b>Select Plugin Site:</b>", reply_markup=kb)
        return

    # Extract search query from original message or reply message
    query_text = ""
    orig_text = callback.message.text or ""
    if "Searching for:" in orig_text:
        query_text = orig_text.split("Searching for:")[1].split("\n")[0].strip().strip("<code>").strip("</code>")
    elif callback.message.reply_to_message and callback.message.reply_to_message.text:
        q_parts = callback.message.reply_to_message.text.split(maxsplit=1)
        if len(q_parts) > 1:
            query_text = q_parts[1].strip()

    site = parts[3] if len(parts) > 3 else "all"
    method = action

    await callback.answer("Searching torrents...")
    await callback.message.edit_text(f"<b>Searching torrents for:</b> <code>{query_text or 'trending'}</code>...")

    try:
        results = await search_torrents(query_text, site=site, method=method)
        telegraph_url = await telegraph_helper.generate_telegraph_page(results, query_text or "trending", site)
        if telegraph_url:
            reply_kb = InlineKeyboardMarkup([[InlineKeyboardButton("VIEW", url=telegraph_url)]])
            msg = f"<b>Found {len(results)} result(s) for <i>{html.escape(query_text or 'trending')}</i>\nSource: <i>{html.escape(site.capitalize())}</i></b>"
            await callback.message.edit_text(msg, reply_markup=reply_kb)
        else:
            formatted_html = format_search_results_html(results, query_text or "trending", site)
            await callback.message.edit_text(formatted_html, disable_web_page_preview=True)
    except MagnetioRPCError as e:
        log.warning("Torrent search backend unavailable: %s", e)
        await callback.message.edit_text("<b>Search backend is unavailable right now, try again shortly.</b>")
    except Exception as e:
        log.exception("Torrent search failed: %s", e)
        await callback.message.edit_text(f"<b>Search error:</b> {e}")


from ..auth import authorized_filter


def register_torrent_search_handlers(app: Client) -> None:
    """Registers torrent search handlers on Pyrogram Client."""
    app.add_handler(MessageHandler(handle_torrent_search, filters.command(["torsearch", "ts", "search"]) & authorized_filter))
    app.add_handler(CallbackQueryHandler(handle_torrent_search_callback, filters.regex(r"^torser:") & authorized_filter))
