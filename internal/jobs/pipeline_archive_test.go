package jobs

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

func createTestZip(t *testing.T, path, innerName, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	zf, err := w.Create(innerName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zf.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPipeline_ArchiveHandling(t *testing.T) {
	tests := []struct {
		name                  string
		unzipJob              bool
		choice                string
		wantArchiveUploaded   bool
		wantExtractedUploaded bool
		wantArchiveRemoved    bool
	}{
		{
			name:                  "unzipJob",
			unzipJob:              true,
			wantArchiveUploaded:   false,
			wantExtractedUploaded: true,
			wantArchiveRemoved:    true,
		},
		{
			name:                  "regular job + Extract & Upload Both",
			unzipJob:              false,
			choice:                ChoiceExtract,
			wantArchiveUploaded:   true,
			wantExtractedUploaded: true,
			wantArchiveRemoved:    true,
		},
		{
			name:                  "regular job + Extract only",
			unzipJob:              false,
			choice:                ChoiceExtractOnly,
			wantArchiveUploaded:   false,
			wantExtractedUploaded: true,
			wantArchiveRemoved:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			dbPath := filepath.Join(tempDir, "store.db")
			st, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()

			cfg := &config.Config{
				DataDir:          tempDir,
				TGUploadDelayMin: 0,
				TGUploadDelayMax: 0,
			}
			downloads := cfg.DownloadsDir()
			_ = os.MkdirAll(downloads, 0o755)

			var mu sync.Mutex
			var uploadedFiles []string
			mock := &mockTGClient{
				uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
					mu.Lock()
					uploadedFiles = append(uploadedFiles, filepath.Base(u.Path))
					mu.Unlock()
					return nil
				},
			}

			lim := pacing.NewTelegramLimiter()
			lim.SetGaps(0, 0)
			d := &Deps{
				Cfg:   cfg,
				Store: st,
				TG:    mock,
			}
			mgr := NewManager(d, lim)

			dest := filepath.Join(tempDir, "dest")
			extract := filepath.Join(tempDir, "extract")
			_ = os.MkdirAll(dest, 0o755)
			_ = os.MkdirAll(extract, 0o755)

			archivePath := filepath.Join(dest, "sample.zip")
			createTestZip(t, archivePath, "inner.txt", "hello inner file")

			jobArgs := "{}"
			if tc.unzipJob {
				jobArgs = `{"unzip": true}`
			}
			job, err := st.CreateJob(context.Background(), 100, "http://example.com/test.zip", false, jobArgs)
			if err != nil {
				t.Fatal(err)
			}

			state := newState(job, dest, extract, context.Background())
			if tc.choice != "" {
				state.sess.setArchiveChoice("1", tc.choice)
			}

			mgr.processFile(state, state.Dest, archivePath, false)

			mu.Lock()
			uploads := append([]string(nil), uploadedFiles...)
			mu.Unlock()

			archiveUploaded := false
			extractedUploaded := false
			for _, f := range uploads {
				if f == "sample.zip" {
					archiveUploaded = true
				}
				if f == "inner.txt" {
					extractedUploaded = true
				}
			}

			if archiveUploaded != tc.wantArchiveUploaded {
				t.Errorf("archiveUploaded = %v, want %v", archiveUploaded, tc.wantArchiveUploaded)
			}
			if extractedUploaded != tc.wantExtractedUploaded {
				t.Errorf("extractedUploaded = %v, want %v", extractedUploaded, tc.wantExtractedUploaded)
			}

			archiveExists := false
			if _, err := os.Stat(archivePath); err == nil {
				archiveExists = true
			}
			if archiveExists == tc.wantArchiveRemoved {
				t.Errorf("archiveExists = %v, want removed = %v", archiveExists, tc.wantArchiveRemoved)
			}
		})
	}
}
