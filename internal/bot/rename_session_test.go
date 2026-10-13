package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/rename"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/go-telegram/bot/models"
)

type mockSessionTG struct {
	tg.Client
	edits   []string
	answers []string
	sends   []string
}

func (m *mockSessionTG) Edit(_ context.Context, ref tg.MessageRef, html string, o tg.SendOpts) error {
	m.edits = append(m.edits, html)
	return nil
}

func (m *mockSessionTG) Send(_ context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	m.sends = append(m.sends, html)
	return tg.MessageRef{ChatID: chatID, ID: 123}, nil
}

func (m *mockSessionTG) Answer(_ context.Context, callbackID, text string, alert bool) error {
	m.answers = append(m.answers, text)
	return nil
}

func TestRenameSession_Lifecycle(t *testing.T) {
	cfg := &config.Config{
		RenameMaxBatch:   5,
		RenameSessionTTL: 50 * time.Millisecond,
	}
	mock := &mockSessionTG{}
	mgr := NewRenameSessionManager(cfg, mock)
	defer mgr.Stop()

	chatID := int64(100)
	userID := int64(200)
	otherUser := int64(999)

	sess := mgr.Start(chatID, userID, "{n:2} - {name}.{ext}", rename.Options{Start: 1, Step: 1}, false, 777)
	if sess == nil {
		t.Fatal("expected non-nil session")
	}

	got, ok := mgr.Get(chatID, userID)
	if !ok || got == nil {
		t.Fatal("expected to find active session")
	}
	if got.Scheme != "{n:2} - {name}.{ext}" {
		t.Errorf("scheme mismatch: %s", got.Scheme)
	}

	if _, ok := mgr.Get(chatID, otherUser); ok {
		t.Error("another user should not have access to this session")
	}

	for i := 1; i <= 5; i++ {
		f := dl.TGFile{FileID: string(rune('0' + i)), Name: "file.txt", Size: 10}
		added, atLimit, count := mgr.AddFile(chatID, userID, f)
		if !added {
			t.Errorf("failed to add file %d", i)
		}
		if i == 5 && !atLimit {
			t.Error("expected atLimit to be true when reaching max batch")
		}
		if count != i {
			t.Errorf("expected count %d, got %d", i, count)
		}
	}

	fOverflow := dl.TGFile{FileID: "6", Name: "overflow.txt", Size: 10}
	added, atLimit, count := mgr.AddFile(chatID, userID, fOverflow)
	if added {
		t.Error("expected 6th file to be rejected by max batch limit")
	}
	if !atLimit || count != 5 {
		t.Errorf("expected atLimit=true count=5, got atLimit=%v count=%d", atLimit, count)
	}

	if ok := mgr.SetScheme(chatID, userID, "{date:20060102}_{name}.{ext}"); !ok {
		t.Error("failed to change scheme")
	}
	got, _ = mgr.Get(chatID, userID)
	if got.Scheme != "{date:20060102}_{name}.{ext}" {
		t.Errorf("scheme not updated: %s", got.Scheme)
	}

	if ok := mgr.Clear(chatID, userID); !ok {
		t.Error("failed to clear files")
	}
	got, _ = mgr.Get(chatID, userID)
	if len(got.Files) != 0 {
		t.Errorf("files not cleared, len=%d", len(got.Files))
	}

	time.Sleep(70 * time.Millisecond)
	if _, ok := mgr.Get(chatID, userID); ok {
		t.Error("expected session to expire after TTL")
	}
}

func TestRenameSession_Callbacks(t *testing.T) {
	cfg := &config.Config{
		RenameMaxBatch:   10,
		RenameSessionTTL: 10 * time.Minute,
	}
	mock := &mockSessionTG{}
	app := &App{
		cfg:            cfg,
		tg:             mock,
		renameSessions: NewRenameSessionManager(cfg, mock),
		auth:           auth.New(cfg),
	}
	defer app.Stop()

	chatID := int64(100)
	ownerID := int64(200)
	otherUser := int64(300)

	app.renameSessions.Start(chatID, ownerID, "{n:2} - {name}.{ext}", rename.Options{Start: 1, Step: 1}, false, 555)
	app.renameSessions.AddFile(chatID, ownerID, dl.TGFile{FileID: "1", Name: "apple.txt", Size: 10})
	app.renameSessions.AddFile(chatID, ownerID, dl.TGFile{FileID: "2", Name: "banana.txt", Size: 20})

	makeUpdate := func(fromID int64, data string) *models.Update {
		return &models.Update{
			CallbackQuery: &models.CallbackQuery{
				ID:   "cb1",
				From: models.User{ID: fromID},
				Data: data,
				Message: models.MaybeInaccessibleMessage{
					Message: &models.Message{
						ID:   555,
						Chat: models.Chat{ID: chatID},
					},
				},
			},
		}
	}

	ctx := context.Background()

	mock.answers = nil
	app.cbRename(ctx, nil, makeUpdate(otherUser, "rename:preview"))
	if len(mock.answers) == 0 || !strings.Contains(mock.answers[0], "belongs to another user") {
		t.Fatalf("expected other user rejected, got answers: %+v", mock.answers)
	}

	mock.sends = nil
	app.cbRename(ctx, nil, makeUpdate(ownerID, "rename:preview"))
	if len(mock.sends) == 0 || !strings.Contains(mock.sends[0], "Rename Preview") {
		t.Fatalf("expected preview table sent, got sends: %+v", mock.sends)
	}
	if !strings.Contains(mock.sends[0], "01 - apple.txt") {
		t.Errorf("preview missing apple.txt: %s", mock.sends[0])
	}
	if !strings.Contains(mock.sends[0], "02 - banana.txt") {
		t.Errorf("preview missing banana.txt: %s", mock.sends[0])
	}

	mock.edits = nil
	app.cbRename(ctx, nil, makeUpdate(ownerID, "rename:cancel"))
	if _, ok := app.renameSessions.Get(chatID, ownerID); ok {
		t.Error("expected session removed after cancel")
	}
	if len(mock.edits) == 0 || !strings.Contains(mock.edits[0], "Cancelled") {
		t.Errorf("expected cancelled edit, got edits: %+v", mock.edits)
	}
}
