package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/tg"
)

func (a *App) cmdHelp(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	text, kb := helpPage("main")
	if _, err := a.tg.Send(ctx, u.Message.Chat.ID, text, tg.SendOpts{Keyboard: kb, ReplyTo: u.Message.ID}); err != nil {
		slog.Warn("send help failed", "err", err)
	}
}

// urlsFromText collects URL-looking tokens from free text.
func urlsFromText(text string) []string {
	var out []string
	for _, t := range strings.Fields(text) {
		if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") || strings.HasPrefix(t, "magnet:") {
			out = append(out, t)
		}
	}
	return out
}

// urlsFromTextFile downloads a replied .txt document and returns its URLs.
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
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			out = append(out, line)
		}
	}
	return out, nil
}

// collectURLs merges URLs from the command tokens, a replied text message and
// a replied .txt document.
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

func (a *App) cmdMirror(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	toks := tokens(m)
	upTG := false
	var link string
	for _, t := range toks {
		switch strings.ToLower(t) {
		case "-tg":
			upTG = true
		case "-m", "-mirror":
		default:
			if link == "" && strings.HasPrefix(t, "http") {
				link = t
			}
		}
	}
	args := dl.Args{IsMirror: true, UploadTG: upTG}
	if _, f := mediaSource(m); f != nil && link == "" {
		args.TGFile = f
		a.enqueue(ctx, m, "mirror_tg:"+f.Name, args)
		return
	}
	if link == "" && m.ReplyToMessage != nil {
		if urls := urlsFromText(textOf(m.ReplyToMessage)); len(urls) > 0 {
			link = urls[0]
		}
	}
	if link == "" {
		a.reply(ctx, m, "Provide a URL or reply to a Telegram file with <code>/m [-tg]</code>.")
		return
	}
	a.enqueue(ctx, m, "mirror:"+link, args)
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

func (a *App) cmdMega(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	var urls []string
	for _, t := range tokens(m) {
		if strings.Contains(t, "mega.") || strings.HasPrefix(t, "mega:") {
			urls = append(urls, t)
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
		a.reply(ctx, m, "Usage: <code>/mega &lt;mega.nz link&gt;</code>")
		return
	}
	a.enqueue(ctx, m, jobTarget(urls), dl.Args{})
}

func (a *App) cmdDrive(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	urls := a.collectURLs(ctx, m, parseFlags(tokens(m)).URLs)
	if len(urls) == 0 {
		a.reply(ctx, m, "Usage: <code>/gd2tg &lt;Google Drive link&gt;</code>")
		return
	}
	for i, x := range urls {
		if !strings.HasPrefix(x, "gdrive:") && !strings.HasPrefix(x, "gd2tg:") {
			urls[i] = "gdrive:" + x
		}
	}
	a.enqueue(ctx, m, jobTarget(urls), dl.Args{})
}

// torrentFileTarget saves a replied .torrent document and returns its job target.
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

// parseAriaFlags extracts aria2 options and remaining URL tokens.
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
