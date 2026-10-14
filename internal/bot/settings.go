package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/settings"
	"github.com/burhanverse/tgdl/internal/tg"
)

type settingsInputState struct {
	Key       string
	ExpiresAt time.Time
}

type pendingChange struct {
	Token     string
	Key       string
	Value     string
	ExpiresAt time.Time
}

type SettingsUI struct {
	mu             sync.Mutex
	mgr            *settings.Manager
	inputState     map[int64]*settingsInputState
	pendingChanges map[string]*pendingChange
	pendingImports map[string]func(ctx context.Context, userID int64) error
}

func NewSettingsUI(mgr *settings.Manager) *SettingsUI {
	return &SettingsUI{
		mgr:            mgr,
		inputState:     make(map[int64]*settingsInputState),
		pendingChanges: make(map[string]*pendingChange),
		pendingImports: make(map[string]func(ctx context.Context, userID int64) error),
	}
}

func (ui *SettingsUI) setInputState(userID int64, key string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.inputState[userID] = &settingsInputState{
		Key:       key,
		ExpiresAt: time.Now().Add(60 * time.Second),
	}
}

func (ui *SettingsUI) getInputState(userID int64) (string, bool) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	st, ok := ui.inputState[userID]
	if !ok {
		return "", false
	}
	if time.Now().After(st.ExpiresAt) {
		delete(ui.inputState, userID)
		return "", false
	}
	return st.Key, true
}

func (ui *SettingsUI) clearInputState(userID int64) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	delete(ui.inputState, userID)
}

func (ui *SettingsUI) addPendingChange(key, val string) string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	ui.pendingChanges[tok] = &pendingChange{
		Token:     tok,
		Key:       key,
		Value:     val,
		ExpiresAt: time.Now().Add(60 * time.Second),
	}
	return tok
}

func (ui *SettingsUI) getPendingChange(token string) (*pendingChange, bool) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	pc, ok := ui.pendingChanges[token]
	if !ok {
		return nil, false
	}
	delete(ui.pendingChanges, token)
	if time.Now().After(pc.ExpiresAt) {
		return nil, false
	}
	return pc, true
}

func (ui *SettingsUI) addPendingImport(fn func(ctx context.Context, userID int64) error) string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	ui.pendingImports[tok] = fn
	return tok
}

func (ui *SettingsUI) getPendingImport(token string) (func(ctx context.Context, userID int64) error, bool) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	fn, ok := ui.pendingImports[token]
	if !ok {
		return nil, false
	}
	delete(ui.pendingImports, token)
	return fn, true
}

func (a *App) checkOwnerPrivate(fromID int64, chat *models.Chat) (bool, string) {
	if a.auth == nil || !a.auth.HasOwner() {
		return false, "Settings commands are disabled because no owner is configured on this bot."
	}
	if !a.auth.IsOwner(fromID) {
		return false, "You are not authorized to access bot settings."
	}
	if chat == nil || chat.Type != "private" {
		return false, "Settings can only be accessed in a private chat with the bot."
	}
	return true, ""
}

func (a *App) cmdSettings(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if ok, errMsg := a.checkOwnerPrivate(m.From.ID, &m.Chat); !ok {
		a.reply(ctx, m, errMsg)
		return
	}
	if a.settingsUI == nil || a.settingsMgr == nil {
		a.reply(ctx, m, "Settings module is unavailable.")
		return
	}

	args := strings.Fields(m.Text)
	if len(args) > 1 {
		sub := strings.ToLower(args[1])
		switch sub {
		case "export":
			a.handleSettingsExport(ctx, m)
			return
		case "import":
			a.handleSettingsImport(ctx, m)
			return
		case "changed":
			text, kb := a.renderChangedCard()
			_, _ = a.tg.Send(ctx, m.Chat.ID, text, tg.SendOpts{Keyboard: kb})
			return
		case "history":
			text, kb := a.renderHistoryCard()
			_, _ = a.tg.Send(ctx, m.Chat.ID, text, tg.SendOpts{Keyboard: kb})
			return
		case "undo":
			a.handleSettingsUndo(ctx, m)
			return
		case "search":
			if len(args) > 2 {
				query := strings.Join(args[2:], " ")
				text, kb := a.renderSearchResults(query)
				_, _ = a.tg.Send(ctx, m.Chat.ID, text, tg.SendOpts{Keyboard: kb})
				return
			}
			a.reply(ctx, m, "Usage: <code>/settings search &lt;query&gt;</code>")
			return
		}
	}

	text, kb := a.renderGroupsMenu()
	_, _ = a.tg.Send(ctx, m.Chat.ID, text, tg.SendOpts{Keyboard: kb})
}

