package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/upload"
)

func (a *App) cmdHelp(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	text, kb := helpPage("main")
	if _, err := a.tg.Send(ctx, u.Message.Chat.ID, text, tg.SendOpts{Keyboard: kb, ReplyTo: u.Message.ID}); err != nil {
		slog.Warn("send help failed", "err", err)
	}
}

func urlsFromText(text string) []string {
	var out []string
	for _, t := range strings.Fields(text) {
		if isURLToken(t) {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) urlsFromTextFile(ctx context.Context, f *dl.TGFile) ([]string, error) {
	dir, err := os.MkdirTemp(a.cfg.DownloadsDir(), "tmp_txt_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, dir, nil)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > 2<<20 {
		return nil, errors.New("URL list file exceeds 2 MiB limit")
	}
	file, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []string
	sc := bufio.NewScanner(io.LimitReader(file, 2<<20))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if isURLToken(line) {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func (a *App) collectURLs(ctx context.Context, m *models.Message, fromTokens []string) []string {
	urls := append([]string(nil), fromTokens...)
	if len(urls) > 0 || m.ReplyToMessage == nil {
		return urls
	}
	r := m.ReplyToMessage
	if r.Document != nil && strings.HasSuffix(strings.ToLower(r.Document.FileName), ".txt") {
		if list, err := a.urlsFromTextFile(ctx, mediaOf(r)); err == nil {
			return list
		} else {
			slog.Warn("read replied text file failed", "err", err)
		}
	}
	return urlsFromText(textOf(r))
}

func (a *App) cmdMirrorTo(hosts []upload.HostID) tgbot.HandlerFunc {
	return func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
		m := u.Message
		f := parseFlags(tokens(m))
		if len(hosts) > 0 && f.ToHosts != "" {
			a.reply(ctx, m, "-to cannot be used with host-specific commands")
			return
		}
		if len(hosts) == 1 && hosts[0] == upload.GoFile {
			urls := a.collectURLs(ctx, m, f.URLs)
			hasGofile := false
			for _, x := range urls {
				if dl.IsGofileURL(a.cfg, x) {
					hasGofile = true
					break
				}
			}
			if !hasGofile {
				for _, t := range tokens(m) {
					if !strings.HasPrefix(t, "-") && dl.IsGofileURL(a.cfg, t) {
						hasGofile = true
						break
					}
				}
			}
			if hasGofile {
				a.cmdGofileDL(ctx, nil, u)
				return
			}
		}
		var mirrorHosts []string
		if f.ToHosts != "" {
			parsed, err := upload.ParseHosts(f.ToHosts)
			if err != nil {
				a.reply(ctx, m, err.Error())
				return
			}
			for _, h := range parsed {
				mirrorHosts = append(mirrorHosts, string(h))
			}
		} else if len(hosts) > 0 {
			for _, h := range hosts {
				mirrorHosts = append(mirrorHosts, string(h))
			}
		}

		urls := a.collectURLs(ctx, m, f.URLs)
		args := dl.Args{
			IsMirror:    true,
			UploadTG:    f.TG,
			Unzip:       f.Unzip,
			Password:    f.Password,
			MirrorHosts: mirrorHosts,
		}
		if len(urls) > 0 {
			a.enqueue(ctx, m, "mirror:"+jobTarget(urls), args)
			return
		}
		if _, file := mediaSource(m); file != nil {
			args.TGFile = file
			a.enqueue(ctx, m, "mirror_tg:"+file.Name, args)
			return
		}
		if len(hosts) == 0 {
			a.reply(ctx, m, "Usage: <code>/m [-to hosts] [-tg] [-uz] &lt;url…&gt;</code> (or reply to a media file or URL list).")
		} else {
			a.reply(ctx, m, fmt.Sprintf("Usage: <code>/%s [-tg] [-uz] &lt;url…&gt;</code> (or reply to a media file or URL list).", hosts[0]))
		}
	}
}

func (a *App) cmdDirect(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	f := parseFlags(tokens(m))
	urls := a.collectURLs(ctx, m, f.URLs)
	if len(urls) == 0 {
		a.reply(ctx, m, "Usage: <code>/dl [-m] [-tg] [-uz] [-p password] &lt;url…&gt;</code> (or reply to a message containing links).")
		return
	}
	a.enqueue(ctx, m, jobTarget(urls), dl.Args{Engine: "direct", IsMirror: f.Mirror, UploadTG: f.TG || !f.Mirror,
		Unzip: f.Unzip, Password: f.Password})
}

func (a *App) cmdEngine(engine, name string) tgbot.HandlerFunc {
	return func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
		m := u.Message
		f := parseFlags(tokens(m))
		urls := a.collectURLs(ctx, m, f.URLs)
		if len(urls) == 0 {
			a.reply(ctx, m, fmt.Sprintf("Usage: <code>/%s &lt;url…&gt;</code> or reply to a message or <code>.txt</code> file containing links.", name))
			return
		}
		a.enqueue(ctx, m, jobTarget(urls), dl.Args{Engine: engine, Password: f.Password, ExtraArgs: f.Extra,
			IsMirror: f.Mirror, UploadTG: f.TG || !f.Mirror, Unzip: f.Unzip})
	}
}

func (a *App) cmdGofileDL(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	f := parseFlags(tokens(m))
	urls := a.collectURLs(ctx, m, f.URLs)

	if len(urls) == 0 {
		for _, t := range tokens(m) {
			if !strings.HasPrefix(t, "-") && dl.IsGofileURL(a.cfg, t) {
				urls = append(urls, t)
			}
		}
	}

	if len(urls) == 0 {
		a.reply(ctx, m, "Please provide a GoFile link to download (e.g. <code>/gfdl &lt;url&gt;</code>).")
		return
	}

	for i, x := range urls {
		if !strings.HasPrefix(x, "gofile:") && !strings.HasPrefix(x, "gf:") && !strings.HasPrefix(x, "gfdl:") && !strings.HasPrefix(x, "gf2tg:") {
			urls[i] = "gofile:" + x
		}
	}

	a.enqueue(ctx, m, jobTarget(urls), dl.Args{
		IsMirror: f.Mirror,
		UploadTG: f.TG || !f.Mirror,
		Unzip:    f.Unzip,
		Password: f.Password,
	})
}

func (a *App) cmdMega(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	f := parseFlags(tokens(m))
	urls := a.collectURLs(ctx, m, f.URLs)
	if len(urls) == 0 {
		for _, t := range tokens(m) {
			if strings.Contains(t, "mega.") || strings.HasPrefix(t, "mega:") {
				urls = append(urls, t)
			}
		}
	}
	if len(urls) == 0 && m.ReplyToMessage != nil {
		for _, t := range strings.Fields(textOf(m.ReplyToMessage)) {
			if strings.Contains(t, "mega.") {
				urls = append(urls, t)
			}
		}
	}
	if len(urls) == 0 {
		a.reply(ctx, m, "Usage: <code>/mega [-m] [-tg] [-uz] [-p password] &lt;mega.nz link&gt;</code> (or reply to a message or .txt file containing links)")
		return
	}
	for i, x := range urls {
		if !strings.HasPrefix(x, "mega:") {
			urls[i] = "mega:" + x
		}
	}
	a.enqueue(ctx, m, jobTarget(urls), dl.Args{
		IsMirror: f.Mirror,
		UploadTG: f.TG || !f.Mirror,
		Unzip:    f.Unzip,
		Password: f.Password,
	})
}

func (a *App) cmdDrive(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	uid := userID(m)

	if m.ReplyToMessage != nil && m.ReplyToMessage.Document != nil && strings.HasSuffix(strings.ToLower(m.ReplyToMessage.Document.FileName), ".json") {
		if uid <= 0 {
			a.reply(ctx, m, "Could not determine your user ID. Credential upload must be performed by an identified user.")
			return
		}
		userAuthDir := filepath.Join(a.cfg.AuthDir, strconv.FormatInt(uid, 10))
		tmpDir, err := os.MkdirTemp(a.cfg.DownloadsDir(), "tmp_gd_cred_")
		if err != nil {
			a.reply(ctx, m, "Storage error: "+code(err.Error()))
			return
		}
		defer os.RemoveAll(tmpDir)

		f := mediaOf(m.ReplyToMessage)
		p, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, tmpDir, nil)
		if err != nil {
			a.reply(ctx, m, "Download failed: "+code(err.Error()))
			return
		}
		st, err := os.Stat(p)
		if err != nil {
			a.reply(ctx, m, "Read failed: "+code(err.Error()))
			return
		}
		if st.Size() > 2<<20 {
			a.reply(ctx, m, "File too large: credentials file must be under 2 MiB.")
			return
		}
		data, err := os.ReadFile(p)
		if err != nil {
			a.reply(ctx, m, "Read failed: "+code(err.Error()))
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			a.reply(ctx, m, "Invalid JSON file: "+code(err.Error()))
			return
		}

		if err := os.MkdirAll(userAuthDir, 0o700); err != nil {
			a.reply(ctx, m, "Failed to create auth directory: "+code(err.Error()))
			return
		}

		if parsed["type"] == "service_account" {
			saDir := filepath.Join(userAuthDir, "accounts")
			_ = os.MkdirAll(saDir, 0o700)
			safeName := filepath.Base(f.Name)
			if safeName == "" || safeName == "." {
				safeName = "service_account.json"
			}
			dest := filepath.Join(saDir, safeName)
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				a.reply(ctx, m, "Failed to save service account: "+code(err.Error()))
				return
			}
			a.reply(ctx, m, fmt.Sprintf("✓ Service Account JSON saved for user <code>%d</code> to <code>%s</code>.\nYou can now use <code>/gd2tg &lt;link&gt;</code> to download Google Drive links!", uid, esc(safeName)))
			return
		} else if _, hasToken := parsed["token"]; hasToken || parsed["refresh_token"] != nil {
			dest := filepath.Join(userAuthDir, "token.json")
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				a.reply(ctx, m, "Failed to save OAuth token: "+code(err.Error()))
				return
			}
			a.reply(ctx, m, fmt.Sprintf("✓ OAuth token saved for user <code>%d</code> (<code>token.json</code>).\nYou can now use <code>/gd2tg &lt;link&gt;</code> to download Google Drive links!", uid))
			return
		} else if _, hasInst := parsed["installed"]; hasInst || parsed["web"] != nil || parsed["client_id"] != nil {
			dest := filepath.Join(userAuthDir, "credentials.json")
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				a.reply(ctx, m, "Failed to save OAuth credentials: "+code(err.Error()))
				return
			}
			a.reply(ctx, m, fmt.Sprintf("✓ OAuth client credentials saved for user <code>%d</code> (<code>credentials.json</code>).", uid))
			return
		} else {
			a.reply(ctx, m, "Unrecognized JSON structure. Expected a Google Cloud Service Account JSON key or OAuth token (<code>token.json</code> / <code>credentials.json</code>).")
			return
		}
	}

	f := parseFlags(tokens(m))
	urls := a.collectURLs(ctx, m, f.URLs)
	if len(urls) == 0 {
		a.reply(ctx, m, "Usage: <code>/gd2tg [-m] [-tg] [-uz] [-p password] &lt;Google Drive link&gt;</code> (or reply to a message, .txt file containing links, or .json credential file)")
		return
	}
	for i, x := range urls {
		if !strings.HasPrefix(x, "gdrive:") && !strings.HasPrefix(x, "gd2tg:") {
			urls[i] = "gdrive:" + x
		}
	}
	a.enqueue(ctx, m, jobTarget(urls), dl.Args{
		IsMirror: f.Mirror,
		UploadTG: f.TG || !f.Mirror,
		Unzip:    f.Unzip,
		Password: f.Password,
	})
}

