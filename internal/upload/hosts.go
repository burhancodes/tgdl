package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
)

type HostID string

const (
	GoFile     HostID = "gofile"
	FileDitch  HostID = "fileditch"
	Pixeldrain HostID = "pixeldrain"
)

var AllHosts = []HostID{GoFile, FileDitch, Pixeldrain}

func (h HostID) String() string { return string(h) }

func (h HostID) Name() string {
	switch h {
	case GoFile:
		return "GoFile"
	case FileDitch:
		return "FileDitch"
	case Pixeldrain:
		return "Pixeldrain"
	default:
		return string(h)
	}
}

func (h HostID) Aliases() []string {
	switch h {
	case GoFile:
		return []string{"gf", "gfup"}
	case FileDitch:
		return []string{"fd", "fdup"}
	case Pixeldrain:
		return []string{"pd", "pdup"}
	default:
		return nil
	}
}

func (h HostID) MaxBytes() int64 {
	if h == Pixeldrain {
		return 10 << 30
	}
	return 0
}

func ParseHosts(raw string) ([]HostID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var out []HostID
	seen := make(map[HostID]bool)
	for _, p := range parts {
		token := strings.ToLower(strings.TrimSpace(p))
		if token == "" {
			continue
		}
		var matched HostID
		switch token {
		case "gofile", "gf", "gfup":
			matched = GoFile
		case "fileditch", "fd", "fdup":
			matched = FileDitch
		case "pixeldrain", "pd", "pdup":
			matched = Pixeldrain
		case "all":
			for _, h := range AllHosts {
				if !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
			continue
		default:
			return nil, fmt.Errorf("unknown mirror host: %q", token)
		}
		if !seen[matched] {
			seen[matched] = true
			out = append(out, matched)
		}
	}
	return out, nil
}

func EnabledHosts(cfgHosts string) []HostID {
	if cfgHosts == "" {
		return AllHosts
	}
	hosts, err := ParseHosts(cfgHosts)
	if err != nil || len(hosts) == 0 {
		return AllHosts
	}
	return hosts
}

type Host struct {
	ID       HostID
	Name     string
	Aliases  []string
	MaxBytes int64
	Upload   func(ctx context.Context, path, displayName string, uid int64, cb ProgressFunc) (string, error)
}

type ProgressFunc func(current, total int64)

type HTTPError struct {
	StatusCode int
	Status     string
	RetryAfter time.Duration
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Status)
}

func isRetryable(err error) (bool, time.Duration) {
	if err == nil {
		return false, 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusTooManyRequests {
			return true, httpErr.RetryAfter
		}
		if httpErr.StatusCode >= 500 {
			return true, httpErr.RetryAfter
		}
		return false, 0
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true, 0
	}
	msg := err.Error()
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "EOF") {
		return true, 0
	}
	return false, 0
}

type Hosts struct {
	cfg       *config.Config
	keys      *Keys
	client    *http.Client
	endpoints map[HostID]string
}

func NewHosts(cfg *config.Config, keys *Keys) *Hosts {
	return &Hosts{
		cfg:       cfg,
		keys:      keys,
		client:    &http.Client{Timeout: 0},
		endpoints: make(map[HostID]string),
	}
}

func (h *Hosts) SetEndpoint(id HostID, url string) {
	if h.endpoints == nil {
		h.endpoints = make(map[HostID]string)
	}
	h.endpoints[id] = url
}

func (h *Hosts) SetClient(c *http.Client) {
	h.client = c
}

type countingReader struct {
	r     io.Reader
	n     atomic.Int64
	total int64
	cb    ProgressFunc
	ctx   context.Context
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	if n > 0 {
		cur := c.n.Add(int64(n))
		if c.cb != nil {
			c.cb(cur, c.total)
		}
	}
	return n, err
}

func statFile(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		var retryAfter time.Duration
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			RetryAfter: retryAfter,
			Body:       strings.TrimSpace(string(b)),
		}
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	return nil
}

