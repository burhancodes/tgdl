package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/torrent"
)

// ---- status / cancel --------------------------------------------------------

func (a *App) statusText(chatFilter int64) string {
	states := a.mgr.Running(chatFilter)
	if len(states) == 0 {
		return "No active tasks."
	}
	var parts []string
	total := 0
	for _, st := range states {
		card := a.mgr.Render(st)
		if total+len(card) > 3600 {
			parts = append(parts, fmt.Sprintf("…and %d more task(s)", len(states)-len(parts)))
			break
		}
		total += len(card)
		parts = append(parts, card)
	}
	return strings.Join(parts, "\n\n")
}

func (a *App) cmdStatus(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	filter := m.Chat.ID
	if a.auth.IsOwner(userID(m)) {
		if t := tokens(m); len(t) > 0 && strings.EqualFold(t[0], "all") {
			filter = 0
		}
	}
	kb := tg.Keyboard{{tg.Button{Text: "Refresh", Data: fmt.Sprintf("status:ref:%d", filter)}}}
	a.replyKB(ctx, m, a.statusText(filter), kb)
}

func (a *App) cbStatus(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}
	var filter int64
	fmt.Sscanf(strings.TrimPrefix(q.Data, "status:ref:"), "%d", &filter)
	if filter == 0 && !a.auth.IsOwner(q.From.ID) || filter != 0 && filter != chatID && !a.auth.IsOwner(q.From.ID) {
		a.ack(ctx, q, "Not yours!", true)
		return
	}
	a.ack(ctx, q, "", false)
	kb := tg.Keyboard{{tg.Button{Text: "Refresh", Data: fmt.Sprintf("status:ref:%d", filter)}}}
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, a.statusText(filter), tg.SendOpts{Keyboard: kb})
}

func cbMessage(q *models.CallbackQuery) (chatID int64, msgID int, ok bool) {
	if q.Message.Message != nil {
		return q.Message.Message.Chat.ID, q.Message.Message.ID, true
	}
	return 0, 0, false
}

func (a *App) isJobOwner(chatID, uid int64, j *store.Job) bool {
	return j.ChatID == chatID || a.auth.IsOwner(uid)
}

// cancelJob cancels a running job or marks a not-yet-started one cancelled.
func (a *App) cancelJob(ctx context.Context, j *store.Job) {
	if !a.mgr.Cancel(j.ID) {
		_ = a.store.SetStatus(ctx, j.ID, store.StatusCancelled)
	}
}

func (a *App) cmdCancel(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	if t := tokens(m); len(t) > 0 {
		id := strings.TrimPrefix(t[0], "#")
		j, err := a.store.GetJob(ctx, id)
		if err != nil || !a.isJobOwner(m.Chat.ID, userID(m), j) {
			a.reply(ctx, m, fmt.Sprintf("Job #%s not found or not owned by you.", esc(id)))
			return
		}
		if j.Status.IsTerminal() {
			a.reply(ctx, m, fmt.Sprintf("Job #%s is already %s.", esc(id), code(string(j.Status))))
			return
		}
		a.cancelJob(ctx, j)
		a.reply(ctx, m, fmt.Sprintf("Job #%s has been cancelled.", esc(j.ID)))
		return
	}
	active, err := a.store.ActiveJobsForChat(ctx, m.Chat.ID)
	if err != nil || len(active) == 0 {
		a.reply(ctx, m, "No active or queued jobs found for this chat.")
		return
	}
	if len(active) == 1 {
		a.cancelJob(ctx, active[0])
		a.reply(ctx, m, fmt.Sprintf("Job #%s (%s) has been cancelled.", esc(active[0].ID), string(active[0].Status)))
		return
	}
	var kb tg.Keyboard
	for _, j := range active {
		label := status.Short(filepath.Base(dl.FirstURL(j.URL)), 25)
		kb = append(kb, []tg.Button{{Text: fmt.Sprintf("#%s - %s (%s)", j.ID, label, j.Status), Data: "cancel_job:" + j.ID}})
	}
	a.replyKB(ctx, m, "<b>Select a job to cancel:</b>", kb)
}

// ---- upload keys ------------------------------------------------------------

func mask(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return k[:4] + strings.Repeat("*", len(k)-4)
}

