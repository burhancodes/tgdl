package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/go-telegram/bot/models"
)

type mockSessionTG struct {
	tg.Client
	edits   []string
	answers []string
	sends   []string
	lastKB  tg.Keyboard
}

func (m *mockSessionTG) Edit(_ context.Context, ref tg.MessageRef, html string, o tg.SendOpts) error {
	m.edits = append(m.edits, html)
	return nil
}

func (m *mockSessionTG) Send(_ context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	m.sends = append(m.sends, html)
	m.lastKB = o.Keyboard
	return tg.MessageRef{ChatID: chatID, ID: 123}, nil
}

func (m *mockSessionTG) Answer(_ context.Context, callbackID, text string, alert bool) error {
	m.answers = append(m.answers, text)
	return nil
}

func setupTestApp(t *testing.T, ttl time.Duration) (*App, *mockSessionTG, *store.Store) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	cfg := &config.Config{
		RenameMaxBatch:   10,
		RenameSessionTTL: ttl,
		AuthorizedIDs:    []int64{200},
	}
	mock := &mockSessionTG{}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	app := &App{
		cfg:            cfg,
		store:          st,
		tg:             mock,
		renameSessions: NewRenameSessionManager(cfg, mock),
		auth:           auth.New(cfg),
	}
	return app, mock, st
}

func TestRename_SingleInline(t *testing.T) {
	app, _, st := setupTestApp(t, 0)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	upd := &models.Update{
		Message: &models.Message{
			ID:   10,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Document: &models.Document{
				FileID:   "tg-file-1",
				FileName: "Trix_The_Cold_Sato_san_is_Only_Sweet_to_Me_S01E01_WEBRip_1080p_AV1.mkv",
				FileSize: 1000,
			},
			Caption: "/rename The Cold Sato-san S01E01",
		},
	}
	app.cmdRename(ctx, nil, upd)

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveJobsForChat: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	var args dl.Args
	if err := json.Unmarshal([]byte(jobs[0].Args), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if len(args.RenameNames) != 1 || args.RenameNames[0] != "The Cold Sato-san S01E01.mkv" {
		t.Errorf("expected rename name 'The Cold Sato-san S01E01.mkv', got %v", args.RenameNames)
	}
}

func TestRename_SinglePrompted(t *testing.T) {
	app, mock, st := setupTestApp(t, 0)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	doc := &models.Document{
		FileID:   "tg-file-prompt",
		FileName: "Original_File.mkv",
		FileSize: 2000,
	}

	updPrompt := &models.Update{
		Message: &models.Message{
			ID:       11,
			Chat:     models.Chat{ID: 100},
			From:     &models.User{ID: 200},
			Document: doc,
			Caption:  "/rename",
		},
	}
	app.cmdRename(ctx, nil, updPrompt)

	if len(mock.sends) == 0 || !strings.Contains(mock.sends[len(mock.sends)-1], "Send the new name") {
		t.Fatalf("expected prompt 'Send the new name', got: %+v", mock.sends)
	}

	updOther := &models.Update{
		Message: &models.Message{
			ID:   12,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 999},
			Text: "Another User Name",
		},
	}
	app.Default(ctx, nil, updOther)

	jobs, _ := st.ActiveJobsForChat(ctx, 100)
	if len(jobs) != 0 {
		t.Fatalf("another user should not be able to answer prompt")
	}

	updUser := &models.Update{
		Message: &models.Message{
			ID:   13,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "Verbatim Name Here",
		},
	}
	app.Default(ctx, nil, updUser)

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveJobsForChat: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after prompt answer, got %d", len(jobs))
	}
	var args dl.Args
	if err := json.Unmarshal([]byte(jobs[0].Args), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if len(args.RenameNames) != 1 || args.RenameNames[0] != "Verbatim Name Here.mkv" {
		t.Errorf("expected verbatim name 'Verbatim Name Here.mkv', got %v", args.RenameNames)
	}
}