func (a *App) cmdSet(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if ok, errMsg := a.checkOwnerPrivate(m.From.ID, &m.Chat); !ok {
		a.reply(ctx, m, errMsg)
		return
	}
	parts := strings.Fields(m.Text)
	if len(parts) < 3 {
		a.reply(ctx, m, "Usage: <code>/set &lt;KEY&gt; &lt;value&gt;</code>")
		return
	}
	key := settings.CleanKey(parts[1])
	val := strings.Join(parts[2:], " ")

	spec, ok := a.settingsMgr.Registry().Get(key)
	if !ok {
		a.reply(ctx, m, fmt.Sprintf("Unknown setting key <code>%s</code>.", html.EscapeString(key)))
		return
	}
	if spec.Apply == settings.ApplyReadOnly {
		a.reply(ctx, m, fmt.Sprintf("Setting <code>%s</code> is read-only.", html.EscapeString(key)))
		return
	}

	if spec.Secret {
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
	}

	if settings.IsDangerous(spec, val) {
		oldVal, _, _, _ := a.settingsMgr.Get(key)
		token := a.settingsUI.addPendingChange(key, val)
		card := fmt.Sprintf("⚠️ <b>Confirm Dangerous Setting Change</b>\n\n"+
			"<b>Key:</b> <code>%s</code>\n"+
			"<b>Old:</b> <code>%s</code>\n"+
			"<b>New:</b> <code>%s</code>\n\n"+
			"Are you sure you want to apply this change?",
			html.EscapeString(key),
			html.EscapeString(settings.MaskValue(oldVal, spec.Secret)),
			html.EscapeString(settings.MaskValue(val, spec.Secret)),
		)
		kb := tg.Keyboard{
			{
				tg.Button{Text: "✅ Confirm", Data: "set:cnf:" + token},
				tg.Button{Text: "❌ Cancel", Data: "set:cnfcanc:" + token},
			},
		}
		_, _ = a.tg.Send(ctx, m.Chat.ID, card, tg.SendOpts{Keyboard: kb})
		return
	}

	rec, err := a.settingsMgr.Set(ctx, m.From.ID, key, val, "cmd_set")
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Failed to set <code>%s</code>: %s", html.EscapeString(key), html.EscapeString(err.Error())))
		return
	}

	badge := ""
	if spec.Apply == settings.ApplyRestart {
		badge = "\n<i>(Restart required to take effect)</i>"
	}
	a.reply(ctx, m, fmt.Sprintf("✅ Updated <code>%s</code> = <code>%s</code>%s",
		html.EscapeString(key),
		html.EscapeString(settings.MaskValue(rec.NewValue, spec.Secret)),
		badge,
	))
}

func (a *App) cmdGet(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if ok, errMsg := a.checkOwnerPrivate(m.From.ID, &m.Chat); !ok {
		a.reply(ctx, m, errMsg)
		return
	}
	parts := strings.Fields(m.Text)
	if len(parts) < 2 {
		a.reply(ctx, m, "Usage: <code>/get &lt;KEY&gt;</code>")
		return
	}
	key := settings.CleanKey(parts[1])
	text, kb, err := a.renderKeyCard(key)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Error: %s", html.EscapeString(err.Error())))
		return
	}
	_, _ = a.tg.Send(ctx, m.Chat.ID, text, tg.SendOpts{Keyboard: kb})
}

