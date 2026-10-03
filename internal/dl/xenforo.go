package dl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/netguard"
)

var (
	simpcityRE = regexp.MustCompile(`simpcity\.(cr|is|cz|hk|rs|ax|su|st|top|to)\b`)
	threadRE   = regexp.MustCompile(`/threads/[a-zA-Z0-9._-]+\.\d+`)
	postRE     = regexp.MustCompile(`/posts/\d+`)
	xfPrefixes = []string{"xenforo:", "simpcity:", "forum:", "fpd:"}
)

// IsXenforoURL reports whether the URL looks like a XenForo thread/post.
func IsXenforoURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	if u == "" {
		return false
	}
	for _, p := range xfPrefixes {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return simpcityRE.MatchString(u) || threadRE.MatchString(u) || postRE.MatchString(u)
}

// StripXenforoPrefix removes xenforo:/simpcity:/forum:/fpd: schemes.
func StripXenforoPrefix(s string) string {
	for _, p := range xfPrefixes {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

// Scraper is the JSON-RPC sidecar used to resolve forum media.
type Scraper interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	EnsureStarted(ctx context.Context)
}

// Xenforo downloads media resolved from XenForo forum threads.
type Xenforo struct {
	cfg     *config.Config
	scraper Scraper
	client  *http.Client
}

func NewXenforo(cfg *config.Config, s Scraper) *Xenforo {
	return &Xenforo{cfg: cfg, scraper: s, client: netguard.NewClient(netguard.Options{
		AllowPrivate: cfg.AllowPrivateURLs, ForceIPv6: cfg.ForceIPv6, SourceAddress: cfg.SourceAddress,
	})}
}

func (x *Xenforo) cookiesText(uid int64) string {
	cands := []string{}
	if uid > 0 {
		cands = append(cands, filepath.Join(x.cfg.AuthDir, strconv.FormatInt(uid, 10), "cookies.txt"))
	}
	cands = append(cands, filepath.Join(x.cfg.AuthDir, "cookies.txt"), "cookies.txt")
	for _, c := range cands {
		if b, err := os.ReadFile(c); err == nil {
			return string(b)
		}
	}
	return ""
}

type xfResource struct {
	ResolvedURL string            `json:"resolvedUrl"`
	FolderName  string            `json:"folderName"`
	Filename    string            `json:"filename"`
	Headers     map[string]string `json:"headers"`
}

type xfScrape struct {
	ThreadTitle string `json:"threadTitle"`
	Posts       []struct {
		Resources []xfResource `json:"resources"`
	} `json:"posts"`
}

// XenforoOpts are parsed from the job's extra args.
type XenforoOpts struct {
	Passwords []string
	MaxPages  int
	UserAgent string
}

// ParseXenforoArgs extracts password/max-pages/UA flags from CLI-style args.
func ParseXenforoArgs(args []string) XenforoOpts {
	o := XenforoOpts{MaxPages: 1}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return strings.TrimSpace(args[i]), true
			}
			return "", false
		}
		switch {
		case a == "-p" || a == "--password" || a == "-pass":
			if v, ok := next(); ok {
				o.Passwords = append(o.Passwords, v)
			}
		case strings.HasPrefix(a, "--password=") || strings.HasPrefix(a, "-p=") || strings.HasPrefix(a, "--pass="):
			o.Passwords = append(o.Passwords, strings.TrimSpace(a[strings.Index(a, "=")+1:]))
		case a == "--max-pages" || a == "-pages":
			if v, ok := next(); ok {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					o.MaxPages = n
				}
			}
		case a == "-ua" || a == "--ua" || a == "--user-agent":
			if v, ok := next(); ok {
				o.UserAgent = v
			}
		case strings.HasPrefix(a, "--ua=") || strings.HasPrefix(a, "--user-agent="):
			o.UserAgent = strings.TrimSpace(a[strings.Index(a, "=")+1:])
		}
	}
	return o
}

// Download scrapes the thread and downloads all resolved media into dest.
func (x *Xenforo) Download(ctx context.Context, target, dest string, uid int64, o XenforoOpts, on CountFunc) Result {
	slog.Info("xenforo download", "url", target, "user", uid)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Result{ErrorTail: err.Error()}
	}
	if !x.cfg.AllowPrivateURLs {
		if err := netguard.CheckURL(ctx, target); err != nil {
			return Result{ErrorTail: err.Error()}
		}
	}
	x.scraper.EnsureStarted(ctx)
	cookies := x.cookiesText(uid)
	ua := ResolveDeviceUA(x.cfg.AuthDir, uid, cookies, o.UserAgent)
	params := map[string]any{"url": target, "cookies": cookies, "userAgent": ua, "passwords": o.Passwords, "maxPages": o.MaxPages}
	if uid > 0 {
		params["user_id"] = strconv.FormatInt(uid, 10)
	}
	raw, err := x.scraper.Call(ctx, "xenforo.scrape", params)
	if err != nil {
		return Result{ErrorTail: "Scraper error: " + err.Error()}
	}
	var data xfScrape
	if err := json.Unmarshal(raw, &data); err != nil {
		return Result{ErrorTail: "Invalid response from scraper service."}
	}
	title := data.ThreadTitle
	if title == "" {
		title = "Thread"
	}
	var items []xfResource
	for _, p := range data.Posts {
		for _, r := range p.Resources {
			if r.ResolvedURL != "" {
				items = append(items, r)
			}
		}
	}
	if len(data.Posts) == 0 {
		return Result{ErrorTail: "No posts or downloadable media found."}
	}
	if len(items) == 0 {
		return Result{ErrorTail: "No downloadable media resources resolved."}
	}
	slog.Info("xenforo resources", "count", len(items), "posts", len(data.Posts), "title", title)

	var files []string
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		folder := it.FolderName
		if folder == "" {
			folder = title
		}
		sub, err := fsutil.SafeJoin(dest, fsutil.SanitizeFilename(folder))
		if err != nil {
			continue
		}
		p, err := x.fetch(ctx, it, sub, ua)
		if err != nil {
			slog.Warn("xenforo item failed", "url", it.ResolvedURL, "err", err)
			continue
		}
		files = append(files, p)
		if on != nil {
			on(len(files), filepath.Base(p), it.ResolvedURL)
		}
	}
	fsutil.SortNatural(files)
	if len(files) == 0 {
		return Result{ErrorTail: "All media downloads failed."}
	}
	return Result{OK: true, Files: files}
}

func (x *Xenforo) fetch(ctx context.Context, it xfResource, dir, ua string) (string, error) {
	if !x.cfg.AllowPrivateURLs {
		if err := netguard.CheckURL(ctx, it.ResolvedURL); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ref := it.Headers["Referer"]
	if ref == "" {
		ref = it.ResolvedURL
	}
	hdr := DeviceHeaders(ua, "image", ref)
	for k, v := range it.Headers {
		hdr[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, it.ResolvedURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := x.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d fetching media", resp.StatusCode)
	}
	name := it.Filename
	if name == "" {
		name = FilenameFromResponse(resp.Request.URL.String(), resp.Header)
	}
	name = fsutil.SanitizeFilename(name)
	target := filepath.Join(dir, name)
	stem, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	for i := 1; ; i++ {
		if _, err := os.Stat(target); err != nil {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	f, err := os.Create(target)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(target)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

var (
	_ = errors.New
	_ = url.Parse
)
