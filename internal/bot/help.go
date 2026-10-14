package bot

import "github.com/burhanverse/tgdl/internal/tg"

var helpPages = map[string]string{
	"main": "<b>tgdl</b> — download from almost anywhere and upload to Telegram.\n\n" +
		"Pick a topic below. Quick start: <code>/dl &lt;url&gt;</code>, <code>/tor &lt;magnet&gt;</code>, <code>/mega &lt;link&gt;</code>, <code>/gd2tg &lt;link&gt;</code>.\n" +
		"Use /status to watch jobs and /cancel to stop them.",
	"dl": "<b>Downloads</b>\n" +
		"• <code>/dl [-m] [-tg] [-uz] [-p pass] &lt;url…&gt;</code> — direct HTTP/HLS links\n" +
		"• <code>/gofile [-m] [-tg] [-uz] &lt;url…&gt;</code> or <code>/gfdl</code> — GoFile bypass proxy links\n" +
		"• <code>/gdl</code> — gallery-dl (reply to a .txt of links for batches)\n" +
		"• <code>/cdl</code> — cyberdrop-dl\n" +
		"• <code>/m [-to hosts] [-tg] [-uz]</code> — mirror to enabled hosts (or specific with -to)\n" +
		"• <code>/gofile</code>, <code>/fileditch</code>, <code>/pixeldrain</code> — mirror to a specific host\n" +
		"<code>-to</code> selects mirror hosts (e.g. -to gofile,pd), <code>-m</code> mirrors to web hosts, <code>-tg</code> also uploads to Telegram, <code>-uz</code> extracts archives.",
	"tor": "<b>Torrents &amp; aria2</b>\n" +
		"• <code>/tor &lt;magnet | .torrent URL&gt;</code> or reply to a .torrent file\n" +
		"• <code>/aria &lt;url&gt; [-c N] [-s N] [--header 'K: V'] [--out NAME] [--opt k=v]</code>\n" +
		"• <code>/ts [-p provider] &lt;query&gt;</code> — search torrent indexers",
	"unzip": "<b>Archives</b>\n" +
		"• <code>/unzip [-p pass]</code> — reply to an archive (or pass a URL); split volumes are detected automatically\n" +
		"Encrypted archives prompt for a password (reply to the prompt).\n" +
		"Archives found in normal jobs ask whether to extract them too.",
	"cloud": "<b>Cloud &amp; hosts</b>\n" +
		"• <code>/gd2tg &lt;link&gt;</code> — Google Drive file or folder (or reply with credentials .json)\n" +
		"• <code>/mega &lt;link&gt;</code> — MEGA file or folder\n" +
		"• <code>/gofile &lt;link&gt;</code> — GoFile download via bypass proxy\n" +
		"• <code>/m</code>, <code>/gofile</code>, <code>/fileditch</code>, <code>/pixeldrain</code> (aliases: <code>/gf</code>, <code>/fd</code>, <code>/pd</code>) — mirror URLs or replied files\n" +
		"• <code>/gofilekey</code>, <code>/pdkey</code> — store your personal API keys",
	"cookies": "<b>Cookies</b>\n" +
		"• <code>/cookies</code> — reply to cookies.txt to save it (used by gallery-dl and cyberdrop-dl)\n" +
		"• <code>/cookies delete</code> — delete saved cookies",
	"rename": "<b>Rename</b>\n" +
		"• Reply to a file with <code>/rename &lt;new name&gt;</code>. The extension is kept automatically.\n" +
		"• Several files: <code>/rename batch</code>, send the files, then send one name per line.",
	"settings": "<b>Owner Settings</b>\n" +
		"Owner-only configuration management inside Telegram:\n" +
		"• <code>/settings</code> — open interactive settings dashboard\n" +
		"• <code>/set &lt;KEY&gt; &lt;value&gt;</code> — change setting live\n" +
		"• <code>/get &lt;KEY&gt;</code> — view setting value and details\n" +
		"• <code>/unset &lt;KEY&gt;</code> — revert override to default/env\n" +
		"• <code>/settings changed</code> — view all active overrides\n" +
		"• <code>/settings history</code> — view change history and undo\n" +
		"• <code>/settings export</code> — export JSON with secrets redacted\n" +
		"• <code>/settings import</code> — import JSON with diff confirmation\n" +
		"• <code>/restart</code> — cleanly restart bot container",
}

func helpPage(page string) (string, tg.Keyboard) {
	text, ok := helpPages[page]
	if !ok {
		text, page = helpPages["main"], "main"
	}
	b := func(label, data string) tg.Button { return tg.Button{Text: label, Data: "help_page:" + data} }
	kb := tg.Keyboard{
		{b("Downloads", "dl"), b("Torrents", "tor")},
		{b("Archives", "unzip"), b("Cloud", "cloud")},
		{b("Rename", "rename"), b("Cookies", "cookies")},
		{b("Settings", "settings")},
	}
	if page != "main" {
		kb = append(kb, []tg.Button{b("Back", "main")})
	}
	return text, append(kb, []tg.Button{b("Close", "close")})
}
