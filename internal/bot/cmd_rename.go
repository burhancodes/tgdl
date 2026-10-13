package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/burhanverse/tgdl/internal/archive"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/rename"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/tg"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (a *App) cmdRename(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if !a.auth.Authorized(userID(m)) {
		return
	}

	toks := tokens(m)
	if len(toks) == 0 {
		a.reply(ctx, m, renameUsage())
		return
	}

	sub := strings.ToLower(toks[0])
	switch sub {
	case "batch":
		a.handleRenameBatch(ctx, m, toks[1:])
	case "scheme":
		a.handleRenameScheme(ctx, m, toks[1:])
	case "preset":
		a.handleRenamePreset(ctx, m, toks[1:])
	default:
		a.handleRenameSingle(ctx, m, toks)
	}
}

func (a *App) handleRenameBatch(ctx context.Context, m *models.Message, toks []string) {
	_, file := mediaSource(m)
	if file != nil && archive.IsArchiveExt(file.Name) {
		scheme, opts, isDoc, err := parseRenameOpts(toks)
		if err != nil {
			a.reply(ctx, m, err.Error())
			return
		}
		if scheme == "" {
			scheme = "{name}.{ext}"
		}
		schemeStr := scheme
		if p, ok := rename.LookupPreset(schemeStr); ok {
			schemeStr = p
		}
		if _, err := rename.Parse(schemeStr); err != nil {
			a.reply(ctx, m, fmt.Sprintf("Invalid scheme: %s", status.Esc(err.Error())))
			return
		}
		args := dl.Args{
			Unzip:         true,
			RenameScheme:  scheme,
			RenameOptions: &opts,
			TGFile:        file,
			AsDoc:         isDoc,
		}
		a.enqueue(ctx, m, "unzip:"+file.Name, args)
		return
	}

	scheme, opts, isDoc, err := parseRenameOpts(toks)
	if err != nil {
		a.reply(ctx, m, err.Error())
		return
	}
	if scheme == "" {
		a.reply(ctx, m, "Usage: <code>/rename batch &lt;scheme&gt; [options]</code>\nExample: <code>/rename batch {n:3} - {name}.{ext}</code>")
		return
	}
	schemeStr := scheme
	if p, ok := rename.LookupPreset(schemeStr); ok {
		schemeStr = p
	}
	if _, err := rename.Parse(schemeStr); err != nil {
		a.reply(ctx, m, fmt.Sprintf("Invalid scheme: %s", status.Esc(err.Error())))
		return
	}

	cardText := formatBatchCard(scheme, 0, a.cfg.RenameMaxBatch)
	kb := batchKeyboard()
	ref, err := a.tg.Send(ctx, m.Chat.ID, cardText, tg.SendOpts{Keyboard: kb})
	if err != nil {
		a.reply(ctx, m, "Failed to create batch session: "+status.Code(err.Error()))
		return
	}
	a.renameSessions.Start(m.Chat.ID, userID(m), scheme, opts, isDoc, ref.ID)
}

func (a *App) handleRenameScheme(ctx context.Context, m *models.Message, toks []string) {
	newScheme := strings.TrimSpace(strings.Join(toks, " "))
	if newScheme == "" {
		a.reply(ctx, m, "Usage: <code>/rename scheme &lt;new scheme&gt;</code>")
		return
	}
	sess, ok := a.renameSessions.Get(m.Chat.ID, userID(m))
	if !ok {
		a.reply(ctx, m, "No active batch session. Start one with <code>/rename batch &lt;scheme&gt;</code>.")
		return
	}
	schemeStr := newScheme
	if p, ok := rename.LookupPreset(schemeStr); ok {
		schemeStr = p
	}
	if _, err := rename.Parse(schemeStr); err != nil {
		a.reply(ctx, m, fmt.Sprintf("Invalid scheme: %s", status.Esc(err.Error())))
		return
	}
	_ = sess
	a.renameSessions.SetScheme(m.Chat.ID, userID(m), newScheme)
	a.reply(ctx, m, fmt.Sprintf("Batch scheme updated to: <code>%s</code>", status.Esc(newScheme)))
}

