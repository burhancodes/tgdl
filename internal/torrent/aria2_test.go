package torrent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

func newMockAria(t *testing.T, handler http.HandlerFunc) (*Aria2, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	aria := &Aria2{
		client: ts.Client(),
		port:   port,
		cmd:    &exec.Cmd{},
		exited: make(chan struct{}),
	}
	return aria, ts
}

func TestAria2_TellStatus(t *testing.T) {
	aria, ts := newMockAria(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "aria2.tellStatus" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "tgdl",
				"result": map[string]any{
					"gid":             "gid999",
					"status":          "active",
					"completedLength": "500",
					"totalLength":     "1000",
					"downloadSpeed":   "250",
					"bittorrent": map[string]any{
						"info": map[string]string{
							"name": "sample_video.mp4",
						},
					},
					"files": []map[string]any{
						{
							"path": "/tmp/sample_video.mp4",
						},
					},
				},
			})
			return
		}
		http.Error(w, "unknown method", http.StatusBadRequest)
	})
	defer ts.Close()

	st, err := aria.TellStatus(context.Background(), "gid999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.GID != "gid999" || st.Name != "sample_video.mp4" || st.Status != "active" {
		t.Errorf("unexpected status: %+v", st)
	}
	if st.CompletedLength != 500 || st.TotalLength != 1000 || st.DownloadSpeed != 250 {
		t.Errorf("unexpected metrics: %+v", st)
	}
}

func TestAria2_HandleKill(t *testing.T) {
	killed := false
	aria, ts := newMockAria(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "aria2.forceRemove" {
			killed = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "tgdl",
				"result":  "OK",
			})
			return
		}
		http.Error(w, "unknown method", http.StatusBadRequest)
	})
	defer ts.Close()

	h := &Handle{a: aria, gid: "gidKillTest"}
	h.Kill()
	if !killed {
		t.Error("expected aria2.forceRemove to be called on Kill")
	}
}

func TestAria2_ErrorResponse(t *testing.T) {
	aria, ts := newMockAria(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "tgdl",
			"error": map[string]any{
				"code":    1,
				"message": "GID not found",
			},
		})
	})
	defer ts.Close()

	if _, err := aria.TellStatus(context.Background(), "invalid_gid"); err == nil {
		t.Fatal("expected error, got nil")
	}
}
