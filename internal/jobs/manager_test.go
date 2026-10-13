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
