package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/upload"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type mockMirrorTG struct {
	tg.Client
	sends []string
}

func (m *mockMirrorTG) Send(_ context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	m.sends = append(m.sends, html)
	return tg.MessageRef{ChatID: chatID, ID: 100}, nil
}

func setupMirrorTestApp(t *testing.T) (*App, *mockMirrorTG, *store.Store) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := &config.Config{
		DataDir:       tempDir,
		AuthorizedIDs: []int64{100},
		MirrorHosts:   "gofile,fileditch,pixeldrain",
		MirrorRetries: 3,
	}
	dbPath := filepath.Join(tempDir, "test.db")
	_ = os.MkdirAll(cfg.DownloadsDir(), 0o755)
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mockTG := &mockMirrorTG{}
	keys := upload.NewKeys(cfg)
	hosts := upload.NewHosts(cfg, keys)

	app := &App{
		cfg:   cfg,
		auth:  auth.New(cfg),
		store: st,
		tg:    mockTG,
		keys:  keys,
		hosts: hosts,
	}
	return app, mockTG, st
}

func makeMsgUpdate(text string, reply *models.Message, doc *models.Document, video *models.Video) *models.Update {
	msg := &models.Message{
		ID:             1,
		Chat:           models.Chat{ID: 100},
		From:           &models.User{ID: 100},
		Text:           text,
		ReplyToMessage: reply,
		Document:       doc,
		Video:          video,
	}
	return &models.Update{Message: msg}
}

func TestMirrorCommand_Selection(t *testing.T) {
	ctx := context.Background()

	t.Run("Mirror_All", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/m https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo(nil)(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %v (err: %v)", len(jobs), err)
		}
		if jobs[0].URL != "mirror:https://example.com/test.mp4" {
			t.Errorf("expected target mirror:https://example.com/test.mp4, got %s", jobs[0].URL)
		}
		args := dl.ParseArgs(jobs[0].Args)
		if !args.IsMirror {
			t.Errorf("expected IsMirror true")
		}
		if len(args.MirrorHosts) != 0 {
			t.Errorf("expected empty MirrorHosts for /m (means all), got %v", args.MirrorHosts)
		}
	})

	t.Run("Mirror_GoFile", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/gofile https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo([]upload.HostID{upload.GoFile})(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %v (err: %v)", len(jobs), err)
		}
		args := dl.ParseArgs(jobs[0].Args)
		if len(args.MirrorHosts) != 1 || args.MirrorHosts[0] != "gofile" {
			t.Errorf("expected MirrorHosts [gofile], got %v", args.MirrorHosts)
		}
	})

	t.Run("Mirror_Pixeldrain", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/pd https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo([]upload.HostID{upload.Pixeldrain})(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %v (err: %v)", len(jobs), err)
		}
		args := dl.ParseArgs(jobs[0].Args)
		if len(args.MirrorHosts) != 1 || args.MirrorHosts[0] != "pixeldrain" {
			t.Errorf("expected MirrorHosts [pixeldrain], got %v", args.MirrorHosts)
		}
	})

	t.Run("Mirror_To_Specific", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/m -to gofile,pd https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo(nil)(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %v (err: %v)", len(jobs), err)
		}
		args := dl.ParseArgs(jobs[0].Args)
		if len(args.MirrorHosts) != 2 || args.MirrorHosts[0] != "gofile" || args.MirrorHosts[1] != "pixeldrain" {
			t.Errorf("expected MirrorHosts [gofile pixeldrain], got %v", args.MirrorHosts)
		}
	})

	t.Run("Mirror_To_Unknown", func(t *testing.T) {
		app, mockTG, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/m -to unknown https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo(nil)(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("expected 0 jobs for unknown host, got %d", len(jobs))
		}
		if len(mockMirrorTG_Sends(mockTG)) == 0 || !strings.Contains(mockTG.sends[0], "unknown mirror host") {
			t.Errorf("expected error message mentioning unknown mirror host, got: %v", mockTG.sends)
		}
	})

	t.Run("Mirror_To_RejectedOnSpecific", func(t *testing.T) {
		app, mockTG, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/gofile -to pd https://example.com/test.mp4", nil, nil, nil)
		app.cmdMirrorTo([]upload.HostID{upload.GoFile})(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 0 {
			t.Fatalf("expected 0 jobs, got %d", len(jobs))
		}
		if len(mockMirrorTG_Sends(mockTG)) == 0 || !strings.Contains(mockTG.sends[0], "-to") {
			t.Errorf("expected -to rejection message, got: %v", mockTG.sends)
		}
	})
}

