package dl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		GofileBypassHost: "gf.1drv.eu.org",
		AllowPrivateURLs: true,
	}
}

func TestIsGofileURL(t *testing.T) {
	cfg := testConfig()
	yes := []string{
		"https://gofile.io/d/QGjyK2",
		"https://www.gofile.io/d/QGjyK2",
		"https://gofile.co/d/XYZ123",
		"https://sub.gofile.io/f/abcd456",
		"gofile:https://gofile.io/d/QGjyK2",
		"gofile:QGjyK2",
		"gf:https://gofile.io/d/QGjyK2",
		"gfdl:https://gofile.io/d/QGjyK2",
		"https://gf.1drv.eu.org/QGjyK2",
	}
	for _, u := range yes {
		if !IsGofileURL(cfg, u) {
			t.Errorf("IsGofileURL(%q) = false, want true", u)
		}
	}

	no := []string{
		"https://example.com/file.zip",
		"https://drive.google.com/file/d/123",
		"https://mega.nz/file/123#abc",
		"",
	}
	for _, u := range no {
		if IsGofileURL(cfg, u) {
			t.Errorf("IsGofileURL(%q) = true, want false", u)
		}
	}
}

func TestExtractGofileInfo(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		url          string
		wantID       string
		wantTrailing string
		wantOK       bool
	}{
		{"https://gofile.io/d/QGjyK2", "QGjyK2", "", true},
		{"https://www.gofile.io/d/QGjyK2", "QGjyK2", "", true},
		{"https://gofile.io/d/QGjyK2/video.mp4", "QGjyK2", "/video.mp4", true},
		{"https://gofile.co/f/myfolder123", "myfolder123", "", true},
		{"https://gofile.io/file/d/test_id_99", "test_id_99", "", true},
		{"https://gofile.io/?file=query_id_1", "query_id_1", "", true},
		{"https://gofile.io/direct_id_42", "direct_id_42", "", true},
		{"gofile:https://gofile.io/d/prefixed123", "prefixed123", "", true},
		{"gofile:raw_id_only", "raw_id_only", "", true},

		// Ignored prefixes
		{"https://gofile.io/api/getFolder", "", "", false},
		{"https://gofile.io/public/assets/logo.png", "", "", false},
		{"https://gofile.io/static/style.css", "", "", false},
		{"https://gofile.io/cdn/chunk.bin", "", "", false},
		{"https://gofile.io/download?id=123", "", "", false},

		// Opt-out parameter
		{"https://gofile.io/d/QGjyK2?noredirect=1", "", "", false},
	}

	for _, c := range cases {
		id, trailing, ok := ExtractGofileInfo(cfg, c.url)
		if ok != c.wantOK {
			t.Errorf("ExtractGofileInfo(%q) ok = %v, want %v", c.url, ok, c.wantOK)
			continue
		}
		if ok {
			if id != c.wantID || trailing != c.wantTrailing {
				t.Errorf("ExtractGofileInfo(%q) = (%q, %q), want (%q, %q)", c.url, id, trailing, c.wantID, c.wantTrailing)
			}
		}
	}
}

func TestGofileBypassURL(t *testing.T) {
	cfg := testConfig()

	// Default host gf.1drv.eu.org
	if got := GofileBypassURL(cfg, "https://gofile.io/d/QGjyK2"); got != "https://gf.1drv.eu.org/QGjyK2" {
		t.Errorf("GofileBypassURL default failed: got %q", got)
	}

	// Trailing path preserved
	if got := GofileBypassURL(cfg, "https://gofile.io/d/QGjyK2/movie.mp4"); got != "https://gf.1drv.eu.org/QGjyK2/movie.mp4" {
		t.Errorf("GofileBypassURL trailing failed: got %q", got)
	}

	// Custom bypass host
	customCfg := &config.Config{GofileBypassHost: "custom.proxy.org"}
	if got := GofileBypassURL(customCfg, "https://gofile.co/f/abcd"); got != "https://custom.proxy.org/abcd" {
		t.Errorf("GofileBypassURL custom host failed: got %q", got)
	}

	// Preserves query params and hash
	if got := GofileBypassURL(cfg, "https://gofile.io/d/XYZ123?token=abc#section"); got != "https://gf.1drv.eu.org/XYZ123?token=abc#section" {
		t.Errorf("GofileBypassURL query/hash failed: got %q", got)
	}

	// Opt-out noredirect returns original URL
	orig := "https://gofile.io/d/XYZ123?token=abc&noredirect=1"
	if got := GofileBypassURL(cfg, orig); got != orig {
		t.Errorf("GofileBypassURL noredirect failed: got %q, want %q", got, orig)
	}
}

func TestDownloadGofileBatchNaturalSort(t *testing.T) {
	var requestedPaths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(r.URL.Path)+`.bin"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dummy content"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		GofileBypassHost: ts.URL,
		AllowPrivateURLs: true,
	}

	tmpDir, err := os.MkdirTemp("", "tgdl_gofile_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	batchURLs := `["https://gofile.io/d/id_ep10", "https://gofile.io/d/id_ep2", "https://gofile.io/d/id_ep1"]`

	files, err := DownloadGofile(context.Background(), cfg, tmpDir, batchURLs, nil)
	if err != nil {
		t.Fatalf("DownloadGofile failed: %v", err)
	}

	// Naturally sorted order should be id_ep1, id_ep2, id_ep10
	expectedOrder := []string{"/id_ep1", "/id_ep2", "/id_ep10"}
	if len(requestedPaths) != len(expectedOrder) {
		t.Fatalf("requested %d paths, want %d: %v", len(requestedPaths), len(expectedOrder), requestedPaths)
	}
	for i, want := range expectedOrder {
		if requestedPaths[i] != want {
			t.Errorf("request %d: got %s, want %s", i, requestedPaths[i], want)
		}
	}
	if len(files) != 3 {
		t.Fatalf("downloaded %d files, want 3", len(files))
	}
}

func TestDownloadGofileStructuredItems(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dummy video"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		GofileBypassHost: ts.URL,
		AllowPrivateURLs: true,
	}

	tmpDir, err := os.MkdirTemp("", "tgdl_gofile_struct_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	inputJSON := `[
		{"url": "https://gofile.io/d/itemB", "filename": "ep2.mp4", "path": "Season 1"},
		{"url": "https://gofile.io/d/itemA", "filename": "ep1.mp4", "path": "Season 1"}
	]`

	files, err := DownloadGofile(context.Background(), cfg, tmpDir, inputJSON, nil)
	if err != nil {
		t.Fatalf("DownloadGofile with structured items failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("downloaded %d files, want 2", len(files))
	}
	if filepath.Base(files[0]) != "ep1.mp4" || filepath.Base(files[1]) != "ep2.mp4" {
		t.Errorf("unexpected file order: %v", files)
	}
}