func (h *Hosts) Host(id HostID) (Host, bool) {
	for _, candidate := range AllHosts {
		if candidate == id {
			return Host{
				ID:       id,
				Name:     id.Name(),
				Aliases:  id.Aliases(),
				MaxBytes: id.MaxBytes(),
				Upload: func(ctx context.Context, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
					return h.Upload(ctx, id, path, displayName, uid, cb)
				},
			}, true
		}
	}
	return Host{}, false
}

func (h *Hosts) All() []Host {
	var out []Host
	for _, id := range AllHosts {
		if host, ok := h.Host(id); ok {
			out = append(out, host)
		}
	}
	return out
}

func (h *Hosts) Upload(ctx context.Context, host HostID, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
	retries := 3
	if h.cfg != nil && h.cfg.MirrorRetries >= 0 {
		retries = h.cfg.MirrorRetries
	}
	return h.UploadWithRetry(ctx, host, path, displayName, uid, retries, cb)
}

func (h *Hosts) UploadWithRetry(ctx context.Context, host HostID, path, displayName string, uid int64, maxRetries int, cb ProgressFunc) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		link, err := h.uploadOnce(ctx, host, path, displayName, uid, cb)
		if err == nil {
			return link, nil
		}
		lastErr = err
		if attempt == maxRetries {
			break
		}
		retryable, retryAfter := isRetryable(err)
		if !retryable {
			return "", err
		}
		delay := retryAfter
		if delay <= 0 {
			base := 50 * time.Millisecond
			delay = time.Duration(1<<attempt) * base
			jitter := time.Duration(time.Now().UnixNano() % int64(delay/2+1))
			delay += jitter
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", lastErr
}

func (h *Hosts) uploadOnce(ctx context.Context, host HostID, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
	if displayName == "" {
		displayName = filepath.Base(path)
	}
	switch host {
	case GoFile:
		return h.gofile(ctx, path, displayName, uid, cb)
	case FileDitch:
		return h.fileditch(ctx, path, displayName, false, cb)
	case Pixeldrain:
		return h.pixeldrain(ctx, path, displayName, uid, cb)
	default:
		return "", fmt.Errorf("unknown mirror host: %q", host)
	}
}

func (h *Hosts) Pixeldrain(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	return h.Upload(ctx, Pixeldrain, path, "", uid, cb)
}

func (h *Hosts) Gofile(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	return h.Upload(ctx, GoFile, path, "", uid, cb)
}

func (h *Hosts) Fileditch(ctx context.Context, path string, temp bool, cb ProgressFunc) (string, error) {
	return h.fileditch(ctx, path, "", temp, cb)
}