func (a *App) cmdKey(service, label string) tgbot.HandlerFunc {
	return func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
		m := u.Message
		uid := userID(m)
		if uid <= 0 {
			a.reply(ctx, m, "Error: cannot identify your user ID.")
			return
		}
		arg := strings.TrimSpace(strings.TrimPrefix(textOf(m), strings.Fields(textOf(m))[0]))
		cmdName := commandOf(textOf(m))
		switch {
		case arg == "":
			if cur := a.keys.Get(uid, service); cur != "" {
				a.reply(ctx, m, fmt.Sprintf("<b>%s API key</b>: set (%s)\n\nUpdate: <code>/%s your_key</code>\nDelete: <code>/%s delete</code>", label, code(mask(cur)), cmdName, cmdName))
			} else {
				a.reply(ctx, m, fmt.Sprintf("<b>%s API key</b>: not set\n\nProvide yours: <code>/%s your_key</code>", label, cmdName))
			}
		case strings.EqualFold(arg, "delete") || strings.EqualFold(arg, "del") || strings.EqualFold(arg, "remove") || strings.EqualFold(arg, "clear"):
			_ = a.keys.Delete(uid, service)
			a.reply(ctx, m, fmt.Sprintf("Deleted your personal %s API key.", label))
		default:
			if err := a.keys.Save(uid, service, arg); err != nil {
				a.reply(ctx, m, "Could not save the key: "+code(err.Error()))
				return
			}
			_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID}) // keep secrets out of the chat
			a.reply(ctx, m, fmt.Sprintf("Saved your personal %s API key (%s).", label, code(mask(arg))))
		}
	}
}

// ---- direct host uploads ----------------------------------------------------

func (a *App) cmdHostUpload(host string) tgbot.HandlerFunc {
	return func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
		m := u.Message
		_, f := mediaSource(m)
		if f == nil {
			a.reply(ctx, m, "Reply to a Telegram file with this command to upload it.")
			return
		}
		go a.hostUpload(context.WithoutCancel(ctx), m, f, host)
	}
}

func (a *App) hostUpload(ctx context.Context, m *models.Message, f *dl.TGFile, host string) {
	ref, err := a.tg.Send(ctx, m.Chat.ID, fmt.Sprintf("Downloading %s…", code(f.Name)), tg.SendOpts{ReplyTo: m.ID})
	if err != nil {
		return
	}
	dir, err := os.MkdirTemp(a.cfg.DownloadsDir(), "tmp_up_")
	if err != nil {
		_ = a.tg.Edit(ctx, ref, "Upload failed: "+code(err.Error()), tg.SendOpts{})
		return
	}
	defer os.RemoveAll(dir)

	var last time.Time
	edit := func(text string) {
		if time.Since(last) < 3*time.Second {
			return
		}
		last = time.Now()
		if err := a.lim.Acquire(ctx, m.Chat.ID); err == nil {
			_ = a.tg.Edit(ctx, ref, text, tg.SendOpts{})
		}
	}
	path, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, dir, func(cur, total int64) {
		edit(fmt.Sprintf("Downloading %s… %.1f%%", code(f.Name), pct(cur, total)))
	})
	if err != nil {
		_ = a.tg.Edit(ctx, ref, "Download failed: "+code(err.Error()), tg.SendOpts{})
		return
	}
	up := func(cur, total int64) { edit(fmt.Sprintf("Uploading to %s… %.1f%%", host, pct(cur, total))) }
	uid := userID(m)
	var link string
	switch host {
	case "pixeldrain":
		link, err = a.hosts.Pixeldrain(ctx, path, uid, up)
	case "gofile":
		link, err = a.hosts.Gofile(ctx, path, uid, up)
	default:
		link, err = a.hosts.Fileditch(ctx, path, false, up)
	}
	if err != nil {
		_ = a.tg.Edit(ctx, ref, "Upload failed: "+code(status.Short(err.Error(), 300)), tg.SendOpts{})
		return
	}
	_ = a.tg.Edit(ctx, ref, fmt.Sprintf("<b>Uploaded to %s</b>\n%s\n%s", host, code(f.Name), esc(link)), tg.SendOpts{})
}

func pct(cur, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(cur) / float64(total) * 100
}

// ---- gallery-dl config & cookies ---------------------------------------------