func (a *App) cmdUnset(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if ok, errMsg := a.checkOwnerPrivate(m.From.ID, &m.Chat); !ok {
		a.reply(ctx, m, errMsg)
		return
	}
	parts := strings.Fields(m.Text)
	if len(parts) < 2 {
		a.reply(ctx, m, "Usage: <code>/unset &lt;KEY&gt;</code>")
		return
	}
	key := settings.CleanKey(parts[1])
	rec, err := a.settingsMgr.Reset(ctx, m.From.ID, key, "cmd_unset")
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Failed to reset <code>%s</code>: %s", html.EscapeString(key), html.EscapeString(err.Error())))
		return
	}
	if rec == nil {
		a.reply(ctx, m, fmt.Sprintf("Setting <code>%s</code> has no active override.", html.EscapeString(key)))
		return
	}
	spec, _ := a.settingsMgr.Registry().Get(key)
	isSecret := spec != nil && spec.Secret
	a.reply(ctx, m, fmt.Sprintf("🔄 Reset <code>%s</code> to <code>%s</code>",
		html.EscapeString(key),
		html.EscapeString(settings.MaskValue(rec.NewValue, isSecret)),
	))
}

func (a *App) cmdRestart(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if ok, errMsg := a.checkOwnerPrivate(m.From.ID, &m.Chat); !ok {
		a.reply(ctx, m, errMsg)
		return
	}

	isDocker := false
	if _, err := os.Stat("/.dockerenv"); err == nil {
		isDocker = true
	} else if os.Getenv("DOCKER_CONTAINER") != "" || os.Getenv("CONTAINER") != "" {
		isDocker = true
	}

	msg := "Restarting bot cleanly… Docker will restart the container."
	if !isDocker {
		msg = "Shutting down bot cleanly with exit code 0.\n\n⚠️ <i>No Docker container detected (/.dockerenv missing); process will not auto-restart.</i>"
	}
	a.reply(ctx, m, msg)

	go func() {
		time.Sleep(500 * time.Millisecond)
		p, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}()
}

func (a *App) handleSettingsExport(ctx context.Context, m *models.Message) {
	data, err := a.settingsMgr.Export()
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Export failed: %s", html.EscapeString(err.Error())))
		return
	}

	tmpDir := filepath.Join(a.cfg.DataDir, "tmp")
	_ = os.MkdirAll(tmpDir, 0o700)
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("settings-export-%d.json", time.Now().Unix()))
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		a.reply(ctx, m, fmt.Sprintf("Failed to write export file: %s", html.EscapeString(err.Error())))
		return
	}
	defer os.Remove(tmpFile)

	u := tg.Upload{
		Path:    tmpFile,
		Caption: "<b>tgdl Settings Export</b>\n<blockquote>Secrets are redacted.</blockquote>",
		Kind:    tg.KindDocument,
	}
	if _, err := a.tg.Upload(ctx, m.Chat.ID, u); err != nil {
		a.reply(ctx, m, fmt.Sprintf("Upload failed: %s", html.EscapeString(err.Error())))
	}
}

func (a *App) handleSettingsImport(ctx context.Context, m *models.Message) {
	if m.ReplyToMessage == nil || m.ReplyToMessage.Document == nil {
		a.reply(ctx, m, "Please reply to a settings JSON document with <code>/settings import</code> to import.")
		return
	}
	doc := m.ReplyToMessage.Document
	f := tg.FileRef{
		FileID: doc.FileID,
		Name:   doc.FileName,
		Size:   int64(doc.FileSize),
	}
	if f.Size > 2<<20 {
		a.reply(ctx, m, "Import file too large (max 2 MB).")
		return
	}

	tmpDir := filepath.Join(a.cfg.DataDir, "tmp")
	_ = os.MkdirAll(tmpDir, 0o700)
	downloadedPath, err := a.tg.Download(ctx, f, tmpDir, nil)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Failed to download import file: %s", html.EscapeString(err.Error())))
		return
	}
	defer os.Remove(downloadedPath)

	df, err := os.Open(downloadedPath)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Open file error: %s", html.EscapeString(err.Error())))
		return
	}
	defer df.Close()

	raw, err := io.ReadAll(io.LimitReader(df, 2<<20))
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Read file error: %s", html.EscapeString(err.Error())))
		return
	}

	diffs, applyFn, err := a.settingsMgr.Import(raw)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Import validation failed:\n<code>%s</code>", html.EscapeString(err.Error())))
		return
	}
	if len(diffs) == 0 {
		a.reply(ctx, m, "No changes detected in imported file.")
		return
	}

	token := a.settingsUI.addPendingImport(applyFn)
	var sb strings.Builder
	sb.WriteString("📥 <b>Settings Import Preview</b>\n\n")
	for _, d := range diffs {
		sb.WriteString(fmt.Sprintf("• <b>%s</b>: <code>%s</code> ➔ <code>%s</code>\n",
			html.EscapeString(d.Key),
			html.EscapeString(settings.MaskValue(d.OldVal, d.IsSecret)),
			html.EscapeString(settings.MaskValue(d.NewVal, d.IsSecret)),
		))
	}
	sb.WriteString("\nApply these changes?")

	kb := tg.Keyboard{
		{
			tg.Button{Text: "✅ Apply Import", Data: "set:imp:cnf:" + token},
			tg.Button{Text: "❌ Cancel", Data: "set:imp:canc:" + token},
		},
	}
	_, _ = a.tg.Send(ctx, m.Chat.ID, sb.String(), tg.SendOpts{Keyboard: kb})
}