func (h *Hosts) pixeldrain(ctx context.Context, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
	f, size, err := statFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if displayName == "" {
		displayName = filepath.Base(path)
	}
	key := strings.TrimSpace(h.keys.Resolve(uid, string(Pixeldrain)))
	domain := "pixeldrain.com"
	if h.cfg != nil && h.cfg.PixeldrainDomain != "" {
		domain = h.cfg.PixeldrainDomain
	}
	endpoint := fmt.Sprintf("https://%s/api/file/%s", domain, url.PathEscape(displayName))
	if ep := h.endpoints[Pixeldrain]; ep != "" {
		if strings.HasSuffix(ep, "/") {
			endpoint = ep + url.PathEscape(displayName)
		} else {
			endpoint = ep + "/" + url.PathEscape(displayName)
		}
	}
	body := &countingReader{r: f, total: size, cb: cb, ctx: ctx}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	if key != "" {
		req.SetBasicAuth("", key)
	} else {
		slog.Info("pixeldrain: no API key, attempting anonymous upload")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		Success bool   `json:"success"`
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := decode(resp, &out); err != nil {
		return "", fmt.Errorf("pixeldrain upload failed: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("pixeldrain upload failed: %s", out.Message)
	}
	if ep := h.endpoints[Pixeldrain]; ep != "" {
		return fmt.Sprintf("%s/u/%s", strings.TrimRight(ep, "/"), out.ID), nil
	}
	return fmt.Sprintf("https://%s/u/%s", domain, out.ID), nil
}

func (h *Hosts) multipartUpload(ctx context.Context, endpoint, field, path, displayName string, extra map[string]string, hdr map[string]string, cb ProgressFunc, v any) error {
	f, size, err := statFile(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if displayName == "" {
		displayName = filepath.Base(path)
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	cr := &countingReader{r: f, total: size, cb: cb, ctx: ctx}
	go func() {
		var werr error
		defer func() { _ = pw.CloseWithError(werr) }()
		for k, val := range extra {
			if werr = mw.WriteField(k, val); werr != nil {
				return
			}
		}
		part, err := mw.CreateFormFile(field, displayName)
		if err != nil {
			werr = err
			return
		}
		if _, werr = io.Copy(part, cr); werr != nil {
			return
		}
		werr = mw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return err
	}
	return decode(resp, v)
}

func (h *Hosts) gofile(ctx context.Context, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
	tok := strings.TrimSpace(h.keys.Resolve(uid, string(GoFile)))
	hdr := map[string]string{}
	if tok != "" {
		hdr["Authorization"] = "Bearer " + tok
	}
	endpoint := "https://upload.gofile.io/uploadfile"
	if ep := h.endpoints[GoFile]; ep != "" {
		endpoint = ep
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			DownloadPage string `json:"downloadPage"`
		} `json:"data"`
	}
	if err := h.multipartUpload(ctx, endpoint, "file", path, displayName, nil, hdr, cb, &out); err != nil {
		return "", fmt.Errorf("gofile upload failed: %w", err)
	}
	if out.Status != "ok" || out.Data.DownloadPage == "" {
		return "", fmt.Errorf("gofile API returned error status %q", out.Status)
	}
	return out.Data.DownloadPage, nil
}

func (h *Hosts) fileditch(ctx context.Context, path, displayName string, temp bool, cb ProgressFunc) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() == 0 {
		return "", errors.New("file is empty (0 bytes)")
	}
	endpoint := "https://new.fileditch.com/upload.php"
	if temp {
		endpoint = "https://temp.fileditch.com/upload.php"
	}
	if ep := h.endpoints[FileDitch]; ep != "" {
		endpoint = ep
	}
	var out struct {
		Success bool   `json:"success"`
		URL     string `json:"url"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := h.multipartUpload(ctx, endpoint, "files[]", path, displayName, nil, nil, cb, &out); err != nil {
		return "", fmt.Errorf("fileditch upload failed: %w", err)
	}
	u := out.URL
	if u == "" && len(out.Files) > 0 {
		u = out.Files[0].URL
	}
	if !out.Success || u == "" {
		return "", errors.New("fileditch API returned an error")
	}
	return u, nil
}

type Result struct {
	Links    []string
	Uploaded int
	Failed   int
	Total    int
}

func UploadTree(ctx context.Context, root string, fn func(ctx context.Context, path string) (string, error)) (Result, error) {
	st, err := os.Stat(root)
	if err != nil {
		return Result{}, err
	}
	var files []string
	if st.Mode().IsRegular() {
		files = []string{root}
	} else {
		all, _ := fsutil.ListFiles(root)
		for _, f := range all {
			if !strings.HasPrefix(filepath.Base(f), ".") {
				files = append(files, f)
			}
		}
		fsutil.SortNatural(files)
	}
	if len(files) == 0 {
		return Result{}, errors.New("no files found to upload")
	}
	res := Result{Total: len(files)}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		link, err := fn(ctx, f)
		if err != nil {
			slog.Warn("upload failed", "file", filepath.Base(f), "err", err)
			res.Failed++
			continue
		}
		res.Links = append(res.Links, link)
		res.Uploaded++
	}
	return res, nil
}
