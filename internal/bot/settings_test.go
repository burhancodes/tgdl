package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/settings"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/go-telegram/bot/models"
)

type fakeSettingsTGClient struct {
	tg.Client
	sentMessages    []string
	sentKeyboards   []tg.Keyboard
	editedMessages  []string
	deletedMessages []tg.MessageRef
	answers         []string
}

func (f *fakeSettingsTGClient) Send(ctx context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	f.sentMessages = append(f.sentMessages, html)
	f.sentKeyboards = append(f.sentKeyboards, o.Keyboard)
	return tg.MessageRef{ChatID: chatID, ID: len(f.sentMessages)}, nil
}

func (f *fakeSettingsTGClient) Edit(ctx context.Context, ref tg.MessageRef, html string, o tg.SendOpts) error {
	f.editedMessages = append(f.editedMessages, html)
	return nil
}

func (f *fakeSettingsTGClient) Delete(ctx context.Context, ref tg.MessageRef) error {
	f.deletedMessages = append(f.deletedMessages, ref)
	return nil
}

func (f *fakeSettingsTGClient) Answer(ctx context.Context, callbackID, text string, alert bool) error {
	f.answers = append(f.answers, text)
	return nil
}

func newTestSettingsApp(t *testing.T, ownerID int64, hasOwner bool) (*App, *fakeSettingsTGClient, *settings.Manager) {
	dir := t.TempDir()
	cfg := &config.Config{
		OwnerID:  ownerID,
		HasOwner: hasOwner,
		DataDir:  dir,
	}
	authz := auth.New(cfg)
	reg := settings.NewRegistry()
	store := settings.NewStore(dir, "", false, "", reg)
	mgr, err := settings.NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	fakeTG := &fakeSettingsTGClient{}
	ui := NewSettingsUI(mgr)
	app := &App{
		cfg:         cfg,
		auth:        authz,
		tg:          fakeTG,
		settingsMgr: mgr,
		settingsUI:  ui,
	}
	return app, fakeTG, mgr
}

func TestSettingsAuthorization(t *testing.T) {
	app, fakeTG, _ := newTestSettingsApp(t, 999, true)
	ctx := context.Background()

	nonOwnerMsg := &models.Update{
		Message: &models.Message{
			ID:   1,
			Chat: models.Chat{ID: 100, Type: "private"},
			From: &models.User{ID: 111},
			Text: "/settings",
		},
	}
	app.cmdSettings(ctx, nil, nonOwnerMsg)
	if len(fakeTG.sentMessages) == 0 || !strings.Contains(fakeTG.sentMessages[0], "not authorized") {
		t.Errorf("expected non-owner to be rejected, got %v", fakeTG.sentMessages)
	}

	fakeTG.sentMessages = nil
	groupMsg := &models.Update{
		Message: &models.Message{
			ID:   2,
			Chat: models.Chat{ID: -100, Type: "group"},
			From: &models.User{ID: 999},
			Text: "/settings",
		},
	}
	app.cmdSettings(ctx, nil, groupMsg)
	if len(fakeTG.sentMessages) == 0 || !strings.Contains(fakeTG.sentMessages[0], "private chat") {
		t.Errorf("expected group usage to be rejected, got %v", fakeTG.sentMessages)
	}

	appNoOwner, fakeTGNoOwner, _ := newTestSettingsApp(t, 0, false)
	ownerMsg := &models.Update{
		Message: &models.Message{
			ID:   3,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/settings",
		},
	}
	appNoOwner.cmdSettings(ctx, nil, ownerMsg)
	if len(fakeTGNoOwner.sentMessages) == 0 || !strings.Contains(fakeTGNoOwner.sentMessages[0], "no owner is configured") {
		t.Errorf("expected no owner to disable settings, got %v", fakeTGNoOwner.sentMessages)
	}
}

func TestSettingsCmdFlow(t *testing.T) {
	app, fakeTG, mgr := newTestSettingsApp(t, 999, true)
	ctx := context.Background()

	setMsg := &models.Update{
		Message: &models.Message{
			ID:   10,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/set LOG_LEVEL DEBUG",
		},
	}
	app.cmdSet(ctx, nil, setMsg)
	val, _, _, _ := mgr.Get("LOG_LEVEL")
	if val != "DEBUG" {
		t.Errorf("got %q, want DEBUG", val)
	}

	badMsg := &models.Update{
		Message: &models.Message{
			ID:   11,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/set LOG_LEVEL INVALID",
		},
	}
	fakeTG.sentMessages = nil
	app.cmdSet(ctx, nil, badMsg)
	val, _, _, _ = mgr.Get("LOG_LEVEL")
	if val != "DEBUG" {
		t.Errorf("expected old value DEBUG to be preserved, got %q", val)
	}
	if len(fakeTG.sentMessages) == 0 || !strings.Contains(fakeTG.sentMessages[0], "Failed to set") {
		t.Errorf("expected error message sent to user, got %v", fakeTG.sentMessages)
	}

	secretMsg := &models.Update{
		Message: &models.Message{
			ID:   12,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/set GOFILE_API_KEY my-super-secret-key-1234",
		},
	}
	app.cmdSet(ctx, nil, secretMsg)
	if len(fakeTG.deletedMessages) == 0 || fakeTG.deletedMessages[0].ID != 12 {
		t.Errorf("expected secret input message to be deleted immediately")
	}

	unsetMsg := &models.Update{
		Message: &models.Message{
			ID:   13,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/unset LOG_LEVEL",
		},
	}
	app.cmdUnset(ctx, nil, unsetMsg)
	val, src, _, _ := mgr.Get("LOG_LEVEL")
	if val != "INFO" || src != settings.SourceDefault {
		t.Errorf("after unset got (%q, %s), want (INFO, default)", val, src)
	}
}

