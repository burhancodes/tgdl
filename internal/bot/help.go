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
		"• <code>/xenforo &lt;thread url&gt;</code> — forum threads (needs cookies for private forums)\n" +
		"• <code>/m [-tg]</code> — mirror a link or replied file to GoFile, FileDitch and Pixeldrain\n" +
		"<code>-m</code> mirrors to web hosts, <code>-tg</code> also uploads to Telegram, <code>-uz</code> extracts archives.",
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
		"• <code>/gfup</code>, <code>/fileditch</code>, <code>/pdup</code> — upload a replied file\n" +
		"• <code>/gofilekey</code>, <code>/pdkey</code> — store your personal API keys",
	"config": "<b>Configuration</b>\n" +
		"• <code>/gdlconf</code> — reply to gallery-dl.conf or cookies.txt to save it\n" +
		"• <code>/setkeystore</code> and <code>/patch</code> — sign and patch Telegram APKs",
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
		{b("Config", "config")},
	}
	if page != "main" {
		kb = append(kb, []tg.Button{b("Back", "main")})
	}
	return text, append(kb, []tg.Button{b("Close", "close")})
}
