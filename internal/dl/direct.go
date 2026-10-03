package dl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/netguard"
	"github.com/burhanverse/tgdl/internal/pacing"
)

const (
	chunkSize = 1 << 20
	userAgent = "Mozilla/5.0 (X11; Linux x86_64)"
)

// ErrDirect wraps direct-download failures surfaced to users.
var ErrDirect = errors.New("direct download failed")

var directExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.mp4 .mkv .avi .mov .webm .flv .wmv .3gp .m4v .ts .f4v .vob .m3u8
		.mp3 .flac .m4a .aac .opus .ogg .wav .wma .alac .aiff
		.zip .rar .7z .tar .gz .bz2 .xz .iso .tgz .tbz2 .zst .cab .dmg
		.apk .exe .bin .msi .deb .rpm .appimage .app .ipa
		.pdf .epub .mobi .djvu .doc .docx .xls .xlsx .ppt .pptx`) {
		directExts[e] = true
	}
}

func cleanURL(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

func urlExt(u string) string {
	p, err := url.Parse(cleanURL(u))
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(p.Path))
}

// IsDirectURL reports whether the job URL looks like a direct file link.
func IsDirectURL(raw string) bool {
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "direct:") {
		return true
	}
	var urls []string
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, u := range list {
				if u = strings.TrimSpace(u); u != "" {
					urls = append(urls, u)
				}
			}
		}
	}
	if len(urls) == 0 {
		for _, f := range strings.Fields(raw) {
			if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
				urls = append(urls, f)
			}
		}
	}
	for _, u := range urls {
		if directExts[urlExt(u)] {
			return true
		}
	}
	return false
}

// Direct downloads plain HTTP(S) files and HLS playlists.
type Direct struct {
	cfg      *config.Config
	client   *http.Client
	Headers  map[string]string
	Progress ProgressFunc

	processed int64
	total     int64
	failed    int
	files     []string
}

// NewDirect constructs a downloader that enforces the SSRF policy in cfg.
func NewDirect(cfg *config.Config, progress ProgressFunc) *Direct {
	return &Direct{
		cfg:      cfg,
		Progress: progress,
		client: netguard.NewClient(netguard.Options{
			AllowPrivate:  cfg.AllowPrivateURLs,
			ForceIPv6:     cfg.ForceIPv6,
			SourceAddress: cfg.SourceAddress,
		}),
	}
}

// Client exposes the guarded HTTP client for sibling engines.
func (d *Direct) Client() *http.Client { return d.client }

func (d *Direct) report(file, u string) {
	if d.Progress != nil {
		d.Progress(Progress{Current: d.processed, Total: d.total, File: file, URL: u})
	}
}

type item struct{ URL, Filename, Subpath string }

// Download fetches one or more URLs into destDir. contents may be a single
// URL, a whitespace-separated list, or a JSON array of URLs.
func (d *Direct) Download(ctx context.Context, destDir, contents string) ([]string, error) {
	items := parseItems(contents)
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: no direct URLs provided", ErrDirect)
	}
	if len(items) > 1 {
		sort.SliceStable(items, func(i, j int) bool { return fsutil.NaturalPathLess(itemKey(items[i]), itemKey(items[j])) })
	}
	return d.DownloadItems(ctx, destDir, items)
}

func itemKey(it item) string {
	if it.Filename != "" || it.Subpath != "" {
		return filepath.Join(it.Subpath, it.Filename)
	}
	if p, err := url.Parse(it.URL); err == nil && path.Base(p.Path) != "." {
		return path.Base(p.Path)
	}
	return it.URL
}

func stripPrefixes(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimPrefix(u, "direct:")
	return strings.TrimPrefix(u, "mirror:")
}

func parseItems(contents string) []item {
	contents = strings.TrimSpace(contents)
	var items []item
	if strings.HasPrefix(contents, "[") {
		var objList []struct {
			URL      string `json:"url"`
			Filename string `json:"filename"`
			Path     string `json:"path"`
		}
		if json.Unmarshal([]byte(contents), &objList) == nil && len(objList) > 0 {
			for _, it := range objList {
				u := stripPrefixes(it.URL)
				if u != "" {
					items = append(items, item{
						URL:      u,
						Filename: it.Filename,
						Subpath:  it.Path,
					})
				}
			}
			if len(items) > 0 {
				return items
			}
		}
		var list []string
		if json.Unmarshal([]byte(contents), &list) == nil {
			for _, u := range list {
				if u = strings.TrimSpace(u); u != "" {
					items = append(items, item{URL: stripPrefixes(u)})
				}
			}
			if len(items) > 0 {
				return items
			}
		}
	}
	var fields []string
	for _, f := range strings.Fields(contents) {
		for _, p := range []string{"http://", "https://", "direct:", "mirror:"} {
			if strings.HasPrefix(f, p) {
				fields = append(fields, f)
				break
			}
		}
	}
	if len(fields) > 1 {
		for _, f := range fields {
			items = append(items, item{URL: stripPrefixes(f)})
		}
		return items
	}
	if contents != "" {
		items = append(items, item{URL: stripPrefixes(contents)})
	}
	return items
}

// DownloadItems downloads each item; it fails only if every item failed.
func (d *Direct) DownloadItems(ctx context.Context, destDir string, items []item) ([]string, error) {
	if destDir == "" {
		return nil, fmt.Errorf("%w: destination directory required", ErrDirect)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	thr := pacing.NewThrottler(pacing.ParseSpeedLimit(d.cfg.GlobalSpeedLimit))
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return d.files, err
		}
		d.report("", it.URL)
		p, err := d.fetchItem(ctx, destDir, it, thr)
		if err != nil {
			if ctx.Err() != nil {
				return d.files, ctx.Err()
			}
			d.failed++
			slog.Error("direct item failed", "url", it.URL, "err", err)
			continue
		}
		d.files = append(d.files, p)
	}
	if d.failed == len(items) {
		return nil, fmt.Errorf("%w: all %d downloads failed", ErrDirect, len(items))
	}
	fsutil.SortNatural(d.files)
	return d.files, nil
}

var cdFilenameRE = regexp.MustCompile(`(?i)filename\*?=(?:UTF-8'')?"?([^";]+)"?`)

