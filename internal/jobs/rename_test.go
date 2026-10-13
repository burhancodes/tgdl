package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/rename"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

type mockRenameTG struct {
	tg.Client
	mu         sync.Mutex
	failFileID string
	downloaded []string
	uploaded   []string
	onDownload func(fileID string)
}

func (m *mockRenameTG) Download(ctx context.Context, f tg.FileRef, destDir string, cb tg.ProgressFunc) (string, error) {
	m.mu.Lock()
	if m.onDownload != nil {
		m.onDownload(f.FileID)
	}
	if m.failFileID != "" && f.FileID == m.failFileID {
		m.mu.Unlock()
		return "", errors.New("simulated download failure")
	}
	m.downloaded = append(m.downloaded, f.Name)
	m.mu.Unlock()

	_ = os.MkdirAll(destDir, 0o755)
	dest := filepath.Join(destDir, f.Name)
	if err := os.WriteFile(dest, []byte("data"), 0o644); err != nil {
		return "", err
	}
	if cb != nil {
		cb(f.Size, f.Size)
	}
	return dest, nil
}

func (m *mockRenameTG) Upload(ctx context.Context, chatID int64, u tg.Upload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploaded = append(m.uploaded, filepath.Base(u.Path))
	return nil
}

func (m *mockRenameTG) Send(ctx context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	return tg.MessageRef{ChatID: chatID, ID: 123}, nil
}

func (m *mockRenameTG) Edit(ctx context.Context, ref tg.MessageRef, html string, o tg.SendOpts) error {
	return nil
}

func (m *mockRenameTG) Delete(ctx context.Context, ref tg.MessageRef) error {
	return nil
}

func (m *mockRenameTG) Pin(ctx context.Context, ref tg.MessageRef) error {
	return nil
}

func (m *mockRenameTG) Unpin(ctx context.Context, ref tg.MessageRef) error {
	return nil
}

func setupRenameManager(t *testing.T, mock *mockRenameTG) (*Manager, *store.Store, *config.Config, string) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DataDir:          tmpDir,
		MinFreeDiskBytes: 1024,
		TGBatchSize:      100,
	}
	st, err := store.Open(context.Background(), filepath.Join(tmpDir, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	lim := pacing.NewTelegramLimiter()
	lim.SetGaps(0, -1)
	d := &Deps{
		Cfg:   cfg,
		Store: st,
		TG:    mock,
	}
	m := NewManager(d, lim)
	return m, st, cfg, tmpDir
}

func TestRenameJob_BatchSuccess(t *testing.T) {
	mock := &mockRenameTG{}
	m, st, _, _ := setupRenameManager(t, mock)
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	files := []dl.TGFile{
		{FileID: "1", Name: "doc1.txt", Size: 4},
		{FileID: "2", Name: "doc2.txt", Size: 4},
		{FileID: "3", Name: "doc3.txt", Size: 4},
		{FileID: "4", Name: "doc4.txt", Size: 4},
		{FileID: "5", Name: "doc5.txt", Size: 4},
	}
	args := dl.Args{
		RenameScheme: "{n:2} - {name}.{ext}",
		RenameFiles:  files,
	}
	argsJSON, _ := json.Marshal(args)

	job, err := st.CreateJob(ctx, 100, "rename:batch", false, string(argsJSON))
	if err != nil {
		t.Fatal(err)
	}

	m.run(job.ID)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	expected := []string{
		"01 - doc1.txt",
		"02 - doc2.txt",
		"03 - doc3.txt",
		"04 - doc4.txt",
		"05 - doc5.txt",
	}

	if len(mock.uploaded) != len(expected) {
		t.Fatalf("expected %d uploads, got %d: %+v", len(expected), len(mock.uploaded), mock.uploaded)
	}
	for i, want := range expected {
		if mock.uploaded[i] != want {
			t.Errorf("file %d mismatch: got %s, want %s", i, mock.uploaded[i], want)
		}
	}

	updatedJob, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedJob.Status != store.StatusDone {
		t.Errorf("expected StatusDone, got %s", updatedJob.Status)
	}
	if updatedJob.SentFiles != 5 {
		t.Errorf("expected 5 sent files, got %d", updatedJob.SentFiles)
	}
}

func TestRenameJob_ThirdFileFailsAndSkipped(t *testing.T) {
	mock := &mockRenameTG{failFileID: "3"}
	m, st, _, _ := setupRenameManager(t, mock)
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	files := []dl.TGFile{
		{FileID: "1", Name: "doc1.txt", Size: 4},
		{FileID: "2", Name: "doc2.txt", Size: 4},
		{FileID: "3", Name: "doc3.txt", Size: 4},
		{FileID: "4", Name: "doc4.txt", Size: 4},
		{FileID: "5", Name: "doc5.txt", Size: 4},
	}
	args := dl.Args{
		RenameScheme: "{n:2} - {name}.{ext}",
		RenameFiles:  files,
	}
	argsJSON, _ := json.Marshal(args)

	job, err := st.CreateJob(ctx, 100, "rename:batch", false, string(argsJSON))
	if err != nil {
		t.Fatal(err)
	}

	m.run(job.ID)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	expected := []string{
		"01 - doc1.txt",
		"02 - doc2.txt",
		"04 - doc4.txt",
		"05 - doc5.txt",
	}

	if len(mock.uploaded) != len(expected) {
		t.Fatalf("expected %d uploads, got %d: %+v", len(expected), len(mock.uploaded), mock.uploaded)
	}
	for i, want := range expected {
		if mock.uploaded[i] != want {
			t.Errorf("file %d mismatch: got %s, want %s", i, mock.uploaded[i], want)
		}
	}

	updatedJob, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedJob.Status != store.StatusDone {
		t.Errorf("expected StatusDone, got %s", updatedJob.Status)
	}
	if updatedJob.SentFiles != 4 {
		t.Errorf("expected 4 sent files, got %d", updatedJob.SentFiles)
	}
	if updatedJob.SkippedFiles != 1 {
		t.Errorf("expected 1 skipped file, got %d", updatedJob.SkippedFiles)
	}
}

func TestRenameJob_CancelMidBatchCleansDest(t *testing.T) {
	var cancel context.CancelFunc
	var m *Manager
	mock := &mockRenameTG{}
	mock.onDownload = func(fileID string) {
		if fileID == "2" && cancel != nil {
			cancel()
		}
	}

	var st *store.Store
	var cfg *config.Config
	m, st, cfg, _ = setupRenameManager(t, mock)
	defer st.Close()

	ctx, c := context.WithCancel(context.Background())
	cancel = c
	defer cancel()

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	files := []dl.TGFile{
		{FileID: "1", Name: "doc1.txt", Size: 4},
		{FileID: "2", Name: "doc2.txt", Size: 4},
		{FileID: "3", Name: "doc3.txt", Size: 4},
	}
	args := dl.Args{
		RenameScheme: "{n:2} - {name}.{ext}",
		RenameFiles:  files,
	}
	argsJSON, _ := json.Marshal(args)

	job, err := st.CreateJob(ctx, 100, "rename:batch", false, string(argsJSON))
	if err != nil {
		t.Fatal(err)
	}

	m.run(job.ID)

	destPath := filepath.Join(cfg.DownloadsDir(), job.DownloadDir())
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Errorf("expected Dest directory to be removed on cancel, but it exists: %s", destPath)
	}
}

