package upload

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

func (h *Hosts) Pixeldrain(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	return h.Upload(ctx, Pixeldrain, path, "", uid, cb)
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