func (a *App) cmdGDLConf(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	uid := userID(m)
	if uid <= 0 {
		a.reply(ctx, m, "Could not determine your user ID.")
		return
	}
	dir, err := a.cfg.UserDir(uid)
	if err != nil {
		a.reply(ctx, m, "Storage error: "+code(err.Error()))
		return
	}
	confPath, cookiePath := filepath.Join(dir, "gallery-dl.conf"), filepath.Join(dir, "cookies.txt")
	if t := tokens(m); len(t) > 0 && (strings.EqualFold(t[0], "delete") || strings.EqualFold(t[0], "clear")) {
		_ = os.Remove(confPath)
		_ = os.Remove(cookiePath)
		a.reply(ctx, m, "Deleted your gallery-dl configuration and cookies.")
		return
	}
	if _, f := mediaSource(m); f != nil {
		tmp, err := os.MkdirTemp(a.cfg.DownloadsDir(), "tmp_conf_")
		if err != nil {
			a.reply(ctx, m, "Storage error: "+code(err.Error()))
			return
		}
		defer os.RemoveAll(tmp)
		p, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, tmp, nil)
		if err != nil {
			a.reply(ctx, m, "Download failed: "+code(err.Error()))
			return
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 2<<20 {
			a.reply(ctx, m, "Could not read the file (max 2MB).")
			return
		}
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "cookie") || strings.HasSuffix(name, ".txt") {
			if err := os.WriteFile(cookiePath, data, 0o600); err != nil {
				a.reply(ctx, m, "Save failed: "+code(err.Error()))
				return
			}
			a.reply(ctx, m, "Saved your <code>cookies.txt</code>. It is used by gallery-dl, cyberdrop-dl and the forum scraper.")
			return
		}
		var probe map[string]any
		if err := json.Unmarshal(data, &probe); err != nil {
			a.reply(ctx, m, "That is not valid JSON: "+code(err.Error()))
			return
		}
		if err := os.WriteFile(confPath, data, 0o600); err != nil {
			a.reply(ctx, m, "Save failed: "+code(err.Error()))
			return
		}
		a.reply(ctx, m, "Saved your gallery-dl configuration.")
		return
	}
	has := func(p string) string {
		if _, err := os.Stat(p); err == nil {
			return "set"
		}
		return "not set"
	}
	a.reply(ctx, m, fmt.Sprintf("<b>gallery-dl settings</b>\n• Config: %s\n• Cookies: %s\n\nReply to a <code>gallery-dl.conf</code> (JSON) or <code>cookies.txt</code> file with <code>/gdlconf</code> to save it, or use <code>/gdlconf delete</code>.",
		code(has(confPath)), code(has(cookiePath))))
}

// ---- APK patching ---------------------------------------------------------------

func (a *App) cmdKeystore(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	uid := userID(m)
	if uid <= 0 {
		a.reply(ctx, m, "Could not determine your user ID.")
		return
	}
	dir, err := a.cfg.UserDir(uid)
	if err != nil {
		a.reply(ctx, m, "Storage error: "+code(err.Error()))
		return
	}
	args := tokens(m)
	cfgFile := filepath.Join(dir, "keystore_config.json")
	if len(args) > 0 && (strings.EqualFold(args[0], "delete") || strings.EqualFold(args[0], "remove") || strings.EqualFold(args[0], "clear")) {
		for _, pat := range []string{"*.jks", "*.keystore"} {
			old, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, f := range old {
				_ = os.Remove(f)
			}
		}
		_ = os.Remove(cfgFile)
		a.reply(ctx, m, "Your JKS keystore and credentials have been deleted.")
		return
	}
	usage := "<code>/setkeystore &lt;store_password&gt; &lt;key_alias&gt; [key_password]</code>"
	if _, f := mediaSource(m); f != nil {
		lower := strings.ToLower(f.Name)
		if !strings.HasSuffix(lower, ".jks") && !strings.HasSuffix(lower, ".keystore") {
			a.reply(ctx, m, "Please provide a valid <code>.jks</code> or <code>.keystore</code> file.")
			return
		}
		if len(args) < 2 {
			a.reply(ctx, m, "<b>Missing passwords/alias.</b> Usage when replying to or attaching the file:\n"+usage)
			return
		}
		storePass, alias := args[0], args[1]
		keyPass := storePass
		if len(args) > 2 {
			keyPass = args[2]
		}
		for _, pat := range []string{"*.jks", "*.keystore"} {
			old, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, x := range old {
				_ = os.Remove(x)
			}
		}
		saved, err := a.api.Download(ctx, tg.FileRef{FileID: f.FileID, Name: "keystore.jks", Size: f.Size}, dir, nil)
		if err != nil {
			a.reply(ctx, m, "Failed to save keystore: "+code(err.Error()))
			return
		}
		_ = os.Chmod(saved, 0o600)
		b, _ := json.MarshalIndent(map[string]string{"store_pass": storePass, "key_alias": alias, "key_pass": keyPass}, "", "  ")
		if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
			a.reply(ctx, m, "Failed to save credentials: "+code(err.Error()))
			return
		}
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID}) // remove the message containing passwords
		a.tg.Send(ctx, m.Chat.ID, fmt.Sprintf("<b>JKS keystore saved.</b>\n• Alias: %s\n\nYou can now use /patch.", code(alias)), tg.SendOpts{})
		return
	}
	if ks := a.cfg.UserKeystore(uid); ks != nil {
		a.reply(ctx, m, fmt.Sprintf("<b>JKS keystore</b>: set (%s)\n• Alias: %s\n\nTo update, reply to a new <code>.jks</code> with:\n%s\nTo remove: <code>/setkeystore delete</code>",
			code(filepath.Base(ks.Path)), code(ks.KeyAlias), usage))
		return
	}
	a.reply(ctx, m, "<b>JKS keystore setup</b>\n\nUpload your <code>.jks</code> file and reply to it with:\n"+usage)
}

