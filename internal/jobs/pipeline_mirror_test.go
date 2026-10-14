package jobs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/upload"
)

type mockPipelineTG struct {
	tg.Client
	mu    sync.Mutex
	sends []string
}

func (m *mockPipelineTG) Send(_ context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	m.mu.Lock()
	m.sends = append(m.sends, html)
	m.mu.Unlock()
	return tg.MessageRef{ChatID: chatID, ID: 100}, nil
}

func (m *mockPipelineTG) Edit(_ context.Context, ref tg.MessageRef, html string, o tg.SendOpts) error {
	return nil
}

func (m *mockPipelineTG) Delete(_ context.Context, ref tg.MessageRef) error {
	return nil
}

func (m *mockPipelineTG) Pin(_ context.Context, ref tg.MessageRef) error {
	return nil
}

func (m *mockPipelineTG) Unpin(_ context.Context, ref tg.MessageRef) error {
	return nil
}

func setupTestPipelineManager(t *testing.T, cfgHosts string) (*Manager, *upload.Hosts, *mockPipelineTG, *store.Store, *config.Config) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := &config.Config{
		DataDir:          tempDir,
		MirrorHosts:      cfgHosts,
		MirrorRetries:    1,
		TGUploadDelayMin: 0,
		TGUploadDelayMax: 0,
	}
	_ = os.MkdirAll(cfg.DownloadsDir(), 0o755)

	dbPath := filepath.Join(tempDir, "state.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	keys := upload.NewKeys(cfg)
	hosts := upload.NewHosts(cfg, keys)
	hosts.SetClient(&http.Client{Timeout: 0})

	mockTG := &mockPipelineTG{}
	lim := pacing.NewTelegramLimiter()
	lim.SetGaps(0, 0)

	deps := &Deps{
		Cfg:   cfg,
		Store: st,
		TG:    mockTG,
		Keys:  keys,
		Hosts: hosts,
	}
	mgr := NewManager(deps, lim)
	return mgr, hosts, mockTG, st, cfg
}

func TestPipeline_SparseFile_PixeldrainSkip(t *testing.T) {
	mgr, hosts, _, _, cfg := setupTestPipelineManager(t, "gofile,fileditch,pixeldrain")

	srvGF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"downloadPage":"https://gofile.io/d/sparse123"}}`))
	}))
	defer srvGF.Close()

	srvFD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"url":"https://fileditch.com/sparse123"}`))
	}))
	defer srvFD.Close()

	hosts.SetEndpoint(upload.GoFile, srvGF.URL)
	hosts.SetEndpoint(upload.FileDitch, srvFD.URL)

	dest := filepath.Join(cfg.DownloadsDir(), "job_sparse")
	_ = os.MkdirAll(dest, 0o755)
	filePath := filepath.Join(dest, "huge_file.bin")

	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(11 << 30); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	job := &store.Job{
		ID:     "sparse_job",
		ChatID: 100,
		URL:    "mirror:http://example.com/huge_file.bin",
		Args:   `{"is_mirror": true}`,
	}
	st := newState(job, dest, dest+"_extract", context.Background())

	rec := mgr.mirrorFile(st, filePath)

	if rec.Links["gofile"] != "https://gofile.io/d/sparse123" {
		t.Errorf("expected GoFile link, got: %s", rec.Links["gofile"])
	}
	if rec.Links["fileditch"] != "https://fileditch.com/sparse123" {
		t.Errorf("expected FileDitch link, got: %s", rec.Links["fileditch"])
	}
	if rec.Links["pixeldrain"] != "" {
		t.Errorf("expected Pixeldrain to be skipped, but got link: %s", rec.Links["pixeldrain"])
	}
	pdErr := rec.Errors["pixeldrain"]
	if !strings.Contains(pdErr, "too large for Pixeldrain (10 GiB limit)") {
		t.Errorf("expected Pixeldrain limit error message, got: %q", pdErr)
	}
}