func mockMirrorTG_Sends(m *mockMirrorTG) []string {
	return m.sends
}

func TestMirrorCommand_MultipleURLs(t *testing.T) {
	ctx := context.Background()
	app, _, st := setupMirrorTestApp(t)
	u := makeMsgUpdate("/m https://example.com/1.mp4 https://example.com/2.mp4", nil, nil, nil)
	app.cmdMirrorTo(nil)(ctx, nil, u)

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected 1 job for multiple URLs, got %d", len(jobs))
	}
	expectedTarget := `mirror:["https://example.com/1.mp4","https://example.com/2.mp4"]`
	if jobs[0].URL != expectedTarget {
		t.Errorf("expected target %q, got %q", expectedTarget, jobs[0].URL)
	}
}

func TestMirrorCommand_RepliedTextFile(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getMe") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":12345,"is_bot":true,"first_name":"test"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"txt1","file_path":"test.txt"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "test.txt") {
			_, _ = w.Write([]byte("https://example.com/url1.mp4\nhttps://example.com/url2.mp4\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, err := tgbot.New("12345:TESTTOKEN", tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	api := tg.NewBotAPI(b, "12345:TESTTOKEN")
	api.BaseURL = srv.URL

	app, _, st := setupMirrorTestApp(t)
	app.api = api

	content := "https://example.com/url1.mp4\nhttps://example.com/url2.mp4\n"
	reply := &models.Message{
		ID: 50,
		Document: &models.Document{
			FileID:   "txt1",
			FileName: "links.txt",
			FileSize: int64(len(content)),
		},
	}
	u := makeMsgUpdate("/m", reply, nil, nil)
	app.cmdMirrorTo(nil)(ctx, nil, u)

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected 1 job for replied text file, got %d (err: %v)", len(jobs), err)
	}
	expectedTarget := `mirror:["https://example.com/url1.mp4","https://example.com/url2.mp4"]`
	if jobs[0].URL != expectedTarget {
		t.Errorf("expected target %q, got %q", expectedTarget, jobs[0].URL)
	}
}

func TestMirrorCommand_TelegramFileInput(t *testing.T) {
	ctx := context.Background()
	app, _, st := setupMirrorTestApp(t)

	reply := &models.Message{
		ID: 50,
		Video: &models.Video{
			FileID:   "video123",
			FileName: "myvideo.mp4",
			FileSize: 1024,
		},
	}
	u := makeMsgUpdate("/m", reply, nil, nil)
	app.cmdMirrorTo(nil)(ctx, nil, u)

	jobs, err := st.ActiveJobsForChat(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].URL != "mirror_tg:myvideo.mp4" {
		t.Errorf("expected target mirror_tg:myvideo.mp4, got %s", jobs[0].URL)
	}
	args := dl.ParseArgs(jobs[0].Args)
	if args.TGFile == nil || args.TGFile.FileID != "video123" {
		t.Errorf("expected args.TGFile with FileID video123, got: %+v", args.TGFile)
	}
}

func TestMirrorCommand_GofileDownloader(t *testing.T) {
	ctx := context.Background()

	t.Run("Gofile_Download", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/gofile https://gofile.io/d/abc123", nil, nil, nil)
		app.cmdMirrorTo([]upload.HostID{upload.GoFile})(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %d", len(jobs))
		}
		if jobs[0].URL != "gofile:https://gofile.io/d/abc123" {
			t.Errorf("expected target gofile:https://gofile.io/d/abc123, got %s", jobs[0].URL)
		}
		args := dl.ParseArgs(jobs[0].Args)
		if args.IsMirror {
			t.Errorf("expected IsMirror false for download")
		}
	})

	t.Run("Gfdl_Download", func(t *testing.T) {
		app, _, st := setupMirrorTestApp(t)
		u := makeMsgUpdate("/gfdl https://gofile.io/d/def456", nil, nil, nil)
		app.cmdGofileDL(ctx, nil, u)

		jobs, err := st.ActiveJobsForChat(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %d", len(jobs))
		}
		if jobs[0].URL != "gofile:https://gofile.io/d/def456" {
			t.Errorf("expected target gofile:https://gofile.io/d/def456, got %s", jobs[0].URL)
		}
	})
}