func (a *App) handleRenamePreset(ctx context.Context, m *models.Message, toks []string) {
	if len(toks) == 0 {
		a.reply(ctx, m, "Usage: <code>/rename preset &lt;name&gt;</code>\nAvailable presets: <code>sequence</code>, <code>clean</code>, <code>date-prefix</code>, <code>tv</code>")
		return
	}
	presetName := strings.ToLower(toks[0])
	presetVal, ok := rename.LookupPreset(presetName)
	if !ok {
		a.reply(ctx, m, fmt.Sprintf("Unknown preset %s. Available presets: <code>sequence</code>, <code>clean</code>, <code>date-prefix</code>, <code>tv</code>", status.Code(presetName)))
		return
	}
	_, hasSess := a.renameSessions.Get(m.Chat.ID, userID(m))
	if !hasSess {
		a.reply(ctx, m, "No active batch session. Start one with <code>/rename batch &lt;scheme&gt;</code>.")
		return
	}
	a.renameSessions.SetScheme(m.Chat.ID, userID(m), presetVal)
	a.reply(ctx, m, fmt.Sprintf("Batch scheme set to preset <i>%s</i>: <code>%s</code>", status.Esc(presetName), status.Esc(presetVal)))
}

func (a *App) handleRenameSingle(ctx context.Context, m *models.Message, toks []string) {
	_, file := mediaSource(m)
	if file == nil {
		a.reply(ctx, m, renameUsage())
		return
	}

	scheme, opts, isDoc, err := parseRenameOpts(toks)
	if err != nil {
		a.reply(ctx, m, err.Error())
		return
	}
	if scheme == "" {
		scheme = "{name}.{ext}"
	}
	schemeStr := scheme
	if p, ok := rename.LookupPreset(schemeStr); ok {
		schemeStr = p
	}
	if _, err := rename.Parse(schemeStr); err != nil {
		a.reply(ctx, m, fmt.Sprintf("Invalid scheme: %s", status.Esc(err.Error())))
		return
	}

	target := "rename:" + file.Name
	args := dl.Args{
		RenameScheme:  scheme,
		RenameOptions: &opts,
		RenameFiles:   []dl.TGFile{*file},
		AsDoc:         isDoc,
	}
	a.enqueue(ctx, m, target, args)
}

func (a *App) cbRename(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	if q == nil || q.Message.Message == nil {
		return
	}
	msg := q.Message.Message
	chatID := msg.Chat.ID
	fromID := q.From.ID

	if !a.auth.Authorized(fromID) {
		_ = a.tg.Answer(ctx, q.ID, "Unauthorized.", true)
		return
	}

	sess, ok := a.renameSessions.Get(chatID, fromID)
	if !ok {
		_ = a.tg.Answer(ctx, q.ID, "This batch rename session belongs to another user or has expired.", true)
		return
	}

	action := strings.TrimPrefix(q.Data, "rename:")
	switch action {
	case "preview":
		a.cbRenamePreview(ctx, q, sess)
	case "apply":
		a.cbRenameApply(ctx, q, msg, sess)
	case "clear":
		a.renameSessions.Clear(chatID, fromID)
		_ = a.tg.Answer(ctx, q.ID, "Batch cleared.", false)
	case "cancel":
		a.renameSessions.Remove(chatID, fromID)
		_ = a.tg.Answer(ctx, q.ID, "Batch cancelled.", false)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msg.ID}, "<b>Batch Rename Cancelled</b>", tg.SendOpts{})
	}
}

