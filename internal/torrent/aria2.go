package torrent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/pacing"
)

// Progress is reported on every poll of an aria2 download.
type Progress struct {
	Pct        float64
	Completed  int64
	Speed      int64
	Seeders    int
	Connection int
	Name       string
}

// ProgressFunc receives aria2 progress.
type ProgressFunc func(Progress)

// Aria2 manages a single local aria2c RPC daemon.
type Aria2 struct {
	cfg      *config.Config
	trackers *Trackers
	client   *http.Client

	mu     sync.Mutex
	cmd    *exec.Cmd
	port   int
	secret string
	exited chan struct{}
}

func NewAria2(cfg *config.Config, tr *Trackers) *Aria2 {
	return &Aria2{cfg: cfg, trackers: tr, client: &http.Client{Timeout: 15 * time.Second}}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func speedOpt(limit string) string {
	if pacing.ParseSpeedLimit(limit) == 0 {
		return "0"
	}
	return limit
}

// Start launches the daemon (idempotent). If aria2c is missing it logs a
// warning and returns nil so the rest of the bot keeps working.
func (a *Aria2) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.cmd != nil && a.alive() {
		port := a.port
		a.mu.Unlock()
		_, _ = a.call(ctx, port, "aria2.changeGlobalOption", map[string]any{"max-overall-download-limit": speedOpt(a.cfg.GlobalSpeedLimit)})
		return nil
	}
	a.mu.Unlock()

	if _, err := exec.LookPath("aria2c"); err != nil {
		slog.Warn("aria2c is not installed; torrent/aria downloads will fail")
		return nil
	}
	a.trackers.Refresh(ctx)

	port, err := freePort()
	if err != nil {
		return err
	}
	var sec [24]byte
	if _, err := rand.Read(sec[:]); err != nil {
		return err
	}
	secret := hex.EncodeToString(sec[:])

	args := []string{
		"--enable-rpc", "--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(port),
		"--rpc-secret=" + secret,
		"--log=" + filepath.Join(a.cfg.LogDir, "aria2c_daemon.log"),
		"--log-level=notice",
		"--seed-time=0", "--seed-ratio=0.0",
		"--bt-tracker-connect-timeout=10", "--bt-tracker-timeout=10",
		"--enable-dht=true", "--bt-enable-lpd=true", "--enable-peer-exchange=true",
		"--bt-max-peers=120", "--max-overall-upload-limit=50K",
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"--bt-tracker=" + a.trackers.CSV(),
	}
	if a.cfg.ForceIPv6 || a.cfg.SourceAddress != "" {
		src := a.cfg.SourceAddress
		if src == "" {
			src = "::"
		}
		args = append(args, "--disable-ipv6=false", "--enable-dht6=true", "--async-dns=true", "--interface="+src)
	}
	if pacing.ParseSpeedLimit(a.cfg.GlobalSpeedLimit) > 0 {
		args = append(args, "--max-overall-download-limit="+a.cfg.GlobalSpeedLimit)
	}

	cmd := exec.Command("aria2c", args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start aria2c: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	a.mu.Lock()
	a.cmd, a.port, a.secret, a.exited = cmd, port, secret, exited
	a.mu.Unlock()
	slog.Info("aria2c RPC daemon started", "port", port)

	// Wait until the RPC endpoint answers.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.call(ctx, port, "aria2.getVersion", nil); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("aria2c exited during startup")
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("aria2c RPC did not become ready")
}

func (a *Aria2) alive() bool {
	select {
	case <-a.exited:
		return false
	default:
		return a.cmd != nil
	}
}

// Stop shuts the daemon down gracefully, killing it after a short grace period.
func (a *Aria2) Stop(ctx context.Context) {
	a.mu.Lock()
	cmd, port, exited := a.cmd, a.port, a.exited
	a.cmd = nil
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	_, _ = a.call(ctx, port, "aria2.shutdown", nil)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

func (a *Aria2) call(ctx context.Context, port int, method string, params ...any) (json.RawMessage, error) {
	a.mu.Lock()
	secret := a.secret
	a.mu.Unlock()
	p := make([]any, 0, len(params)+1)
	if secret != "" {
		p = append(p, "token:"+secret)
	}
	for _, x := range params {
		if x != nil {
			p = append(p, x)
		}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "tgdl", "method": method, "params": p})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("aria2 rpc: bad response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("aria2 rpc %s: %s", method, out.Error.Message)
	}
	return out.Result, nil
}

type status struct {
	Status          string `json:"status"`
	ErrorCode       string `json:"errorCode"`
	ErrorMessage    string `json:"errorMessage"`
	CompletedLength string `json:"completedLength"`
	TotalLength     string `json:"totalLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	NumSeeders      string `json:"numSeeders"`
	Connections     string `json:"connections"`
	FollowedBy      []string `json:"followedBy"`
	Bittorrent      struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
}

func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

// Handle lets the queue manager cancel an in-flight aria2 download.
type Handle struct {
	a   *Aria2
	gid string
	mu  sync.Mutex
}

// Kill force-removes the active GID.
func (h *Handle) Kill() {
	h.mu.Lock()
	gid := h.gid
	h.mu.Unlock()
	h.a.mu.Lock()
	port := h.a.port
	h.a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.a.call(ctx, port, "aria2.forceRemove", gid); err != nil {
		slog.Warn("aria2 forceRemove failed", "gid", gid, "err", err)
	}
}

// Download fetches HTTP/FTP/magnet/torrent targets via the daemon and blocks
// until completion, failure, or ctx cancellation.
func (a *Aria2) Download(ctx context.Context, target, destDir string, opts map[string]any, onProgress ProgressFunc) dl.Result {
	if err := a.Start(ctx); err != nil {
		return dl.Result{ErrorTail: "aria2c RPC daemon is not running: " + err.Error()}
	}
	a.mu.Lock()
	port, running := a.port, a.cmd != nil && a.alive()
	a.mu.Unlock()
	if !running {
		return dl.Result{ErrorTail: "aria2c RPC daemon is not running."}
	}

	isMagnet := strings.HasPrefix(target, "magnet:") || strings.Contains(target, "magnet:?xt=")
	isTorrentFile := (strings.HasPrefix(target, "torrent:") || strings.HasSuffix(target, ".torrent")) &&
		!strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") &&
		!strings.HasPrefix(target, "magnet:") && !strings.HasPrefix(target, "ftp://")
	isBT := isMagnet || isTorrentFile

	rpcOpts := map[string]any{"dir": destDir}
	if isBT {
		rpcOpts["bt-tracker"] = a.trackers.CSV()
	}
	for k, v := range opts {
		rpcOpts[k] = v
	}

	var raw json.RawMessage
	var err error
	if isTorrentFile {
		p := strings.TrimPrefix(target, "torrent:")
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return dl.Result{ErrorTail: fmt.Sprintf("torrent file not readable: %v", rerr)}
		}
		raw, err = a.call(ctx, port, "aria2.addTorrent", base64.StdEncoding.EncodeToString(b), []string{}, rpcOpts)
	} else {
		t := strings.TrimPrefix(target, "torrent:")
		if isMagnet && strings.HasPrefix(t, "magnet:") {
			t = a.trackers.AddToMagnet(t)
		}
		raw, err = a.call(ctx, port, "aria2.addUri", []string{t}, rpcOpts)
	}
	if err != nil {
		return dl.Result{ErrorTail: "Failed to add download to daemon: " + err.Error()}
	}
	var gid string
	if json.Unmarshal(raw, &gid) != nil || gid == "" {
		return dl.Result{ErrorTail: "aria2c daemon did not return a GID."}
	}
	h := &Handle{a: a, gid: gid}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.mu.Lock()
		g := h.gid
		h.mu.Unlock()
		_, _ = a.call(c, port, "aria2.removeDownloadResult", g)
	}()

	// Cancellation: force-remove the GID when ctx ends.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			h.Kill()
		case <-done:
		}
	}()

	lastActive := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return dl.Result{ErrorTail: "cancelled"}
		case <-a.exited:
			return dl.Result{ErrorTail: "aria2c daemon stopped unexpectedly"}
		case <-tick.C:
		}

		h.mu.Lock()
		cur := h.gid
		h.mu.Unlock()
		res, err := a.call(ctx, port, "aria2.tellStatus", cur)
		if err != nil {
			slog.Warn("aria2 tellStatus failed", "gid", cur, "err", err)
			continue
		}
		var st status
		if err := json.Unmarshal(res, &st); err != nil {
			continue
		}
		if len(st.FollowedBy) > 0 { // metadata download handed off to the real transfer
			h.mu.Lock()
			h.gid = st.FollowedBy[0]
			h.mu.Unlock()
			continue
		}
		switch st.Status {
		case "complete":
			return dl.Result{OK: true, Files: listResult(destDir)}
		case "error":
			return dl.Result{ErrorTail: fmt.Sprintf("Aria2 error code %s: %s", st.ErrorCode, st.ErrorMessage)}
		case "removed":
			return dl.Result{ErrorTail: "download removed"}
		}

		completed, total, speed := atoi64(st.CompletedLength), atoi64(st.TotalLength), atoi64(st.DownloadSpeed)
		seeders, conns := int(atoi64(st.NumSeeders)), int(atoi64(st.Connections))
		pct := 0.0
		if total > 0 {
			pct = float64(completed) * 100 / float64(total)
		}
		if isBT {
			if completed > 0 || speed > 0 || seeders > 0 {
				lastActive = time.Now()
			} else if time.Since(lastActive) > 5*time.Minute {
				h.Kill()
				return dl.Result{ErrorTail: "Torrent is dead (stuck at 0% with no active seeders/peers)."}
			}
		}
		name := st.Bittorrent.Info.Name
		if name == "" && len(st.Files) > 0 && st.Files[0].Path != "" {
			name = filepath.Base(st.Files[0].Path)
		}
		if onProgress != nil {
			onProgress(Progress{Pct: pct, Completed: completed, Speed: speed, Seeders: seeders, Connection: conns, Name: name})
		}
	}
}

func listResult(dir string) []string {
	all, _ := fsutil.ListFiles(dir)
	out := all[:0]
	for _, p := range all {
		if !strings.HasSuffix(p, ".part") && !strings.HasSuffix(p, ".aria2") {
			out = append(out, p)
		}
	}
	fsutil.SortNatural(out)
	return out
}