func (a *App) handleSettingsUndo(ctx context.Context, m *models.Message) {
	last, err := a.settingsMgr.Undo(ctx, m.From.ID)
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("Undo failed: %s", html.EscapeString(err.Error())))
		return
	}
	spec, _ := a.settingsMgr.Registry().Get(last.Key)
	isSecret := spec != nil && spec.Secret
	a.reply(ctx, m, fmt.Sprintf("↩️ Undid change for <code>%s</code>: restored to <code>%s</code>",
		html.EscapeString(last.Key),
		html.EscapeString(settings.MaskValue(last.OldValue, isSecret)),
	))
}

func (a *App) cbSettings(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	q := u.CallbackQuery
	if q == nil || q.From.ID == 0 {
		return
	}
	chatID, msgID, ok := cbMessage(q)
	if !ok {
		return
	}

	var chat *models.Chat
	if q.Message.Message != nil {
		chat = &q.Message.Message.Chat
	}
	if isOk, errMsg := a.checkOwnerPrivate(q.From.ID, chat); !isOk {
		a.ack(ctx, q, errMsg, true)
		return
	}

	data := q.Data
	switch {
	case data == "set:home":
		a.ack(ctx, q, "", false)
		text, kb := a.renderGroupsMenu()
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case data == "set:close":
		a.ack(ctx, q, "Closed.", false)
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: chatID, ID: msgID})

	case data == "set:hist":
		a.ack(ctx, q, "", false)
		text, kb := a.renderHistoryCard()
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case data == "set:chg":
		a.ack(ctx, q, "", false)
		text, kb := a.renderChangedCard()
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case data == "set:undo":
		last, err := a.settingsMgr.Undo(ctx, q.From.ID)
		if err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		a.ack(ctx, q, "Undid change for "+last.Key, false)
		text, kb := a.renderHistoryCard()
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case data == "set:rstall":
		if err := a.settingsMgr.ResetAll(ctx, q.From.ID, "ui_reset_all"); err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		a.ack(ctx, q, "Reset all overrides to defaults.", false)
		text, kb := a.renderChangedCard()
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case strings.HasPrefix(data, "set:g:"):
		parts := strings.Split(data[len("set:g:"):], ":p:")
		grp := parts[0]
		page := 0
		if len(parts) > 1 {
			page, _ = strconv.Atoi(parts[1])
		}
		a.ack(ctx, q, "", false)
		text, kb := a.renderGroupKeys(grp, page)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case strings.HasPrefix(data, "set:k:"):
		key := data[len("set:k:"):]
		a.ack(ctx, q, "", false)
		text, kb, err := a.renderKeyCard(key)
		if err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case strings.HasPrefix(data, "set:edit:"):
		key := data[len("set:edit:"):]
		spec, ok := a.settingsMgr.Registry().Get(key)
		if !ok || spec.Apply == settings.ApplyReadOnly {
			a.ack(ctx, q, "This setting is read-only.", true)
			return
		}
		a.settingsUI.setInputState(q.From.ID, key)
		a.ack(ctx, q, "Awaiting input…", false)
		prompt := fmt.Sprintf("✏️ Send the new value for <code>%s</code> in this private chat (60s timeout).\n\n<i>Type /cancel to abort.</i>", html.EscapeString(key))
		_, _ = a.tg.Send(ctx, chatID, prompt, tg.SendOpts{})

	case strings.HasPrefix(data, "set:rst:"):
		key := data[len("set:rst:"):]
		rec, err := a.settingsMgr.Reset(ctx, q.From.ID, key, "ui_reset")
		if err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		if rec == nil {
			a.ack(ctx, q, "Already using default/env value.", false)
		} else {
			a.ack(ctx, q, "Reset "+key, false)
		}
		text, kb, _ := a.renderKeyCard(key)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case strings.HasPrefix(data, "set:tog:"):
		rem := data[len("set:tog:"):]
		parts := strings.SplitN(rem, ":", 2)
		if len(parts) != 2 {
			return
		}
		key, newVal := parts[0], parts[1]
		rec, err := a.settingsMgr.Set(ctx, q.From.ID, key, newVal, "ui_toggle")
		if err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		spec, _ := a.settingsMgr.Registry().Get(key)
		isSecret := spec != nil && spec.Secret
		a.ack(ctx, q, "Updated "+key+" = "+settings.MaskValue(rec.NewValue, isSecret), false)
		text, kb, _ := a.renderKeyCard(key)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, text, tg.SendOpts{Keyboard: kb})

	case strings.HasPrefix(data, "set:cnf:"):
		tok := data[len("set:cnf:"):]
		pc, found := a.settingsUI.getPendingChange(tok)
		if !found {
			a.ack(ctx, q, "Confirmation has expired.", true)
			return
		}
		rec, err := a.settingsMgr.Set(ctx, q.From.ID, pc.Key, pc.Value, "confirmed")
		if err != nil {
			a.ack(ctx, q, err.Error(), true)
			return
		}
		spec, _ := a.settingsMgr.Registry().Get(pc.Key)
		isSecret := spec != nil && spec.Secret
		a.ack(ctx, q, "Confirmed and updated "+pc.Key, false)
		confMsg := fmt.Sprintf("✅ <b>Setting Updated</b>\n\n<code>%s</code> = <code>%s</code>",
			html.EscapeString(pc.Key),
			html.EscapeString(settings.MaskValue(rec.NewValue, isSecret)),
		)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, confMsg, tg.SendOpts{})

	case strings.HasPrefix(data, "set:cnfcanc:"):
		tok := data[len("set:cnfcanc:"):]
		_, _ = a.settingsUI.getPendingChange(tok)
		a.ack(ctx, q, "Cancelled.", false)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, "❌ Change cancelled.", tg.SendOpts{})

	case strings.HasPrefix(data, "set:imp:cnf:"):
		tok := data[len("set:imp:cnf:"):]
		fn, found := a.settingsUI.getPendingImport(tok)
		if !found {
			a.ack(ctx, q, "Import confirmation has expired.", true)
			return
		}
		if err := fn(ctx, q.From.ID); err != nil {
			a.ack(ctx, q, "Import error: "+err.Error(), true)
			return
		}
		a.ack(ctx, q, "Import applied successfully.", false)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, "✅ <b>Settings Imported Successfully</b>", tg.SendOpts{})

	case strings.HasPrefix(data, "set:imp:canc:"):
		tok := data[len("set:imp:canc:"):]
		_, _ = a.settingsUI.getPendingImport(tok)
		a.ack(ctx, q, "Import cancelled.", false)
		_ = a.tg.Edit(ctx, tg.MessageRef{ChatID: chatID, ID: msgID}, "❌ Import cancelled.", tg.SendOpts{})
	}
}