func TestPipeline_PartialFailure_JobDone(t *testing.T) {
	mgr, hosts, _, stStore, cfg := setupTestPipelineManager(t, "gofile,fileditch,pixeldrain")

	srvGF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"downloadPage":"https://gofile.io/d/ok123"}}`))
	}))
	defer srvGF.Close()

	srvFD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvFD.Close()

	srvPD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"id":"pd_ok"}`))
	}))
	defer srvPD.Close()

	hosts.SetEndpoint(upload.GoFile, srvGF.URL)
	hosts.SetEndpoint(upload.FileDitch, srvFD.URL)
	hosts.SetEndpoint(upload.Pixeldrain, srvPD.URL)

	dest := filepath.Join(cfg.DownloadsDir(), "job_partial")
	_ = os.MkdirAll(dest, 0o755)
	filePath := filepath.Join(dest, "part_file.txt")
	if err := os.WriteFile(filePath, []byte("hello mirror partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	job, err := stStore.CreateJob(context.Background(), 100, "mirror:http://example.com/part_file.txt", true, `{"is_mirror": true}`)
	if err != nil {
		t.Fatal(err)
	}

	st := newState(job, dest, dest+"_extract", context.Background())
	mgr.processFile(st, dest, filePath, false)

	sent, skipped := st.counts()
	if sent != 1 {
		t.Errorf("expected sent=1 since GoFile and Pixeldrain succeeded, got %d", sent)
	}
	if skipped != 0 {
		t.Errorf("expected skipped=0 since at least one host succeeded, got %d", skipped)
	}

	mirrors := st.mirrorRecords()
	if len(mirrors) != 1 {
		t.Fatalf("expected 1 mirror record, got %d", len(mirrors))
	}
	m := mirrors[0]
	if m.Links["gofile"] != "https://gofile.io/d/ok123" {
		t.Errorf("expected GoFile link, got: %s", m.Links["gofile"])
	}
	if m.Links["pixeldrain"] != srvPD.URL+"/u/pd_ok" {
		t.Errorf("expected Pixeldrain link, got: %s", m.Links["pixeldrain"])
	}
	if m.Errors["fileditch"] == "" {
		t.Errorf("expected FileDitch error recorded")
	}

	summaryText := mgr.summary(st, sent, skipped)
	if !strings.Contains(summaryText, "https://gofile.io/d/ok123") {
		t.Errorf("summary missing GoFile link: %s", summaryText)
	}
	if !strings.Contains(summaryText, "pd_ok") {
		t.Errorf("summary missing Pixeldrain link: %s", summaryText)
	}
	if !strings.Contains(summaryText, "FileDitch") || !strings.Contains(summaryText, "500") {
		t.Errorf("summary missing FileDitch failure reason: %s", summaryText)
	}
}

func TestPipeline_AllHostsFail_JobSkipped(t *testing.T) {
	mgr, hosts, _, stStore, cfg := setupTestPipelineManager(t, "gofile,fileditch")

	srvGF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srvGF.Close()

	srvFD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srvFD.Close()

	hosts.SetEndpoint(upload.GoFile, srvGF.URL)
	hosts.SetEndpoint(upload.FileDitch, srvFD.URL)

	dest := filepath.Join(cfg.DownloadsDir(), "job_allfail")
	_ = os.MkdirAll(dest, 0o755)
	filePath := filepath.Join(dest, "fail_file.txt")
	if err := os.WriteFile(filePath, []byte("all fail content"), 0o644); err != nil {
		t.Fatal(err)
	}

	job, err := stStore.CreateJob(context.Background(), 100, "mirror:http://example.com/fail_file.txt", true, `{"is_mirror": true}`)
	if err != nil {
		t.Fatal(err)
	}

	st := newState(job, dest, dest+"_extract", context.Background())
	mgr.processFile(st, dest, filePath, false)

	sent, skipped := st.counts()
	if sent != 0 {
		t.Errorf("expected sent=0 when all hosts fail, got %d", sent)
	}
	if skipped != 1 {
		t.Errorf("expected skipped=1 when all hosts fail, got %d", skipped)
	}

	skips := st.skips()
	if len(skips) != 1 {
		t.Fatalf("expected 1 skip, got %d", len(skips))
	}
	if !strings.Contains(skips[0].Reason, "GoFile") || !strings.Contains(skips[0].Reason, "FileDitch") {
		t.Errorf("expected skip reason mentioning both hosts, got: %s", skips[0].Reason)
	}
}

func TestPipeline_Resume_PersistedMirrorHosts(t *testing.T) {
	mgr, _, _, stStore, cfg := setupTestPipelineManager(t, "gofile,fileditch,pixeldrain")

	rawArgs := `{"is_mirror": true, "mirror_hosts": ["gofile", "pixeldrain"]}`
	job, err := stStore.CreateJob(context.Background(), 100, "mirror:http://example.com/test.mp4", true, rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	loadedJob, err := stStore.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}

	st := newState(loadedJob, cfg.DownloadsDir(), cfg.DownloadsDir()+"_extract", context.Background())
	selected := mgr.selectedHosts(st.Args())

	if len(selected) != 2 {
		t.Fatalf("expected 2 selected hosts on resume, got %d: %v", len(selected), selected)
	}
	if selected[0] != upload.GoFile || selected[1] != upload.Pixeldrain {
		t.Errorf("expected [gofile, pixeldrain], got %v", selected)
	}
}