func TestRenameJob_ResumeReusesPlan(t *testing.T) {
	mock := &mockRenameTG{}
	m, st, _, _ := setupRenameManager(t, mock)
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	files := []dl.TGFile{
		{FileID: "1", Name: "doc1.txt", Size: 4},
		{FileID: "2", Name: "doc2.txt", Size: 4},
	}
	args := dl.Args{
		RenameScheme: "{name}_{rand:4}.{ext}",
		RenameFiles:  files,
	}
	argsJSON, _ := json.Marshal(args)

	job, err := st.CreateJob(ctx, 100, "rename:batch", false, string(argsJSON))
	if err != nil {
		t.Fatal(err)
	}

	parsed, _ := rename.Parse("{name}_{rand:4}.{ext}")
	plan, _ := parsed.Plan([]rename.Input{
		{OriginalName: "doc1.txt", Size: 4},
		{OriginalName: "doc2.txt", Size: 4},
	}, rename.Options{})

	type planEntry struct {
		FileID  string `json:"file_id"`
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	storedEntries := []planEntry{
		{FileID: "1", OldName: "doc1.txt", NewName: "custom_fixed_1.txt"},
		{FileID: "2", OldName: "doc2.txt", NewName: "custom_fixed_2.txt"},
	}
	storedJSON, _ := json.Marshal(storedEntries)
	if err := st.SaveRenamePlan(ctx, job.ID, string(storedJSON)); err != nil {
		t.Fatal(err)
	}

	_ = plan

	m.run(job.ID)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.uploaded) != 2 {
		t.Fatalf("expected 2 uploads, got %d", len(mock.uploaded))
	}
	if mock.uploaded[0] != "custom_fixed_1.txt" {
		t.Errorf("file 0 mismatch: got %s, want custom_fixed_1.txt", mock.uploaded[0])
	}
	if mock.uploaded[1] != "custom_fixed_2.txt" {
		t.Errorf("file 1 mismatch: got %s, want custom_fixed_2.txt", mock.uploaded[1])
	}
}
