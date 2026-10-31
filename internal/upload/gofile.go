package upload

import (
	"context"
	"fmt"
	"strings"
)

func (h *Hosts) Gofile(ctx context.Context, path string, uid int64, cb ProgressFunc) (string, error) {
	return h.Upload(ctx, GoFile, path, "", uid, cb)
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
