package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxFileDitchAnonymousBytes = int64(150) << 30
	maxFileDitchPremiumBytes   = int64(250) << 30
)

type FileDitchStatusState string

const (
	FileDitchStatusExists  FileDitchStatusState = "exists"
	FileDitchStatusGone    FileDitchStatusState = "gone"
	FileDitchStatusUnknown FileDitchStatusState = "unknown"
)

type FileDitchStatus struct {
	File      string               `json:"file"`
	State     FileDitchStatusState `json:"-"`
	Size      *int64               `json:"size"`
	Views     *int64               `json:"views"`
	ShortLink *string              `json:"short_link"`
}

type fileDitchStatusJSON struct {
	File      string          `json:"file"`
	Status    json.RawMessage `json:"status"`
	Size      *int64          `json:"size"`
	Views     *int64          `json:"views"`
	ShortLink *string         `json:"short_link"`
	Error     string          `json:"error"`
}

type FileDitchItem struct {
	URL        string  `json:"url"`
	Filename   string  `json:"filename"`
	Size       int64   `json:"size"`
	Uploaded   int64   `json:"uploaded"`
	Folder     string  `json:"folder"`
	Thumbnail  *string `json:"thumbnail"`
	DurationMS *int64  `json:"duration_ms"`
	Width      *int    `json:"width"`
	Height     *int    `json:"height"`
	Hotlink    *string `json:"hotlink"`
}

type FileDitchListResult struct {
	Files   []FileDitchItem `json:"files"`
	Count   int             `json:"count"`
	Next    *string         `json:"next"`
	Success bool            `json:"success"`
}

type FileDitchDeleteResultItem struct {
	URL    string `json:"url"`
	Folder string `json:"folder"`
	Status string `json:"status"`
}

type FileDitchDeleteResult struct {
	Success bool                        `json:"success"`
	Results []FileDitchDeleteResultItem `json:"results"`
}

func (h *Hosts) Fileditch(ctx context.Context, path string, temp bool, cb ProgressFunc) (string, error) {
	return h.fileditch(ctx, path, "", 0, cb)
}

func (h *Hosts) fileditch(ctx context.Context, path, displayName string, uid int64, cb ProgressFunc) (string, error) {
	f, size, err := statFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if size == 0 {
		return "", errors.New("file is empty (0 bytes)")
	}
	if size > maxFileDitchPremiumBytes {
		return "", fmt.Errorf("file size %d exceeds FileDitch max limit of 250 GB", size)
	}

	key := strings.TrimSpace(h.keys.Resolve(uid, string(FileDitch)))
	if size > maxFileDitchAnonymousBytes && key == "" {
		return "", fmt.Errorf("file size %d exceeds FileDitch 150 GB limit (login token required)", size)
	}

	if displayName == "" {
		displayName = filepath.Base(path)
	}

	endpoint := "https://new.fileditch.com/upload.php"
	if ep := h.endpoints[FileDitch]; ep != "" {
		endpoint = ep
	}

	targetURL, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := targetURL.Query()
	q.Set("filename", displayName)
	targetURL.RawQuery = q.Encode()

	body := &countingReader{r: f, total: size, cb: cb, ctx: ctx}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL.String(), body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Filename", displayName)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}

	var out struct {
		Success  bool   `json:"success"`
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		Account  string `json:"account"`
		Error    string `json:"error"`
		Files    []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := decode(resp, &out); err != nil {
		return "", fmt.Errorf("fileditch upload failed: %w", err)
	}
	u := out.URL
	if u == "" && len(out.Files) > 0 {
		u = out.Files[0].URL
	}
	if !out.Success || u == "" {
		if out.Error != "" {
			return "", fmt.Errorf("fileditch upload failed: %s", out.Error)
		}
		return "", errors.New("fileditch API returned an error")
	}
	return u, nil
}

func FileDitchStatusURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", errors.New("invalid url: missing host")
	}
	p := parsed.Path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasPrefix(p, "/api/") && p != "/api" {
		parsed.Path = "/api" + p
	}
	return parsed.String(), nil
}

func (h *Hosts) FileditchStatus(ctx context.Context, fileURL string) (*FileDitchStatus, error) {
	statusURL, err := FileDitchStatusURL(fileURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fileditch status returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var raw fileDitchStatusJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("invalid fileditch status response: %w", err)
	}
	if raw.Error != "" {
		return nil, fmt.Errorf("fileditch status error: %s", raw.Error)
	}
	state := FileDitchStatusUnknown
	switch strings.TrimSpace(string(raw.Status)) {
	case "true":
		state = FileDitchStatusExists
	case "false":
		state = FileDitchStatusGone
	case `"unknown"`, "unknown":
		state = FileDitchStatusUnknown
	}
	return &FileDitchStatus{
		File:      raw.File,
		State:     state,
		Size:      raw.Size,
		Views:     raw.Views,
		ShortLink: raw.ShortLink,
	}, nil
}

func (h *Hosts) FileditchList(ctx context.Context, uid int64, limit int, after string) (*FileDitchListResult, error) {
	key := strings.TrimSpace(h.keys.Resolve(uid, string(FileDitch)))
	if key == "" {
		return nil, errors.New("fileditch list requires login token")
	}
	endpoint := "https://new.fileditch.com/list.php"
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if after != "" {
		q.Set("after", after)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	var out FileDitchListResult
	if err := decode(resp, &out); err != nil {
		return nil, fmt.Errorf("fileditch list failed: %w", err)
	}
	return &out, nil
}

func (h *Hosts) FileditchDelete(ctx context.Context, uid int64, urls ...string) (*FileDitchDeleteResult, error) {
	if len(urls) == 0 {
		return nil, errors.New("no urls provided for deletion")
	}
	key := strings.TrimSpace(h.keys.Resolve(uid, string(FileDitch)))
	if key == "" {
		return nil, errors.New("fileditch delete requires login token")
	}
	endpoint := "https://new.fileditch.com/delete.php"
	bodyBytes, err := json.Marshal(map[string][]string{"urls": urls})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	var out FileDitchDeleteResult
	if err := decode(resp, &out); err != nil {
		return nil, fmt.Errorf("fileditch delete failed: %w", err)
	}
	return &out, nil
}
