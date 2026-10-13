package settings

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

func TestManagerPrecedenceAndGet(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 100, HasOwner: true}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	val, src, _, err := mgr.Get("LOG_LEVEL")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "INFO" || src != SourceDefault {
		t.Errorf("got (%q, %s), want (INFO, default)", val, src)
	}

	t.Setenv("LOG_LEVEL", "WARNING")
	val, src, _, err = mgr.Get("LOG_LEVEL")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "WARNING" || src != SourceEnv {
		t.Errorf("got (%q, %s), want (WARNING, env)", val, src)
	}

	ctx := context.Background()
	if _, err := mgr.Set(ctx, 100, "LOG_LEVEL", "DEBUG", "test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, src, _, err = mgr.Get("LOG_LEVEL")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "DEBUG" || src != SourceOverride {
		t.Errorf("got (%q, %s), want (DEBUG, bot override)", val, src)
	}
}

func TestManagerReadOnlyProtection(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 100, HasOwner: true}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	roKeys := []string{"OWNER_ID", "TG_BOT_TOKEN", "TG_API_ID", "TG_API_HASH", "TG_BOT_API_URL", "DATA_DIR", "AUTH_DIR", "LOG_DIR"}
	for _, k := range roKeys {
		if _, err := mgr.Set(ctx, 100, k, "hacked", "test"); err == nil {
			t.Errorf("expected error setting read-only key %s", k)
		}
	}
}

func TestManagerLockoutProtection(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 12345, HasOwner: true, AuthorizedIDs: []int64{12345}}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.Set(ctx, 12345, "AUTHORIZED_USER_IDS", "9999,8888", "test"); err == nil {
		t.Errorf("expected error when setting AUTHORIZED_USER_IDS without owner")
	}

	if _, err := mgr.Set(ctx, 12345, "AUTHORIZED_USER_IDS", "12345,9999", "test"); err != nil {
		t.Errorf("unexpected error with owner included: %v", err)
	}
}

func TestManagerResetAndUndo(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 100, HasOwner: true}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.Set(ctx, 100, "MAX_JOBS_PER_CHAT", "5", "test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, _, _, _ := mgr.Get("MAX_JOBS_PER_CHAT")
	if val != "5" {
		t.Fatalf("got %q, want 5", val)
	}

	if _, err := mgr.Reset(ctx, 100, "MAX_JOBS_PER_CHAT", "test"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	val, src, _, _ := mgr.Get("MAX_JOBS_PER_CHAT")
	if val != "3" || src != SourceDefault {
		t.Fatalf("after reset got (%q, %s), want (3, default)", val, src)
	}

	undone, err := mgr.Undo(ctx, 100)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if undone.Key != "MAX_JOBS_PER_CHAT" {
		t.Errorf("undone key = %s, want MAX_JOBS_PER_CHAT", undone.Key)
	}
	val, _, _, _ = mgr.Get("MAX_JOBS_PER_CHAT")
	if val != "5" {
		t.Errorf("after undo got %q, want 5", val)
	}
}

func TestManagerExportImport(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 100, HasOwner: true}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.Set(ctx, 100, "MAX_JOBS_PER_CHAT", "7", "test"); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	if _, err := mgr.Set(ctx, 100, "GOFILE_API_KEY", "secret-gofile-key", "test"); err != nil {
		t.Fatalf("Set 2: %v", err)
	}

	exported, err := mgr.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	expStr := string(exported)
	if strings.Contains(expStr, "secret-gofile-key") {
		t.Errorf("export contains plaintext secret!")
	}
	if !strings.Contains(expStr, "[REDACTED]") {
		t.Errorf("export does not contain [REDACTED]")
	}

	dir2 := t.TempDir()
	store2 := NewStore(dir2, "", false, "", reg)
	mgr2, err := NewManager(cfg, reg, store2)
	if err != nil {
		t.Fatalf("NewManager 2: %v", err)
	}

	diffs, applyFn, err := mgr2.Import(exported)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Key != "MAX_JOBS_PER_CHAT" {
		t.Errorf("expected 1 diff for MAX_JOBS_PER_CHAT, got %v", diffs)
	}

	if err := applyFn(ctx, 100); err != nil {
		t.Fatalf("applyFn: %v", err)
	}
	v, _, _, _ := mgr2.Get("MAX_JOBS_PER_CHAT")
	if v != "7" {
		t.Errorf("imported value = %q, want 7", v)
	}
}

func TestManagerRateLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OwnerID: 100, HasOwner: true}
	reg := NewRegistry()
	store := NewStore(dir, "", false, "", reg)
	mgr, err := NewManager(cfg, reg, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	mgr.limiter = NewEditRateLimiter(3, 50*time.Millisecond)

	for i := 0; i < 3; i++ {
		if _, err := mgr.Set(ctx, 100, "MAX_JOBS_PER_CHAT", "2", "test"); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if _, err := mgr.Set(ctx, 100, "MAX_JOBS_PER_CHAT", "4", "test"); err == nil {
		t.Errorf("expected rate limit error on 4th edit")
	}

	time.Sleep(60 * time.Millisecond)
	if _, err := mgr.Set(ctx, 100, "MAX_JOBS_PER_CHAT", "4", "test"); err != nil {
		t.Errorf("expected edit to succeed after window: %v", err)
	}
}
