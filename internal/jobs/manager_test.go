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
		deadJob.DownloadDir() + "_patch_work",
		deadJob.MirrorTempDirName(),
		"tmp_up_abc",
		"tmp_conf_xyz",
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

	// Register tmp_up_active with activeTmp
	release := m.RegisterTempDir(filepath.Join(downloads, "tmp_up_active"))

	keep := []*store.Job{liveJob}

	m.cleanupOrphans(keep)

	// Check which should exist
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
		deadJob.DownloadDir() + "_patch_work",
		deadJob.MirrorTempDirName(),
		"tmp_up_abc",
		"tmp_conf_xyz",
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

	// Release active temp dir and assert it is removed on next cleanupOrphans
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
	nextID int
}

func (c *mockPinTGClient) Send(ctx context.Context, chatID int64, text string, o tg.SendOpts) (tg.MessageRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return tg.MessageRef{ChatID: chatID, ID: c.nextID}, nil
}

func (c *mockPinTGClient) Edit(ctx context.Context, ref tg.MessageRef, text string, o tg.SendOpts) error {
	return nil
}

func (c *mockPinTGClient) Pin(ctx context.Context, ref tg.MessageRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pins = append(c.pins, ref)
	return c.pinErr
}

func (c *mockPinTGClient) Unpin(ctx context.Context, ref tg.MessageRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unpins = append(c.unpins, ref)
	return nil
}

func (c *mockPinTGClient) Delete(ctx context.Context, ref tg.MessageRef) error {
	return nil
}

func TestStatusMessage_PinningAndUnpinning(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		DataDir:          tmpDir,
		PinStatusMessage: true,
	}

	mockTG := &mockPinTGClient{}
	lim := pacing.NewTelegramLimiter()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	m := &Manager{
		d:      &Deps{Cfg: cfg, Store: st, TG: mockTG},
		lim:    lim,
		root:   rootCtx,
		states: map[string]*State{},
	}

	// 1. New job -> exactly one Pin call
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

	// 2. Resuming an existing job (job.StatusMsgID != 0) -> no Pin call
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

	// 3. Job finishing normally -> exactly one Unpin call
	m.finalize(st1)
	if len(mockTG.unpins) != 1 {
		t.Fatalf("expected exactly 1 Unpin call for finished job, got %d", len(mockTG.unpins))
	}
	if mockTG.unpins[0].ChatID != 111 || mockTG.unpins[0].ID != st1.msg.ID {
		t.Errorf("unpin ref mismatch: got %+v, want ChatID=111, ID=%d", mockTG.unpins[0], st1.msg.ID)
	}

	// 4. Interrupted job by shutdown (rootCtx cancelled) -> no Unpin call
	mockTG.unpins = nil
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	shutdownCancel() // cancel root to simulate shutdown
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

	// 5. Pin returning an error does not stop or panic the job
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
