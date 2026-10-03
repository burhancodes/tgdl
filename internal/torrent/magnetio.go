package torrent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

// RPCError is a JSON-RPC or transport failure talking to the search sidecar.
type RPCError struct {
	Msg  string
	Code int
}

func (e *RPCError) Error() string { return e.Msg }

// Result is a normalized torrent search hit.
type Result struct {
	Name      string
	Size      string
	Seeders   int
	Leechers  int
	Magnet    string
	URL       string
	Provider  string
	Quality   string
	Codec     string
	Source    string
	Languages []string
}

// Magnetio is a client (and optional local supervisor) for the Node.js
// torrent search sidecar in ./scraper.
type Magnetio struct {
	cfg  *config.Config
	http *http.Client

	mu        sync.Mutex
	rpcURL    string
	secret    string
	cmd       *exec.Cmd
	providers map[string]string
}

func NewMagnetio(cfg *config.Config) *Magnetio {
	return &Magnetio{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.TorrentTimeout},
		rpcURL: cfg.MagnetioURL, secret: cfg.MagnetioSecret,
	}
}

func (m *Magnetio) endpoint() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rpcURL == "" {
		return "http://127.0.0.1:8080/rpc", m.secret
	}
	return m.rpcURL, m.secret
}

func (m *Magnetio) probe(ctx context.Context, rpcURL, secret string) bool {
	base := strings.TrimSuffix(strings.TrimRight(rpcURL, "/"), "/rpc")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		Status string `json:"status"`
	}
	return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&v) == nil && v.Status == "ok"
}

// Start connects to an existing sidecar or launches ./scraper/index.js.
func (m *Magnetio) Start(ctx context.Context) {
	if m.cfg.MagnetioURL != "" && m.probe(ctx, m.cfg.MagnetioURL, m.cfg.MagnetioSecret) {
		slog.Info("connected to existing Magnetio RPC service", "url", m.cfg.MagnetioURL)
		m.loadProviders(ctx)
		return
	}
	index := filepath.Join("scraper", "index.js")
	if _, err := os.Stat(index); err != nil {
		slog.Warn("scraper/index.js not found; torrent search unavailable")
		return
	}
	if _, err := exec.LookPath("node"); err != nil {
		slog.Warn("node not found in PATH; cannot launch local Magnetio scraper")
		return
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		slog.Error("cannot allocate port for Magnetio", "err", err)
		return
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	secret := m.cfg.MagnetioSecret
	if secret == "" {
		var b [24]byte
		_, _ = rand.Read(b[:])
		secret = hex.EncodeToString(b[:])
	}
	logf, _ := os.OpenFile(filepath.Join(m.cfg.LogDir, "magnetio_scraper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	cmd := exec.Command("node", "index.js")
	cmd.Dir = "scraper"
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port), "RPC_SHARED_SECRET="+secret)
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	if err := cmd.Start(); err != nil {
		slog.Error("failed to start Magnetio scraper", "err", err)
		return
	}
	go func() { _ = cmd.Wait() }()
	u := fmt.Sprintf("http://127.0.0.1:%d/rpc", port)
	m.mu.Lock()
	m.cmd, m.rpcURL, m.secret = cmd, u, secret
	m.mu.Unlock()
	for i := 0; i < 30; i++ {
		if m.probe(ctx, u, secret) {
			slog.Info("Magnetio scraper healthy", "port", port)
			m.loadProviders(ctx)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	slog.Warn("Magnetio scraper health check timed out", "port", port)
}

// Stop terminates a locally supervised sidecar.
func (m *Magnetio) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
	}
}

func (m *Magnetio) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	u, secret := m.endpoint()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, &RPCError{Msg: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, &RPCError{Msg: "connection to search backend failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &RPCError{Msg: fmt.Sprintf("RPC HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &RPCError{Msg: "invalid response format"}
	}
	if out.Error != nil {
		return nil, &RPCError{Msg: fmt.Sprintf("RPC error (%d): %s", out.Error.Code, out.Error.Message), Code: out.Error.Code}
	}
	if out.Result == nil {
		return nil, &RPCError{Msg: "invalid JSON-RPC response: missing result"}
	}
	return out.Result, nil
}

func (m *Magnetio) loadProviders(ctx context.Context) {
	raw, err := m.rpc(ctx, "torrent.providers", map[string]any{})
	if err != nil {
		slog.Warn("fetch torrent providers failed", "err", err)
		return
	}
	var wrapped struct {
		Providers []struct{ ID, Name string } `json:"providers"`
	}
	var list []struct{ ID, Name string }
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Providers != nil {
		list = wrapped.Providers
	} else {
		_ = json.Unmarshal(raw, &list)
	}
	p := map[string]string{}
	for _, it := range list {
		if it.ID != "" && it.Name != "" {
			p[it.ID] = it.Name
		}
	}
	if len(p) > 0 {
		p["all"] = "All Providers"
	}
	m.mu.Lock()
	m.providers = p
	m.mu.Unlock()
	slog.Info("loaded torrent providers", "count", len(p))
}

// Providers returns id->name for available search providers.
func (m *Magnetio) Providers() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.providers))
	for k, v := range m.providers {
		out[k] = v
	}
	return out
}

