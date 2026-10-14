package bot

import (
	"context"
	"fmt"
	"strings"

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
	if len(toks) > 0 && strings.EqualFold(toks[0], "batch") {
		a.handleRenameBatch(ctx, m, toks[1:])
		return
	}

	a.handleRenameSingle(ctx, m, toks)
}

func (a *App) handleRenameBatch(ctx context.Context, m *models.Message, toks []string) {
	isDoc := parseBatchRenameToks(toks)
	cardText := formatBatchCard(0, a.cfg.RenameMaxBatch)
	kb := batchKeyboard()
	ref, err := a.tg.Send(ctx, m.Chat.ID, cardText, tg.SendOpts{Keyboard: kb})
	if err != nil {
		a.reply(ctx, m, "Failed to create batch session: "+status.Code(err.Error()))
		return
	}
	a.renameSessions.Start(m.Chat.ID, userID(m), isDoc, ref.ID)
}

func (a *App) handleRenameSingle(ctx context.Context, m *models.Message, toks []string) {
	_, file := mediaSource(m)
	if file == nil {
		a.reply(ctx, m, renameUsage())
		return
	}

	typedName, isDoc := parseSingleRenameToks(toks)
	if typedName == "" {
		a.renameSessions.SetPendingSingle(m.Chat.ID, userID(m), *file, isDoc)
		a.reply(ctx, m, "Send the new name")
		return
	}

	finalName, _, err := rename.Name(typedName, file.Name)
	if err != nil {
		a.reply(ctx, m, err.Error())
		return
	}

	target := "rename:" + file.Name
	args := dl.Args{
		RenameNames: []string{finalName.Display},
		RenameFiles: []dl.TGFile{*file},
		AsDoc:       isDoc,
	}
	a.enqueue(ctx, m, target, args)
}

func (a *App) handlePendingSingleName(ctx context.Context, m *models.Message, pending *PendingSingleRename) {
	typed := m.Text
	finalName, _, err := rename.Name(typed, pending.File.Name)
	if err != nil {
		a.reply(ctx, m, err.Error())
		return
	}
	target := "rename:" + pending.File.Name
	args := dl.Args{
		RenameNames: []string{finalName.Display},
		RenameFiles: []dl.TGFile{pending.File},
		AsDoc:       pending.AsDoc,
	}
	a.enqueue(ctx, m, target, args)
}

func (a *App) handleBatchNamesMessage(ctx context.Context, m *models.Message, sess *RenameSession) {
	raw := strings.ReplaceAll(m.Text, "\r\n", "\n")
	raw = strings.Trim(raw, "\r\n")
	if strings.TrimSpace(raw) == "" {
		a.reply(ctx, m, "No names provided.")
		return
	}
	lines := strings.Split(raw, "\n")
	a.renameSessions.SetNames(sess.ChatID, sess.UserID, lines)

	fileCount := len(sess.Files)
	nameCount := len(lines)
	if fileCount == 0 {
		a.reply(ctx, m, "No files in batch yet. Send media files first.")
		return
	}
	if nameCount != fileCount {
		a.reply(ctx, m, fmt.Sprintf("Line count mismatch: received %d names for %d files.\nSend the names message again or send more files.", nameCount, fileCount))
		return
	}

	originals := make([]string, len(sess.Files))
	for i, f := range sess.Files {
		originals[i] = f.Name
	}
	plan, err := rename.PlanBatch(lines, originals)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Error creating rename plan: %s", status.Esc(err.Error())))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>Rename Preview</b> (%d files)\n\n", len(plan))
	for _, entry := range plan {
		fmt.Fprintf(&b, "<code>%s</code> → <code>%s</code>", status.Esc(entry.OldName), status.Esc(entry.NewName.Display))
		if len(entry.Notes) > 0 {
			fmt.Fprintf(&b, " <i>(%s)</i>", status.Esc(strings.Join(entry.Notes, ", ")))
		}
		b.WriteByte('\n')
	}

	kb := tg.Keyboard{
		{
			tg.Button{Text: "Apply", Data: "rename:apply"},
			tg.Button{Text: "Cancel", Data: "rename:cancel"},
		},
	}
	chunks := status.Fit(b.String(), 4096)
	for i, chunk := range chunks {
		if i == len(chunks)-1 {
			a.replyKB(ctx, m, chunk, kb)
		} else {
			a.reply(ctx, m, chunk)
		}
	}
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
	if len(sess.Names) == 0 {
		_ = a.tg.Answer(ctx, q.ID, "No names received yet. Send a message with one name per line.", true)
		return
	}
	if len(sess.Names) != len(sess.Files) {
		_ = a.tg.Answer(ctx, q.ID, fmt.Sprintf("Line count mismatch: %d names for %d files.", len(sess.Names), len(sess.Files)), true)
		return
	}

	originals := make([]string, len(sess.Files))
	for i, f := range sess.Files {
		originals[i] = f.Name
	}
	plan, err := rename.PlanBatch(sess.Names, originals)
	if err != nil {
		_ = a.tg.Answer(ctx, q.ID, "Plan failed: "+err.Error(), true)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>Rename Preview</b> (%d files)\n\n", len(plan))
	for _, entry := range plan {
		fmt.Fprintf(&b, "<code>%s</code> → <code>%s</code>", status.Esc(entry.OldName), status.Esc(entry.NewName.Display))
		if len(entry.Notes) > 0 {
			fmt.Fprintf(&b, " <i>(%s)</i>", status.Esc(strings.Join(entry.Notes, ", ")))
		}
		b.WriteByte('\n')
	}

	_ = a.tg.Answer(ctx, q.ID, "", false)
	for _, chunk := range status.Fit(b.String(), 4096) {
		_, _ = a.tg.Send(ctx, sess.ChatID, chunk, tg.SendOpts{})
	}
}

