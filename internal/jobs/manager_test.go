package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/store"
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

	dirsToCreate := []string{
		"job_1",
		"job_1_extract",
		"job_1_pd_temp",
		"job_2",
		"job_2_extract",
		"job_2_patch_work",
		"tmp_up_abc",
		"tmp_conf_xyz",
		"important_folder",
	}

	oldTime := time.Now().Add(-20 * time.Minute)
	for _, d := range dirsToCreate {
		p := filepath.Join(downloads, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, oldTime, oldTime)
	}

	keep := []*store.Job{
		{ID: "1"}, // j.DownloadDir() is "job_1"
	}

	m.cleanupOrphans(keep)

	// Check which should exist
	shouldExist := map[string]bool{
		"job_1":            true,
		"job_1_extract":    true,
		"job_1_pd_temp":    true,
		"important_folder": true,
	}

	shouldNotExist := []string{
		"job_2",
		"job_2_extract",
		"job_2_patch_work",
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
}