func (a *App) cmdPatch(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	uid := userID(m)
	if uid <= 0 {
		a.reply(ctx, m, "Could not determine your user ID; patching needs an identified user.")
		return
	}
	if a.cfg.UserKeystore(uid) == nil {
		a.reply(ctx, m, "<b>No JKS keystore found.</b> Set one first:\n<code>/setkeystore &lt;store_password&gt; &lt;key_alias&gt; [key_password]</code> (reply to your <code>.jks</code>).")
		return
	}
	args := dl.Args{OriginalFilename: "app.apk"}
	target := "patch:"
	if _, f := mediaSource(m); f != nil {
		args.TGFile = f
		if f.Name != "" {
			args.OriginalFilename = f.Name
		}
		target += "tg:" + f.Name
	} else if t := tokens(m); len(t) > 0 && strings.HasPrefix(t[0], "http") {
		raw := strings.Replace(t[0], "pixeldrain.com/u/", "pixeldrain.com/api/file/", 1)
		args.TargetURL = raw
		if base := filepath.Base(strings.SplitN(t[0], "?", 2)[0]); strings.HasSuffix(strings.ToLower(base), ".apk") {
			args.OriginalFilename = base
		}
		target += raw
	} else {
		a.reply(ctx, m, "<b>Invalid usage.</b>\n1. Reply to an APK with <code>/patch</code>\n2. Send an APK with the caption <code>/patch</code>\n3. <code>/patch &lt;URL&gt;</code>")
		return
	}
	a.enqueue(ctx, m, target, args)
}

// ---- torrent search -----------------------------------------------------------------

func (a *App) cmdTorrentSearch(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	m := u.Message
	toks := tokens(m)
	var providers []string
	var q []string
	for i := 0; i < len(toks); i++ {
		if (toks[i] == "-p" || toks[i] == "--provider") && i+1 < len(toks) {
			i++
			providers = append(providers, strings.Split(toks[i], ",")...)
			continue
		}
		q = append(q, toks[i])
	}
	query := strings.TrimSpace(strings.Join(q, " "))
	if query == "" {
		var names []string
		for id := range a.search.Providers() {
			names = append(names, id)
		}
		hint := ""
		if len(names) > 0 {
			hint = "\nProviders: " + code(strings.Join(names, ", "))
		}
		a.reply(ctx, m, "Usage: <code>/ts [-p provider] &lt;query&gt;</code>"+hint)
		return
	}
	if len(providers) == 1 && providers[0] == "all" {
		providers = nil
	}
	ref, _ := a.tg.Send(ctx, m.Chat.ID, "Searching "+code(query)+"…", tg.SendOpts{ReplyTo: m.ID})
	sctx, cancel := context.WithTimeout(ctx, a.cfg.TorrentTimeout+5*time.Second)
	defer cancel()
	res, err := a.search.Search(sctx, query, providers)
	if err != nil {
		slog.Warn("torrent search failed", "err", err)
		_ = a.tg.Edit(ctx, ref, "Search failed: "+code(status.Short(err.Error(), 300)), tg.SendOpts{})
		return
	}
	site := "All Providers"
	if len(providers) > 0 {
		site = strings.Join(providers, ", ")
	}

	if len(res) == 0 {
		_ = a.tg.Edit(ctx, ref, "<b>No torrent results found</b> for <i>"+esc(query)+"</i>.", tg.SendOpts{})
		return
	}

	pageURL, terr := a.telegraph.PublishTorrentResults(sctx, res, query, site)
	if terr == nil && pageURL != "" {
		kb := tg.Keyboard{{{Text: "VIEW", URL: pageURL}}}
		text := fmt.Sprintf("<b>Found %d result(s) for</b> <i>%s</i>\n<b>Source:</b> <i>%s</i>", len(res), esc(query), esc(site))
		_ = a.tg.Edit(ctx, ref, text, tg.SendOpts{Keyboard: kb})
		return
	}

	text := torrent.FormatHTML(res, query, site)
	if len(text) > 4000 {
		text = text[:4000]
		if i := strings.LastIndex(text, "\n\n"); i > 0 {
			text = text[:i]
		}
	}
	_ = a.tg.Edit(ctx, ref, text, tg.SendOpts{})
}