// FormatBytes renders a byte count as a human string.
func FormatBytes(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for _, u := range units {
		if n < 1024 && n > -1024 {
			return fmt.Sprintf("%.2f %s", n, u)
		}
		n /= 1024
	}
	return fmt.Sprintf("%.2f PB", n)
}

// Search queries the sidecar. providers may be nil for all.
func (m *Magnetio) Search(ctx context.Context, query string, providers []string) ([]Result, error) {
	params := map[string]any{"query": query, "limit": max(m.cfg.SearchLimit, 1), "strict": true, "type": "movie"}
	if len(providers) > 0 {
		params["providers"] = providers
	}
	raw, err := m.rpc(ctx, "torrent.search", params)
	if err != nil {
		return nil, err
	}
	type item struct {
		Title     string   `json:"title"`
		Name      string   `json:"name"`
		Size      float64  `json:"size"`
		Seeders   float64  `json:"seeders"`
		Leechers  float64  `json:"leechers"`
		Magnet    string   `json:"magnet"`
		Provider  string   `json:"provider"`
		Quality   string   `json:"quality"`
		Codec     string   `json:"codec"`
		Source    string   `json:"source"`
		Languages []string `json:"languages"`
	}
	var wrapped struct {
		Torrents []item `json:"torrents"`
	}
	var items []item
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Torrents != nil {
		items = wrapped.Torrents
	} else if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &RPCError{Msg: "unexpected search result shape"}
	}
	out := make([]Result, 0, len(items))
	for _, it := range items {
		name := it.Title
		if name == "" {
			name = it.Name
		}
		if name == "" {
			name = "Unknown"
		}
		prov := it.Provider
		if prov == "" {
			prov = "unknown"
		}
		u := it.Magnet
		if u == "" {
			u = "#"
		}
		out = append(out, Result{Name: name, Size: FormatBytes(it.Size), Seeders: int(it.Seeders), Leechers: int(it.Leechers),
			Magnet: it.Magnet, URL: u, Provider: prov, Quality: it.Quality, Codec: it.Codec, Source: it.Source, Languages: it.Languages})
	}
	return out, nil
}

// FormatHTML renders the first 15 results as Telegram HTML.
func FormatHTML(results []Result, query, site string) string {
	if len(results) == 0 {
		return "<b>No torrent results found</b> for <i>" + html.EscapeString(query) + "</i>."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Search Results for:</b> <code>%s</code>\n<b>Source:</b> %s | <b>Total:</b> %d\n\n",
		html.EscapeString(query), html.EscapeString(strings.Title(site)), len(results)) //nolint:staticcheck
	for i, r := range results {
		if i >= 15 {
			break
		}
		fmt.Fprintf(&b, "<b>%d. %s</b>\n├ <b>Size:</b> %s | <b>S:</b> %d | <b>L:</b> %d\n", i+1, html.EscapeString(r.Name), r.Size, r.Seeders, r.Leechers)
		if r.Magnet != "" {
			fmt.Fprintf(&b, "└ <a href='http://t.me/share/url?url=%s'>Share Magnet</a>\n\n", url.QueryEscape(r.Magnet))
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

var _ = errors.New

// Call performs a raw JSON-RPC request; used by the XenForo scraper engine.
func (m *Magnetio) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return m.rpc(ctx, method, params)
}

// EnsureStarted lazily connects to (or launches) the sidecar.
func (m *Magnetio) EnsureStarted(ctx context.Context) {
	u, s := m.endpoint()
	if m.probe(ctx, u, s) {
		return
	}
	m.Start(ctx)
}