func TestSettingsInteractiveInput(t *testing.T) {
	app, fakeTG, mgr := newTestSettingsApp(t, 999, true)
	ctx := context.Background()

	app.settingsUI.setInputState(999, "MAX_JOBS_PER_CHAT")

	inputMsg := &models.Update{
		Message: &models.Message{
			ID:   20,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "5",
		},
	}
	handled := app.handleSettingsInput(ctx, nil, inputMsg.Message)
	if !handled {
		t.Fatalf("expected input to be handled")
	}

	val, _, _, _ := mgr.Get("MAX_JOBS_PER_CHAT")
	if val != "5" {
		t.Errorf("got %q, want 5", val)
	}

	app.settingsUI.setInputState(999, "MAX_JOBS_PER_CHAT")
	cancelMsg := &models.Update{
		Message: &models.Message{
			ID:   21,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/cancel",
		},
	}
	fakeTG.sentMessages = nil
	handled = app.handleSettingsInput(ctx, nil, cancelMsg.Message)
	if !handled {
		t.Fatalf("expected cancel to be handled")
	}
	if len(fakeTG.sentMessages) == 0 || !strings.Contains(fakeTG.sentMessages[0], "cancelled") {
		t.Errorf("expected cancelled message, got %v", fakeTG.sentMessages)
	}
}

func TestSettingsCallbacks(t *testing.T) {
	app, fakeTG, mgr := newTestSettingsApp(t, 999, true)
	ctx := context.Background()

	cbUpdate := &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb1",
			From: models.User{ID: 999},
			Data: "set:home",
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   10,
					Chat: models.Chat{ID: 999, Type: "private"},
				},
			},
		},
	}
	app.cbSettings(ctx, nil, cbUpdate)
	if len(fakeTG.editedMessages) == 0 || !strings.Contains(fakeTG.editedMessages[0], "Settings Dashboard") {
		t.Errorf("expected groups menu rendered, got %v", fakeTG.editedMessages)
	}

	fakeTG.editedMessages = nil
	cbUpdate.CallbackQuery.Data = "set:tog:PIN_STATUS_MESSAGE:false"
	app.cbSettings(ctx, nil, cbUpdate)
	val, _, _, _ := mgr.Get("PIN_STATUS_MESSAGE")
	if val != "false" {
		t.Errorf("expected PIN_STATUS_MESSAGE toggled to false, got %q", val)
	}
}

func TestSettingsDangerousConfirmation(t *testing.T) {
	app, fakeTG, mgr := newTestSettingsApp(t, 999, true)
	ctx := context.Background()

	setMsg := &models.Update{
		Message: &models.Message{
			ID:   30,
			Chat: models.Chat{ID: 999, Type: "private"},
			From: &models.User{ID: 999},
			Text: "/set MIN_FREE_DISK_BYTES 0",
		},
	}
	app.cmdSet(ctx, nil, setMsg)

	val, _, _, _ := mgr.Get("MIN_FREE_DISK_BYTES")
	if val == "0" {
		t.Errorf("dangerous value should not be applied immediately without confirmation")
	}
	if len(fakeTG.sentMessages) == 0 || !strings.Contains(fakeTG.sentMessages[0], "Confirm Dangerous Setting Change") {
		t.Errorf("expected confirmation prompt, got %v", fakeTG.sentMessages)
	}

	var token string
	if len(fakeTG.sentKeyboards) > 0 {
		for _, row := range fakeTG.sentKeyboards[len(fakeTG.sentKeyboards)-1] {
			for _, btn := range row {
				if strings.HasPrefix(btn.Data, "set:cnf:") {
					token = btn.Data[len("set:cnf:"):]
					break
				}
			}
		}
	}
	if token == "" {
		t.Fatalf("no confirmation token button found in keyboard")
	}

	cbConfirm := &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb_conf",
			From: models.User{ID: 999},
			Data: "set:cnf:" + token,
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   31,
					Chat: models.Chat{ID: 999, Type: "private"},
				},
			},
		},
	}
	app.cbSettings(ctx, nil, cbConfirm)
	val, _, _, _ = mgr.Get("MIN_FREE_DISK_BYTES")
	if val != "0" {
		t.Errorf("after confirmation expected MIN_FREE_DISK_BYTES=0, got %q", val)
	}
}