func (a *App) cbRenameApply(ctx context.Context, q *models.CallbackQuery, msg *models.Message, sess *RenameSession) {
	if len(sess.Files) == 0 {
		_ = a.tg.Answer(ctx, q.ID, "No files in batch to rename.", true)
		return
	}
	if len(sess.Names) != len(sess.Files) {
		_ = a.tg.Answer(ctx, q.ID, fmt.Sprintf("Line count mismatch: %d names for %d files.", len(sess.Names), len(sess.Files)), true)
		return
	}

	originals := make([]string, len(sess.Files))
	for i, f := range sess.Files {
		originals[i] = f.Name
	}
	plan, err := rename.PlanBatch(sess.Names, originals)
	if err != nil {
		_ = a.tg.Answer(ctx, q.ID, "Plan failed: "+err.Error(), true)
		return
	}

	finalNames := make([]string, len(plan))
	for i, p := range plan {
		finalNames[i] = p.NewName.Display
	}

	count := len(sess.Files)
	args := dl.Args{
		RenameNames: finalNames,
		RenameFiles: sess.Files,
		AsDoc:       sess.AsDoc,
	}

	a.renameSessions.Remove(sess.ChatID, sess.UserID)
	_ = a.tg.Answer(ctx, q.ID, "Applying rename...", false)

	confirmText := fmt.Sprintf("<b>Batch Rename Queued</b>\nApplying rename for %d files.\n<i>Note: Telegram does not allow renaming files in place, so files are downloaded, renamed, and re-sent.</i>", count)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: sess.ChatID, ID: msg.ID}, confirmText, tg.SendOpts{})

	a.enqueue(ctx, msg, "rename:batch", args)
}

func parseSingleRenameToks(toks []string) (typedName string, isDoc bool) {
	var nameParts []string
	for _, t := range toks {
		if t == "--doc" {
			isDoc = true
		} else {
			nameParts = append(nameParts, t)
		}
	}
	return strings.TrimSpace(strings.Join(nameParts, " ")), isDoc
}

func parseBatchRenameToks(toks []string) bool {
	for _, t := range toks {
		if t == "--doc" {
			return true
		}
	}
	return false
}

func renameUsage() string {
	return `<b>Rename Usage:</b>
• Reply to a file with <code>/rename &lt;new name&gt;</code>. The extension is kept automatically.
• Several files: <code>/rename batch</code>, send the files, then send one name per line.`
}