func (a *App) handleSettingsInput(ctx context.Context, b *tgbot.Bot, m *models.Message) bool {
	if a.settingsUI == nil || a.auth == nil || !a.auth.IsOwner(m.From.ID) || m.Chat.Type != "private" {
		return false
	}

	key, active := a.settingsUI.getInputState(m.From.ID)
	if !active {
		return false
	}
	a.settingsUI.clearInputState(m.From.ID)

	text := strings.TrimSpace(m.Text)
	if text == "/cancel" {
		a.reply(ctx, m, "Edit cancelled.")
		return true
	}

	spec, ok := a.settingsMgr.Registry().Get(key)
	if !ok {
		a.reply(ctx, m, "Unknown setting.")
		return true
	}

	if spec.Secret {
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
	}

	if settings.IsDangerous(spec, text) {
		oldVal, _, _, _ := a.settingsMgr.Get(key)
		token := a.settingsUI.addPendingChange(key, text)
		card := fmt.Sprintf("⚠️ <b>Confirm Dangerous Setting Change</b>\n\n"+
			"<b>Key:</b> <code>%s</code>\n"+
			"<b>Old:</b> <code>%s</code>\n"+
			"<b>New:</b> <code>%s</code>\n\n"+
			"Are you sure you want to apply this change?",
			html.EscapeString(key),
			html.EscapeString(settings.MaskValue(oldVal, spec.Secret)),
			html.EscapeString(settings.MaskValue(text, spec.Secret)),
		)
		kb := tg.Keyboard{
			{
				tg.Button{Text: "✅ Confirm", Data: "set:cnf:" + token},
				tg.Button{Text: "❌ Cancel", Data: "set:cnfcanc:" + token},
			},
		}
		_, _ = a.tg.Send(ctx, m.Chat.ID, card, tg.SendOpts{Keyboard: kb})
		return true
	}

	rec, err := a.settingsMgr.Set(ctx, m.From.ID, key, text, "private_input")
	if err != nil {
		a.reply(ctx, m, fmt.Sprintf("❌ Invalid value for <code>%s</code>: %s", html.EscapeString(key), html.EscapeString(err.Error())))
		return true
	}

	badge := ""
	if spec.Apply == settings.ApplyRestart {
		badge = "\n<i>(Restart required to take effect)</i>"
	}
	a.reply(ctx, m, fmt.Sprintf("✅ Updated <code>%s</code> = <code>%s</code>%s",
		html.EscapeString(key),
		html.EscapeString(settings.MaskValue(rec.NewValue, spec.Secret)),
		badge,
	))
	return true
}