// FilenameFromResponse derives a safe filename from headers or the URL path.
func FilenameFromResponse(rawURL string, h http.Header) string {
	if h != nil {
		if cd := h.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
				return fsutil.SanitizeFilename(params["filename"])
			}
			if m := cdFilenameRE.FindStringSubmatch(cd); m != nil {
				if fn, err := url.PathUnescape(m[1]); err == nil && strings.TrimSpace(fn) != "" {
					return fsutil.SanitizeFilename(fn)
				}
			}
		}
	}
	if p, err := url.Parse(rawURL); err == nil {
		if fn, err := url.PathUnescape(path.Base(p.Path)); err == nil {
			fn = strings.TrimSpace(fn)
			if fn != "" && fn != "/" && fn != "." {
				return fsutil.SanitizeFilename(fn)
			}
		}
	}
	return fmt.Sprintf("direct_file_%d.bin", time.Now().Unix())
}

func (d *Direct) newRequest(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (d *Direct) fetchItem(ctx context.Context, destDir string, it item, thr *pacing.Throttler) (string, error) {
	if !d.cfg.AllowPrivateURLs {
		if err := netguard.CheckURL(ctx, it.URL); err != nil {
			return "", err
		}
	}
	saveDir := destDir
	if it.Subpath != "" {
		sd, err := fsutil.SafeJoin(destDir, it.Subpath)
		if err != nil {
			return "", err
		}
		saveDir = sd
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return "", err
	}

	if urlExt(it.URL) == ".m3u8" {
		name := it.Filename
		if name == "" {
			name = FilenameFromResponse(it.URL, nil)
		}
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".mp4"
		return d.downloadHLS(ctx, it.URL, filepath.Join(saveDir, fsutil.SanitizeFilename(name)))
	}

	req, err := d.newRequest(ctx, it.URL)
	if err != nil {
		return "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d - %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	filename := it.Filename
	if filename == "" {
		filename = FilenameFromResponse(resp.Request.URL.String(), resp.Header)
	}
	filename = fsutil.SanitizeFilename(filename)
	ext := strings.ToLower(filepath.Ext(filename))
	textExt := map[string]bool{".html": true, ".htm": true, ".txt": true, ".json": true, ".xml": true, ".xhtml": true}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	if (ct == "text/html" || ct == "text/plain") && !textExt[ext] {
		return "", fmt.Errorf("expected a file but got an HTML/text response (Content-Type: %s): the URL may be restricted, expired, or need authentication", ct)
	}

	br := bufio.NewReaderSize(resp.Body, chunkSize)
	peek, _ := br.Peek(512)
	if len(peek) > 0 && !textExt[ext] {
		head := strings.ToLower(strings.TrimLeft(string(peek), " \t\r\n\ufeff"))
		if strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.HasPrefix(head, "<?xml") {
			return "", errors.New("expected a file but got an HTML/text response: the URL may be restricted, expired, or need authentication")
		}
	}

	if resp.ContentLength > 0 {
		d.total += resp.ContentLength
	}
	out := filepath.Join(saveDir, filename)
	part := out + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(part) }

	buf := make([]byte, chunkSize)
	for {
		n, rerr := br.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				cleanup()
				return "", werr
			}
			if terr := thr.Consume(ctx, n); terr != nil {
				cleanup()
				return "", terr
			}
			d.processed += int64(n)
			d.report(filename, it.URL)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			cleanup()
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, out); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return out, nil
}
