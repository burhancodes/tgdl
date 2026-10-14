//go:build loadtest

package tg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/burhanverse/tgdl/internal/tg"
)

func TestUploadLargeFileMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "large_sparse_1500M.bin")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	const fileSize = 1500 * 1024 * 1024
	if err := f.Truncate(fileSize); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	api := tg.NewBotAPI(nil, "123456:TEST")
	api.BaseURL = srv.URL

	_, err = api.Upload(context.Background(), 123456, tg.Upload{
		Path: filePath,
		Kind: tg.KindDocument,
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	allocMB := float64(memAfter.Alloc) / (1024 * 1024)
	sysMB := float64(memAfter.Sys) / (1024 * 1024)
	t.Logf("Allocated: %.2f MB, Sys: %.2f MB", allocMB, sysMB)

	if memAfter.Alloc > 200*1024*1024 {
		t.Fatalf("memory allocation too high: %d bytes (%.2f MB > 200 MB)", memAfter.Alloc, allocMB)
	}
}