func (a *App) renderGroupsMenu() (string, tg.Keyboard) {
	text := "⚙️ <b>Settings Dashboard</b>\n\nSelect a configuration group to inspect and modify settings:"
	var kb tg.Keyboard
	row := []tg.Button{}
	for _, grp := range settings.AllGroups {
		row = append(row, tg.Button{Text: grp, Data: "set:g:" + grp})
		if len(row) == 2 {
			kb = append(kb, row)
			row = []tg.Button{}
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}

	bottomRow := []tg.Button{
		{Text: "📜 History", Data: "set:hist"},
		{Text: "📝 Overrides", Data: "set:chg"},
		{Text: "❌ Close", Data: "set:close"},
	}
	kb = append(kb, bottomRow)
	return text, kb
}

func (a *App) renderGroupKeys(group string, page int) (string, tg.Keyboard) {
	specs := a.settingsMgr.Registry().InGroup(group)
	perPage := 8
	totalPages := (len(specs) + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * perPage
	end := min(len(specs), start+perPage)

	var kb tg.Keyboard
	for i := start; i < end; i++ {
		sp := specs[i]
		val, src, _, _ := a.settingsMgr.Get(sp.Key)
		masked := settings.MaskValue(val, sp.Secret)
		if len(masked) > 10 {
			masked = masked[:8] + ".."
		}

		marker := ""
		if src == settings.SourceOverride {
			marker = " •"
		} else if sp.Apply == settings.ApplyRestart {
			marker = " ↻"
		} else if sp.Apply == settings.ApplyReadOnly {
			marker = " 🔒"
		}

		btnText := fmt.Sprintf("%s = %s%s", sp.Key, masked, marker)
		if len(btnText) > 34 {
			btnText = btnText[:31] + "..."
		}
		kb = append(kb, []tg.Button{{Text: btnText, Data: "set:k:" + sp.Key}})
	}

	if totalPages > 1 {
		var navRow []tg.Button
		if page > 0 {
			navRow = append(navRow, tg.Button{Text: "◀ Prev", Data: fmt.Sprintf("set:g:%s:p:%d", group, page-1)})
		}
		navRow = append(navRow, tg.Button{Text: fmt.Sprintf("%d/%d", page+1, totalPages), Data: fmt.Sprintf("set:g:%s:p:%d", group, page)})
		if page < totalPages-1 {
			navRow = append(navRow, tg.Button{Text: "Next ▶", Data: fmt.Sprintf("set:g:%s:p:%d", group, page+1)})
		}
		kb = append(kb, navRow)
	}

	kb = append(kb, []tg.Button{{Text: "◀ Back to Groups", Data: "set:home"}})

	text := fmt.Sprintf("📂 <b>Settings: %s</b>\n\nPage %d of %d. Tap a setting to edit:", html.EscapeString(group), page+1, totalPages)
	return text, kb
}

func (a *App) renderKeyCard(key string) (string, tg.Keyboard, error) {
	val, src, spec, err := a.settingsMgr.Get(key)
	if err != nil {
		return "", nil, err
	}

	maskedCurrent := settings.MaskValue(val, spec.Secret)
	maskedDefault := settings.MaskValue(spec.Default, spec.Secret)

	allowed := allowedSummary(spec)
	card := fmt.Sprintf("⚙️ <b>%s</b>\n\n"+
		"<b>Group:</b> %s\n"+
		"<b>Description:</b> %s\n"+
		"<b>Type:</b> <code>%s</code>\n"+
		"<b>Allowed:</b> %s\n"+
		"<b>Default:</b> <code>%s</code>\n"+
		"<b>Current Value:</b> <code>%s</code>\n"+
		"<b>Source:</b> <code>%s</code>\n"+
		"<b>Apply Mode:</b> <i>%s</i>\n",
		html.EscapeString(spec.Key),
		html.EscapeString(spec.Group),
		html.EscapeString(spec.Description),
		html.EscapeString(spec.Type),
		html.EscapeString(allowed),
		html.EscapeString(maskedDefault),
		html.EscapeString(maskedCurrent),
		html.EscapeString(string(src)),
		html.EscapeString(spec.Apply.String()),
	)

	var kb tg.Keyboard
	if spec.Apply != settings.ApplyReadOnly {
		switch spec.Type {
		case "bool":
			b, _ := settings.ParseBool(val)
			nextBool := !b
			toggleBtn := tg.Button{Text: fmt.Sprintf("Toggle to %v", nextBool), Data: fmt.Sprintf("set:tog:%s:%v", spec.Key, nextBool)}
			resetBtn := tg.Button{Text: "🔄 Reset to default", Data: "set:rst:" + spec.Key}
			kb = append(kb, []tg.Button{toggleBtn, resetBtn})
		case "enum":
			var row []tg.Button
			for _, e := range spec.Enum {
				if !strings.EqualFold(e, val) {
					row = append(row, tg.Button{Text: e, Data: fmt.Sprintf("set:tog:%s:%s", spec.Key, e)})
					if len(row) == 2 {
						kb = append(kb, row)
						row = []tg.Button{}
					}
				}
			}
			if len(row) > 0 {
				kb = append(kb, row)
			}
			kb = append(kb, []tg.Button{{Text: "🔄 Reset to default", Data: "set:rst:" + spec.Key}})
		default:
			kb = append(kb, []tg.Button{
				{Text: "✏️ Edit", Data: "set:edit:" + spec.Key},
				{Text: "🔄 Reset to default", Data: "set:rst:" + spec.Key},
			})
		}
	}

	kb = append(kb, []tg.Button{{Text: "◀ Back to " + spec.Group, Data: "set:g:" + spec.Group}})
	return card, kb, nil
}

func (a *App) renderHistoryCard() (string, tg.Keyboard) {
	history := a.settingsMgr.History()
	var sb strings.Builder
	sb.WriteString("📜 <b>Settings Change History</b> (last 50):\n\n")
	if len(history) == 0 {
		sb.WriteString("<i>No changes recorded yet.</i>")
	} else {
		for i := len(history) - 1; i >= 0; i-- {
			rec := history[i]
			spec, _ := a.settingsMgr.Registry().Get(rec.Key)
			isSecret := spec != nil && spec.Secret
			sb.WriteString(fmt.Sprintf("• [%s] <b>%s</b>: <code>%s</code> ➔ <code>%s</code> (src: %s)\n",
				rec.Timestamp.Format("15:04:05"),
				html.EscapeString(rec.Key),
				html.EscapeString(settings.MaskValue(rec.OldValue, isSecret)),
				html.EscapeString(settings.MaskValue(rec.NewValue, isSecret)),
				html.EscapeString(rec.Source),
			))
		}
	}

	var kb tg.Keyboard
	if len(history) > 0 {
		kb = append(kb, []tg.Button{{Text: "↩️ Undo Last Change", Data: "set:undo"}})
	}
	kb = append(kb, []tg.Button{{Text: "◀ Back to Settings", Data: "set:home"}})
	return sb.String(), kb
}

func (a *App) renderChangedCard() (string, tg.Keyboard) {
	changed := a.settingsMgr.Changed()
	var sb strings.Builder
	sb.WriteString("📝 <b>Current Bot Settings Overrides</b>\n\n")
	if len(changed) == 0 {
		sb.WriteString("<i>No active overrides. All settings are loaded from defaults or environment.</i>")
	} else {
		for k, v := range changed {
			spec, _ := a.settingsMgr.Registry().Get(k)
			isSecret := spec != nil && spec.Secret
			sb.WriteString(fmt.Sprintf("• <b>%s</b> = <code>%s</code>\n",
				html.EscapeString(k),
				html.EscapeString(settings.MaskValue(v, isSecret)),
			))
		}
	}

	var kb tg.Keyboard
	if len(changed) > 0 {
		kb = append(kb, []tg.Button{{Text: "🔄 Reset All Overrides", Data: "set:rstall"}})
	}
	kb = append(kb, []tg.Button{{Text: "◀ Back to Settings", Data: "set:home"}})
	return sb.String(), kb
}

func (a *App) renderSearchResults(query string) (string, tg.Keyboard) {
	matches := a.settingsMgr.Registry().Search(query)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 <b>Search results for:</b> <code>%s</code>\n\n", html.EscapeString(query)))
	if len(matches) == 0 {
		sb.WriteString("<i>No matching settings found.</i>")
	}

	var kb tg.Keyboard
	for i, sp := range matches {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("\n<i>...and %d more matches</i>", len(matches)-10))
			break
		}
		val, _, _, _ := a.settingsMgr.Get(sp.Key)
		masked := settings.MaskValue(val, sp.Secret)
		if len(masked) > 10 {
			masked = masked[:8] + ".."
		}
		btnText := fmt.Sprintf("%s = %s", sp.Key, masked)
		if len(btnText) > 34 {
			btnText = btnText[:31] + "..."
		}
		kb = append(kb, []tg.Button{{Text: btnText, Data: "set:k:" + sp.Key}})
	}

	kb = append(kb, []tg.Button{{Text: "◀ Back to Settings", Data: "set:home"}})
	return sb.String(), kb
}