func TestRename_BatchFlow(t *testing.T) {
	app, mock, st := setupTestApp(t, 0)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	updStart := &models.Update{
		Message: &models.Message{
			ID:   20,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "/rename batch",
		},
	}
	app.cmdRename(ctx, nil, updStart)

	sess, ok := app.renameSessions.Get(100, 200)
	if !ok || sess == nil {
		t.Fatal("expected batch session to start")
	}

	for i := 1; i <= 3; i++ {
		updFile := &models.Update{
			Message: &models.Message{
				ID:   20 + i,
				Chat: models.Chat{ID: 100},
				From: &models.User{ID: 200},
				Document: &models.Document{
					FileID:   fmt.Sprintf("f-%d", i),
					FileName: fmt.Sprintf("Episode_%d_raw.mkv", i),
					FileSize: 5000,
				},
			},
		}
		app.Default(ctx, nil, updFile)
	}

	sess, _ = app.renameSessions.Get(100, 200)
	if len(sess.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(sess.Files))
	}

	mock.sends = nil
	updMismatch := &models.Update{
		Message: &models.Message{
			ID:   30,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "Line 1\nLine 2",
		},
	}
	app.Default(ctx, nil, updMismatch)
	if len(mock.sends) == 0 || !strings.Contains(mock.sends[len(mock.sends)-1], "Line count mismatch") {
		t.Fatalf("expected mismatch notice, got: %+v", mock.sends)
	}

	mock.sends = nil
	updNames := &models.Update{
		Message: &models.Message{
			ID:   31,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "Show S01E01\nShow S01E02\nShow S01E03",
		},
	}
	app.Default(ctx, nil, updNames)
	if len(mock.sends) == 0 || !strings.Contains(mock.sends[len(mock.sends)-1], "Rename Preview") {
		t.Fatalf("expected preview message, got: %+v", mock.sends)
	}
	previewText := mock.sends[len(mock.sends)-1]
	if !strings.Contains(previewText, "Show S01E01.mkv") || !strings.Contains(previewText, "Show S01E02.mkv") || !strings.Contains(previewText, "Show S01E03.mkv") {
		t.Errorf("preview missing expected names: %s", previewText)
	}

	applyQuery := &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb-apply",
			From: models.User{ID: 200},
			Data: "rename:apply",
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   123,
					Chat: models.Chat{ID: 100},
				},
			},
		},
	}
	app.cbRename(ctx, nil, applyQuery)

	if _, ok := app.renameSessions.Get(100, 200); ok {
		t.Error("expected session to be removed after apply")
	}

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveJobsForChat: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after batch apply, got %d", len(jobs))
	}
	var args dl.Args
	if err := json.Unmarshal([]byte(jobs[0].Args), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	expected := []string{"Show S01E01.mkv", "Show S01E02.mkv", "Show S01E03.mkv"}
	if len(args.RenameNames) != 3 {
		t.Fatalf("expected 3 rename names, got %d", len(args.RenameNames))
	}
	for i, want := range expected {
		if args.RenameNames[i] != want {
			t.Errorf("item %d: got %s, want %s", i, args.RenameNames[i], want)
		}
	}
}

func TestRename_Cancel(t *testing.T) {
	app, mock, st := setupTestApp(t, 0)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	updStart := &models.Update{
		Message: &models.Message{
			ID:   40,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "/rename batch",
		},
	}
	app.cmdRename(ctx, nil, updStart)

	cancelQuery := &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb-cancel",
			From: models.User{ID: 200},
			Data: "rename:cancel",
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   123,
					Chat: models.Chat{ID: 100},
				},
			},
		},
	}
	app.cbRename(ctx, nil, cancelQuery)

	if _, ok := app.renameSessions.Get(100, 200); ok {
		t.Error("expected batch session removed after cancel")
	}
	if len(mock.edits) == 0 || !strings.Contains(mock.edits[len(mock.edits)-1], "Cancelled") {
		t.Errorf("expected cancelled message edit, got: %+v", mock.edits)
	}
}

func TestRename_TTLExpiry(t *testing.T) {
	app, _, st := setupTestApp(t, 30*time.Millisecond)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	updStart := &models.Update{
		Message: &models.Message{
			ID:   50,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "/rename batch",
		},
	}
	app.cmdRename(ctx, nil, updStart)

	time.Sleep(50 * time.Millisecond)
	if _, ok := app.renameSessions.Get(100, 200); ok {
		t.Error("expected batch session to expire after TTL")
	}

	app.renameSessions.SetPendingSingle(100, 200, dl.TGFile{FileID: "1", Name: "test.mkv"}, false)
	time.Sleep(50 * time.Millisecond)
	if _, ok := app.renameSessions.GetPendingSingle(100, 200); ok {
		t.Error("expected pending single rename to expire after TTL")
	}
}

func TestRename_BatchDuplicates(t *testing.T) {
	app, mock, st := setupTestApp(t, 0)
	defer app.Stop()
	defer st.Close()

	ctx := context.Background()
	updStart := &models.Update{
		Message: &models.Message{
			ID:   60,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "/rename batch",
		},
	}
	app.cmdRename(ctx, nil, updStart)

	for i := 1; i <= 2; i++ {
		updFile := &models.Update{
			Message: &models.Message{
				ID:   60 + i,
				Chat: models.Chat{ID: 100},
				From: &models.User{ID: 200},
				Document: &models.Document{
					FileID:   fmt.Sprintf("dup-%d", i),
					FileName: fmt.Sprintf("file_%d.mkv", i),
					FileSize: 100,
				},
			},
		}
		app.Default(ctx, nil, updFile)
	}

	mock.sends = nil
	updNames := &models.Update{
		Message: &models.Message{
			ID:   70,
			Chat: models.Chat{ID: 100},
			From: &models.User{ID: 200},
			Text: "Same Title\nSame Title",
		},
	}
	app.Default(ctx, nil, updNames)
	if len(mock.sends) == 0 {
		t.Fatal("expected preview message")
	}
	preview := mock.sends[len(mock.sends)-1]
	if !strings.Contains(preview, "Same Title.mkv") || !strings.Contains(preview, "Same Title (2).mkv") {
		t.Errorf("expected duplicate suffix in preview: %s", preview)
	}
}
