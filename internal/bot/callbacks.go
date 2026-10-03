package bot

import (
	"context"
	"fmt"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/jobs"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/tg"
)

func (a *App) cbCancel(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}
	id := strings.TrimPrefix(q.Data, "cancel_job:")
	j, err := a.store.GetJob(ctx, id)
	if err != nil {
		a.ack(ctx, q, "Job not found.", true)
		return
	}
	if !a.isJobOwner(chatID, q.From.ID, j) {
		a.ack(ctx, q, "You are not authorized to cancel this job.", true)
		return
	}
	if j.Status.IsTerminal() {
		a.ack(ctx, q, fmt.Sprintf("Job #%s is already %s.", j.ID, j.Status), true)
		return
	}
	a.ack(ctx, q, fmt.Sprintf("Cancelling job #%s…", j.ID), false)
	a.cancelJob(ctx, j)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID},
		fmt.Sprintf("<b>Job #%s Cancelled</b>\n<blockquote>Cancelled by user.</blockquote>", esc(j.ID)), tg.SendOpts{})
}

// data format: archive_only:<job>:<id> | archive_ext:<job>:<id>
func (a *App) cbArchive(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}
	parts := strings.SplitN(q.Data, ":", 3)
	if len(parts) != 3 {
		return
	}
	kind, jobID, archiveID := parts[0], parts[1], parts[2]
	j, err := a.store.GetJob(ctx, jobID)
	if err != nil || !a.isJobOwner(chatID, q.From.ID, j) {
		a.ack(ctx, q, "Unauthorized.", true)
		return
	}
	choice, label := jobs.ChoiceArchiveOnly, "Archive Only"
	if kind == "archive_ext" {
		choice, label = jobs.ChoiceExtract, "Extract & Upload Both"
	}
	name, ok := a.mgr.SetArchiveChoice(jobID, archiveID, choice)
	if !ok {
		a.ack(ctx, q, "This prompt has expired.", true)
		return
	}
	a.ack(ctx, q, "Selected: "+label, false)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, status.ArchiveChoice(jobID, name, label), tg.SendOpts{})
}

// data format: convert_mp3:<job>:<id> | convert_orig:<job>:<id>
func (a *App) cbConvert(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}
	parts := strings.SplitN(q.Data, ":", 3)
	if len(parts) != 3 {
		return
	}
	kind, jobID, convID := parts[0], parts[1], parts[2]
	j, err := a.store.GetJob(ctx, jobID)
	if err != nil || !a.isJobOwner(chatID, q.From.ID, j) {
		a.ack(ctx, q, "Unauthorized.", true)
		return
	}
	choice, label := jobs.ChoiceOriginal, "Upload Original"
	if kind == "convert_mp3" {
		choice, label = jobs.ChoiceMP3, "Convert to MP3"
	}
	name, ok := a.mgr.SetAudioChoice(jobID, convID, choice)
	if !ok {
		a.ack(ctx, q, "This prompt has expired.", true)
		return
	}
	a.ack(ctx, q, "Selected: "+label, false)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, status.ConversionChoice(jobID, name, label), tg.SendOpts{})
}

func (a *App) cbHelp(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}
	page := strings.TrimPrefix(q.Data, "help_page:")
	if page == "close" {
		a.ack(ctx, q, "Closed.", false)
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: chatID, ID: msgID})
		return
	}
	a.ack(ctx, q, "", false)
	text, kb := helpPage(page)
	_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})
}