func (a *App) cbRenamePreview(ctx context.Context, q *models.CallbackQuery, sess *RenameSession) {
	if len(sess.Files) == 0 {
		_ = a.tg.Answer(ctx, q.ID, "No files in batch yet. Send media to add.", true)
		return
	}

	schemeStr := sess.Scheme
	if p, ok := rename.LookupPreset(schemeStr); ok {
		schemeStr = p
	}
	parsed, err := rename.Parse(schemeStr)
	if err != nil {
		_ = a.tg.Answer(ctx, q.ID, "Invalid scheme: "+err.Error(), true)
		return
	}

	var inputs []rename.Input
	for _, f := range sess.Files {
		inputs = append(inputs, rename.Input{OriginalName: f.Name, Size: f.Size})
	}
	plan, err := parsed.Plan(inputs, sess.Opts)
	if err != nil {
		_ = a.tg.Answer(ctx, q.ID, "Plan failed: "+err.Error(), true)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>Rename Preview</b> (%d files)\n\n", len(plan.Entries))
	limit := 30
	if len(plan.Entries) < limit {
		limit = len(plan.Entries)
	}
	for i := 0; i < limit; i++ {
		entry := plan.Entries[i]
		fmt.Fprintf(&b, "<code>%s</code> → <code>%s</code>\n", status.Esc(entry.OldName), status.Esc(entry.NewName))
	}
	if len(plan.Entries) > 30 {
		fmt.Fprintf(&b, "…and %d more\n", len(plan.Entries)-30)
	}
	if len(plan.Warnings) > 0 {
		b.WriteString("\n<b>Warnings:</b>\n")
		for _, w := range plan.Warnings {
			fmt.Fprintf(&b, "• %s\n", status.Esc(w))
		}
	}

	_ = a.tg.Answer(ctx, q.ID, "", false)
	_, _ = a.tg.Send(ctx, sess.ChatID, b.String(), tg.SendOpts{})
}

func (a *App) cbRenameApply(ctx context.Context, q *models.CallbackQuery, msg *models.Message, sess *RenameSession) {
	if len(sess.Files) == 0 {
		_ = a.tg.Answer(ctx, q.ID, "No files in batch to rename.", true)
		return
	}

	count := len(sess.Files)
	args := dl.Args{
		RenameScheme:  sess.Scheme,
		RenameOptions: &sess.Opts,
		RenameFiles:   sess.Files,
		AsDoc:         sess.AsDoc,
	}

	a.renameSessions.Remove(sess.ChatID, sess.UserID)
	_ = a.tg.Answer(ctx, q.ID, "Applying rename...", false)

	confirmText := fmt.Sprintf("<b>Batch Rename Queued</b>\nApplying rename for %d files.\n<i>Note: Telegram does not allow renaming files in place, so files are downloaded, renamed, and re-sent.</i>", count)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: sess.ChatID, ID: msg.ID}, confirmText, tg.SendOpts{})

	a.enqueue(ctx, msg, "rename:batch", args)
}

func parseRenameOpts(toks []string) (scheme string, opts rename.Options, isDoc bool, err error) {
	opts = rename.Options{
		Start:      1,
		Step:       1,
		OnConflict: "suffix",
	}
	var schemeParts []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t {
		case "--start":
			if i+1 < len(toks) {
				i++
				if n, e := strconv.Atoi(toks[i]); e == nil {
					opts.Start = n
				}
			}
		case "--step":
			if i+1 < len(toks) {
				i++
				if n, e := strconv.Atoi(toks[i]); e == nil {
					opts.Step = n
				}
			}
		case "--sort":
			if i+1 < len(toks) {
				i++
				s := strings.ToLower(toks[i])
				switch s {
				case "name", "time", "mtime", "size", "sent":
					opts.SortBy = s
				}
			}
		case "--on-conflict":
			if i+1 < len(toks) {
				i++
				c := strings.ToLower(toks[i])
				switch c {
				case "suffix", "skip", "error":
					opts.OnConflict = c
				}
			}
		case "--doc":
			isDoc = true
		default:
			schemeParts = append(schemeParts, t)
		}
	}
	scheme = strings.Join(schemeParts, " ")
	return scheme, opts, isDoc, nil
}

func renameUsage() string {
	return `<b>Rename Usage:</b>
• Single file: reply to a file or send with caption:
  <code>/rename &lt;scheme&gt; [options]</code>
• Batch mode:
  <code>/rename batch &lt;scheme&gt; [options]</code>
• Options:
  <code>--start N</code> (start counter, default 1)
  <code>--step N</code> (step counter, default 1)
  <code>--sort name|time|size|sent</code>
  <code>--on-conflict suffix|skip|error</code>
  <code>--doc</code> (force upload as document)
• Scheme commands:
  <code>/rename scheme &lt;new scheme&gt;</code>
  <code>/rename preset sequence|clean|date-prefix|tv</code>`
}
