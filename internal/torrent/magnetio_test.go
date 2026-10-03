package torrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

func TestIsLocalEndpoint(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"", true},
		{"http://127.0.0.1:8080/rpc", true},
		{"http://127.0.0.1:45041/rpc", true},
		{"http://localhost:8080/rpc", true},
		{"http://localhost/rpc", true},
		{"http://[::1]:8080/rpc", true},
		{"http://magnetio-scraper:8080/rpc", false},
		{"http://scraper:8080", false},
		{"https://search.internal.net/rpc", false},
	}

	for _, tt := range tests {
		got := isLocalEndpoint(tt.url)
		if got != tt.expected {
			t.Errorf("isLocalEndpoint(%q) = %v, want %v", tt.url, got, tt.expected)
		}
	}
}

func TestMagnetio_Start_ConnectsRemoteWithRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// Fail the first 2 attempts, succeed on the 3rd
			if attempts.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.URL.Path == "/rpc" {
			var req struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Method == "torrent.providers" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"providers":[{"id":"1337x","name":"1337x"}]},"id":1}`))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := &config.Config{
		MagnetioURL:    srv.URL + "/rpc",
		MagnetioSecret: "test-secret",
		TorrentTimeout: 5 * time.Second,
	}
	m := NewMagnetio(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	m.Start(ctx)

	endpoint, _ := m.endpoint()
	if endpoint != srv.URL+"/rpc" {
		t.Fatalf("expected endpoint %s, got %s", srv.URL+"/rpc", endpoint)
	}

	providers := m.Providers()
	if len(providers) == 0 {
		t.Fatalf("expected providers loaded, got empty")
	}
	if providers["1337x"] != "1337x" {
		t.Errorf("expected provider 1337x, got %v", providers)
	}
}

func TestMagnetio_Start_RemoteFailureDoesNotSpawnLocal(t *testing.T) {
	// Point to a non-existent remote endpoint
	cfg := &config.Config{
		MagnetioURL:    "http://magnetio-scraper.invalid:8080/rpc",
		MagnetioSecret: "secret",
		TorrentTimeout: 200 * time.Millisecond,
	}
	m := NewMagnetio(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// Should attempt probes, hit ctx timeout or retries, and return without spawning local node
	m.Start(ctx)

	// Local cmd should remain nil
	if m.cmd != nil {
		t.Fatalf("expected m.cmd to be nil for remote endpoint, got %v", m.cmd)
	}
	// Endpoint should still be the configured remote URL, NOT localhost
	endpoint, _ := m.endpoint()
	if endpoint != "http://magnetio-scraper.invalid:8080/rpc" {
		t.Fatalf("endpoint should remain configured remote URL, got %s", endpoint)
	}
}
