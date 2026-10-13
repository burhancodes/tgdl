package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

func TestCleanupOrphans(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DataDir: tmpDir,
	}

	m := &Manager{
		d:      &Deps{Cfg: cfg},
		states: map[string]*State{},
	}

	downloads := cfg.DownloadsDir()
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}

	liveJob := &store.Job{ID: "1"}
	deadJob := &store.Job{ID: "2"}

	dirsToCreate := []string{
		liveJob.DownloadDir(),
		liveJob.DownloadDir() + "_extract",
		liveJob.MirrorTempDirName(),
		deadJob.DownloadDir(),
		deadJob.DownloadDir() + "_extract",
		deadJob.MirrorTempDirName(),
		"tmp_up_abc",
		"tmp_up_active",
		"important_folder",
	}

	oldTime := time.Now().Add(-7 * time.Hour)
	for _, d := range dirsToCreate {
		p := filepath.Join(downloads, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, oldTime, oldTime)
	}

	release := m.RegisterTempDir(filepath.Join(downloads, "tmp_up_active"))

	keep := []*store.Job{liveJob}

	m.cleanupOrphans(keep)

	shouldExist := map[string]bool{
		liveJob.DownloadDir():              true,
		liveJob.DownloadDir() + "_extract": true,
		liveJob.MirrorTempDirName():        true,
		"tmp_up_active":                    true,
		"important_folder":                 true,
	}

	shouldNotExist := []string{
		deadJob.DownloadDir(),
		deadJob.DownloadDir() + "_extract",
		deadJob.MirrorTempDirName(),
		"tmp_up_abc",
	}

	for d := range shouldExist {
		p := filepath.Join(downloads, d)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Errorf("expected %s to exist, but was deleted", d)
		}
	}

	for _, d := range shouldNotExist {
		p := filepath.Join(downloads, d)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("expected %s to be deleted, but still exists", d)
		}
	}

	release()
	m.cleanupOrphans(keep)
	activePath := filepath.Join(downloads, "tmp_up_active")
	if _, err := os.Stat(activePath); err == nil {
		t.Errorf("expected tmp_up_active to be deleted after release, but still exists")
	}
}

type mockPinTGClient struct {
	tg.Client
	mu     sync.Mutex
	pins   []tg.MessageRef
	unpins []tg.MessageRef
	pinErr error
}

func (m *mockPinTGClient) Send(_ context.Context, chatID int64, _ string, _ tg.SendOpts) (tg.MessageRef, error) {
	return tg.MessageRef{ChatID: chatID, ID: 555}, nil
}

func (m *mockPinTGClient) Edit(_ context.Context, ref tg.MessageRef, _ string, _ tg.SendOpts) error {
	return nil
}

func (m *mockPinTGClient) Pin(_ context.Context, ref tg.MessageRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pins = append(m.pins, ref)
	if m.pinErr != nil {
		return m.pinErr
	}
	return nil
}

func (m *mockPinTGClient) Unpin(_ context.Context, ref tg.MessageRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unpins = append(m.unpins, ref)
	return nil
}

func TestManager_StatusMessagePinning(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DataDir:          tmpDir,
		PinStatusMessage: true,
		ExtractThreads:   1,
		ArchiveThreads:   1,
		MinFreeDiskBytes: 1024,
	}

	st, err := store.Open(context.Background(), filepath.Join(tmpDir, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mockTG := &mockPinTGClient{}
	lim := pacing.NewTelegramLimiter()

	ctx := context.Background()
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	m := &Manager{
		d:      &Deps{Cfg: cfg, Store: st, TG: mockTG},
		lim:    lim,
		root:   rootCtx,
		states: map[string]*State{},
	}

	job1, err := st.CreateJob(ctx, 111, "https://example.com/file1.zip", false, "")
	if err != nil {
		t.Fatal(err)
	}
	st1 := newState(job1, filepath.Join(tmpDir, "d1"), filepath.Join(tmpDir, "e1"), rootCtx)
	m.ensureStatusMessage(st1)

	if len(mockTG.pins) != 1 {
		t.Fatalf("expected exactly 1 Pin call for new job, got %d", len(mockTG.pins))
	}
	if mockTG.pins[0].ChatID != 111 || mockTG.pins[0].ID != st1.msg.ID {
		t.Errorf("pin ref mismatch: got %+v, want ChatID=111, ID=%d", mockTG.pins[0], st1.msg.ID)
	}

	mockTG.pins = nil
	job2, err := st.CreateJob(ctx, 222, "https://example.com/file2.zip", false, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetStatusMessage(ctx, job2.ID, 999)
	job2, _ = st.GetJob(ctx, job2.ID)
	st2 := newState(job2, filepath.Join(tmpDir, "d2"), filepath.Join(tmpDir, "e2"), rootCtx)
	m.ensureStatusMessage(st2)

	if len(mockTG.pins) != 0 {
		t.Fatalf("expected 0 Pin calls when resuming a job, got %d", len(mockTG.pins))
	}

	m.finalize(st1)
	if len(mockTG.unpins) != 1 {
		t.Fatalf("expected exactly 1 Unpin call for finished job, got %d", len(mockTG.unpins))
	}
	if mockTG.unpins[0].ChatID != 111 || mockTG.unpins[0].ID != st1.msg.ID {
		t.Errorf("unpin ref mismatch: got %+v, want ChatID=111, ID=%d", mockTG.unpins[0], st1.msg.ID)
	}

	mockTG.unpins = nil
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	shutdownCancel()
	mShutdown := &Manager{
		d:      &Deps{Cfg: cfg, Store: st, TG: mockTG},
		lim:    lim,
		root:   shutdownCtx,
		states: map[string]*State{},
	}
	job3, _ := st.CreateJob(ctx, 333, "https://example.com/file3.zip", false, "")
	st3 := newState(job3, filepath.Join(tmpDir, "d3"), filepath.Join(tmpDir, "e3"), shutdownCtx)
	st3.msg = tg.MessageRef{ChatID: 333, ID: 777}
	mShutdown.finalize(st3)

	if len(mockTG.unpins) != 0 {
		t.Fatalf("expected 0 Unpin calls for shutdown interrupted job, got %d", len(mockTG.unpins))
	}

	mockTG.pins = nil
	mockTG.pinErr = errors.New("chat_admin_required: can_pin_messages right missing")
	job4, _ := st.CreateJob(ctx, 444, "https://example.com/file4.zip", false, "")
	st4 := newState(job4, filepath.Join(tmpDir, "d4"), filepath.Join(tmpDir, "e4"), rootCtx)
	m.ensureStatusMessage(st4)

	if len(mockTG.pins) != 1 {
		t.Fatalf("expected 1 Pin attempt even when Pin fails, got %d", len(mockTG.pins))
	}
	if !st4.msg.Valid() {
		t.Fatalf("expected valid status msg ref despite Pin error, got %+v", st4.msg)
	}
}