func (a *App) torrentFileTarget(ctx context.Context, f *dl.TGFile) (string, error) {
	dir := filepath.Join(a.cfg.DataDir, "torrents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, dir, nil)
	if err != nil {
		return "", err
	}
	return "torrent:" + p, nil
}

func (a *App) cmdTorrent(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	var target string
	for _, t := range tokens(m) {
		if strings.HasPrefix(t, "magnet:") || strings.HasPrefix(t, "http") {
			target = t
			break
		}
	}
	if target == "" && m.ReplyToMessage != nil {
		r := m.ReplyToMessage
		if r.Document != nil && strings.HasSuffix(strings.ToLower(r.Document.FileName), ".torrent") {
			t, err := a.torrentFileTarget(ctx, mediaOf(r))
			if err != nil {
				a.reply(ctx, m, "Could not fetch the .torrent file: "+code(err.Error()))
				return
			}
			target = t
		} else {
			for _, t := range strings.Fields(textOf(r)) {
				if strings.HasPrefix(t, "magnet:") {
					target = t
					break
				}
			}
		}
	}
	if target == "" {
		a.reply(ctx, m, "Usage: <code>/tor &lt;magnet | .torrent URL&gt;</code> or reply to a <code>.torrent</code> file.")
		return
	}
	a.enqueue(ctx, m, target, dl.Args{Engine: "aria2"})
}

var ariaFlags = map[string]string{
	"-c": "max-connection-per-server", "--connections": "max-connection-per-server",
	"-s": "split", "--split": "split", "--min-split-size": "min-split-size",
	"--max-tries": "max-tries", "--retry-wait": "retry-wait", "--header": "header",
	"--ua": "user-agent", "--referer": "referer", "--proxy": "all-proxy",
	"--checksum": "checksum", "--out": "out", "--speed": "max-download-limit",
}

func parseAriaFlags(toks []string, defaultLimit string) (map[string]any, []string) {
	opts := map[string]any{}
	set := func(k, v string) {
		switch cur := opts[k].(type) {
		case nil:
			if k == "header" {
				opts[k] = []string{v}
			} else {
				opts[k] = v
			}
		case []string:
			opts[k] = append(cur, v)
		case string:
			opts[k] = []string{cur, v}
		}
	}
	var rest []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		low := strings.ToLower(t)
		if eq := strings.Index(t, "="); eq > 0 && strings.HasPrefix(t, "-") {
			flag := strings.ToLower(t[:eq])
			if k, ok := ariaFlags[flag]; ok {
				set(k, t[eq+1:])
				continue
			}
			if flag == "--opt" {
				if kv := strings.SplitN(t[eq+1:], "=", 2); len(kv) == 2 {
					set(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
				}
				continue
			}
		}
		if k, ok := ariaFlags[low]; ok {
			if i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
				i++
				set(k, toks[i])
			}
			continue
		}
		if low == "--opt" {
			if i+1 < len(toks) {
				i++
				if kv := strings.SplitN(toks[i], "=", 2); len(kv) == 2 {
					set(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
				}
			}
			continue
		}
		rest = append(rest, t)
	}
	if _, ok := opts["max-download-limit"]; !ok && pacing.ParseSpeedLimit(defaultLimit) > 0 {
		opts["max-download-limit"] = defaultLimit
	}
	return opts, rest
}

func (a *App) cmdAria(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	opts, rest := parseAriaFlags(tokens(m), a.cfg.GlobalSpeedLimit)
	var target string
	for _, t := range rest {
		if isURLToken(t) || strings.HasPrefix(t, "ftp://") {
			target = t
			break
		}
	}
	if target == "" && m.ReplyToMessage != nil {
		if r := m.ReplyToMessage; r.Document != nil && strings.HasSuffix(strings.ToLower(r.Document.FileName), ".torrent") {
			if t, err := a.torrentFileTarget(ctx, mediaOf(r)); err == nil {
				target = t
			}
		} else if urls := urlsFromText(textOf(r)); len(urls) > 0 {
			target = urls[0]
		}
	}
	if target == "" {
		a.reply(ctx, m, "Usage: <code>/aria &lt;url|magnet&gt; [-c N] [-s N] [--header 'K: V'] [--ua UA] [--referer URL] [--out NAME] [--opt key=value]</code>")
		return
	}
	a.enqueue(ctx, m, target, dl.Args{Engine: "aria2", AriaOptions: opts})
}

func (a *App) cmdUnzip(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	f := parseFlags(tokens(m))
	args := dl.Args{Unzip: true, Password: f.Password}
	if _, file := mediaSource(m); file != nil {
		args.TGFile = file
		a.enqueue(ctx, m, "unzip:"+file.Name, args)
		return
	}
	urls := a.collectURLs(ctx, m, f.URLs)
	if len(urls) == 0 {
		a.reply(ctx, m, "Usage: reply to an archive with <code>/unzip [-p password]</code>, or <code>/unzip &lt;url&gt;</code>.")
		return
	}
	a.enqueue(ctx, m, jobTarget(urls), args)
}

var _ = time.Second