func allowedSummary(spec *settings.Spec) string {
	if spec == nil {
		return ""
	}
	switch spec.Type {
	case "bool":
		return "true | false"
	case "enum":
		return strings.Join(spec.Enum, " | ")
	case "int", "int64":
		if spec.Min > 0 && spec.Max > 0 {
			return fmt.Sprintf("%d to %d", spec.Min, spec.Max)
		} else if spec.Min > 0 {
			return fmt.Sprintf(">= %d", spec.Min)
		}
		return "integer"
	case "size":
		if spec.Max > 0 {
			return fmt.Sprintf("up to %s", humanSize(spec.Max))
		}
		return "bytes (e.g. 512M, 2G)"
	case "duration":
		return "duration (e.g. 30s, 15m, 2h)"
	case "path":
		return "filesystem path"
	case "idlist":
		return "comma-separated user IDs"
	case "secret":
		return "secret string"
	default:
		return "text"
	}
}

func humanSize(b int64) string {
	if b >= 1024*1024*1024 && b%(1024*1024*1024) == 0 {
		return fmt.Sprintf("%d GiB", b/(1024*1024*1024))
	}
	if b >= 1024*1024 && b%(1024*1024) == 0 {
		return fmt.Sprintf("%d MiB", b/(1024*1024))
	}
	if b >= 1024 && b%1024 == 0 {
		return fmt.Sprintf("%d KiB", b/1024)
	}
	return strconv.FormatInt(b, 10) + " B"
}
