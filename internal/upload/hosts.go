package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
)

// ProgressFunc reports (uploadedBytes, totalBytes).
type ProgressFunc func(current, total int64)

// Hosts uploads files to Pixeldrain, GoFile and FileDitch.
type Hosts struct {
	cfg    *config.Config
	keys   *Keys
	client *http.Client
}

func NewHosts(cfg *config.Config, keys *Keys) *Hosts {
	return &Hosts{cfg: cfg, keys: keys, client: &http.Client{Timeout: 0}}
}

// countingReader reports progress as bytes are consumed by the HTTP client.
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
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	return nil
}

// Pixeldrain uploads via PUT /api/file/{name}. It returns the public URL.
func (h *Hosts) Pixeldrain(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	f, size, err := statFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	key := strings.TrimSpace(h.keys.Resolve(uid, "pixeldrain"))
	domain := h.cfg.PixeldrainDomain
	body := &countingReader{r: f, total: size, cb: cb, ctx: ctx}
	u := fmt.Sprintf("https://%s/api/file/%s", domain, url.PathEscape(filepath.Base(path)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, body)
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
	return fmt.Sprintf("https://%s/u/%s", domain, out.ID), nil
}

func (h *Hosts) multipartUpload(ctx context.Context, endpoint, field, path string, extra map[string]string, hdr map[string]string, cb ProgressFunc, v any) error {
	f, size, err := statFile(path)
	if err != nil {
		return err
	}
	defer f.Close()

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
		part, err := mw.CreateFormFile(field, filepath.Base(path))
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

// Gofile uploads to GoFile and returns the download page URL.
func (h *Hosts) Gofile(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	tok := strings.TrimSpace(h.keys.Resolve(uid, "gofile"))
	hdr := map[string]string{}
	if tok != "" {
		hdr["Authorization"] = "Bearer " + tok
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			DownloadPage string `json:"downloadPage"`
		} `json:"data"`
	}
	if err := h.multipartUpload(ctx, "https://upload.gofile.io/uploadfile", "file", path, nil, hdr, cb, &out); err != nil {
		return "", fmt.Errorf("gofile upload failed: %w", err)
	}
	if out.Status != "ok" || out.Data.DownloadPage == "" {
		return "", fmt.Errorf("gofile API returned error status %q", out.Status)
	}
	return out.Data.DownloadPage, nil
}

// Fileditch uploads to FileDitch (permanent, or the 72h temp host).
func (h *Hosts) Fileditch(ctx context.Context, path string, temp bool, cb ProgressFunc) (string, error) {
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
	var out struct {
		Success bool   `json:"success"`
		URL     string `json:"url"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := h.multipartUpload(ctx, endpoint, "files[]", path, nil, nil, cb, &out); err != nil {
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

// Result summarises a batch upload.
type Result struct {
	Links    []string
	Uploaded int
	Failed   int
	Total    int
}

// UploadTree uploads a file or every file beneath a directory (natural order,
// dotfiles skipped) with the given single-file uploader.
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

var _ = time.Second
