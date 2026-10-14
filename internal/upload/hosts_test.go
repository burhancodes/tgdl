package upload_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/upload"
)

func TestParseHosts(t *testing.T) {
	tests := []struct {
		input    string
		want     []upload.HostID
		wantErr  bool
		errToken string
	}{
		{
			input: "",
			want:  nil,
		},
		{
			input: "   ",
			want:  nil,
		},
		{
			input: "gofile,fileditch,pixeldrain",
			want:  []upload.HostID{upload.GoFile, upload.FileDitch, upload.Pixeldrain},
		},
		{
			input: "gf,fd,pd",
			want:  []upload.HostID{upload.GoFile, upload.FileDitch, upload.Pixeldrain},
		},
		{
			input: "GOFILE, PD",
			want:  []upload.HostID{upload.GoFile, upload.Pixeldrain},
		},
		{
			input: "gf, gofile, pd, pixeldrain",
			want:  []upload.HostID{upload.GoFile, upload.Pixeldrain},
		},
		{
			input: "all",
			want:  upload.AllHosts,
		},
		{
			input:    "gofile, dropbox",
			wantErr:  true,
			errToken: "dropbox",
		},
		{
			input:    "invalid",
			wantErr:  true,
			errToken: "invalid",
		},
	}

	for _, tt := range tests {
		got, err := upload.ParseHosts(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseHosts(%q) expected error, got nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHosts(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("ParseHosts(%q) len = %d, want %d (%v vs %v)", tt.input, len(got), len(tt.want), got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseHosts(%q)[%d] = %v, want %v", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestHostID_Properties(t *testing.T) {
	if upload.GoFile.Name() != "GoFile" {
		t.Errorf("unexpected name for GoFile: %s", upload.GoFile.Name())
	}
	if upload.FileDitch.Name() != "FileDitch" {
		t.Errorf("unexpected name for FileDitch: %s", upload.FileDitch.Name())
	}
	if upload.Pixeldrain.Name() != "Pixeldrain" {
		t.Errorf("unexpected name for Pixeldrain: %s", upload.Pixeldrain.Name())
	}

	if upload.Pixeldrain.MaxBytes() != 10<<30 {
		t.Errorf("expected 10 GiB max for Pixeldrain, got %d", upload.Pixeldrain.MaxBytes())
	}
	if upload.GoFile.MaxBytes() != 0 {
		t.Errorf("expected 0 max for GoFile, got %d", upload.GoFile.MaxBytes())
	}
	if upload.FileDitch.MaxBytes() != 0 {
		t.Errorf("expected 0 max for FileDitch, got %d", upload.FileDitch.MaxBytes())
	}
}

func TestUpload_Retries_503TwiceThen200(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnt := attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if cnt < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Service temporarily unavailable"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok", "data": {"downloadPage": "https://gofile.io/d/success123"}}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(filePath, []byte("retry payload content"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MirrorRetries: 3}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.GoFile, srv.URL)

	link, err := h.UploadWithRetry(context.Background(), upload.GoFile, filePath, "testfile.txt", 0, 3, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link != "https://gofile.io/d/success123" {
		t.Errorf("unexpected link: %s", link)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestUpload_Retries_400NotRetried(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request error"))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(filePath, []byte("bad request test"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MirrorRetries: 3}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.GoFile, srv.URL)

	_, err := h.UploadWithRetry(context.Background(), upload.GoFile, filePath, "testfile.txt", 0, 3, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts.Load() != 1 {
		t.Errorf("expected exactly 1 attempt on 400 error, got %d", attempts.Load())
	}
}

func TestUpload_Retries_429HonoursRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnt := attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if cnt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "id": "px12345"}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(filePath, []byte("429 rate limit test"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MirrorRetries: 3}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.Pixeldrain, srv.URL)

	link, err := h.UploadWithRetry(context.Background(), upload.Pixeldrain, filePath, "testfile.txt", 0, 3, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link != fmt.Sprintf("%s/u/px12345", srv.URL) {
		t.Errorf("unexpected link: %s", link)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestUpload_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(filePath, []byte("cancel test"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MirrorRetries: 3}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.GoFile, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.UploadWithRetry(ctx, upload.GoFile, filePath, "testfile.txt", 0, 3, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpload_SparseFile_Memory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "id": "sparse123"}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sparse_2gb.bin")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	const size2GB = int64(2) * 1024 * 1024 * 1024
	if err := f.Truncate(size2GB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	cfg := &config.Config{MirrorRetries: 0}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.Pixeldrain, srv.URL)

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	var progCurr atomic.Int64
	_, err = h.Upload(context.Background(), upload.Pixeldrain, filePath, "sparse_2gb.bin", 0, func(cur, total int64) {
		progCurr.Store(cur)
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapGrowth := int64(memAfter.HeapInuse) - int64(memBefore.HeapInuse)
	const maxAllowed = 64 * 1024 * 1024
	if heapGrowth > maxAllowed {
		t.Errorf("heap growth %d bytes (%.2f MiB) exceeded 64 MiB limit", heapGrowth, float64(heapGrowth)/(1024*1024))
	}
	if progCurr.Load() != size2GB {
		t.Errorf("progress reported %d bytes, want %d", progCurr.Load(), size2GB)
	}
}
