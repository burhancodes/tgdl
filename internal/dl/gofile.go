package dl

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
)

var (
	gofileHosts    = []string{"gofile.io", "www.gofile.io", "gofile.co", "www.gofile.co"}
	gofileIgnore   = []string{"/api/", "/public/", "/static/", "/cdn/", "/download"}
	gofileIDRE     = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)
	gofilePrefixes = []string{"gofile:", "gf:", "gf2tg:", "gfdl:"}
	gofileShare    = []*regexp.Regexp{
		regexp.MustCompile(`(?i)/d/([A-Za-z0-9_-]{4,40})(/.*)?$`),
		regexp.MustCompile(`(?i)/f/([A-Za-z0-9_-]{4,40})(/.*)?$`),
		regexp.MustCompile(`(?i)/file/d/([A-Za-z0-9_-]{4,40})(/.*)?$`),
		regexp.MustCompile(`(?i)[?&]file=([A-Za-z0-9_-]{4,40})`),
		regexp.MustCompile(`(?i)/([A-Za-z0-9_-]{4,40})(?:/.*)?$`),
	}
	bypassPathRE = regexp.MustCompile(`^/([A-Za-z0-9_-]{4,40})(/.*)?$`)
	fileQueryRE  = regexp.MustCompile(`(?i)[?&]file=`)
)

// StripGofilePrefix removes any of the gofile:/gf:/gf2tg:/gfdl: schemes.
func StripGofilePrefix(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range gofilePrefixes {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

func hostAllowed(host string) bool {
	if host == "" {
		return true
	}
	for _, h := range gofileHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return strings.Contains(host, ".gofile.")
}

func isBypassHost(host, bypass string) bool {
	bypass = strings.ToLower(bypass)
	return host == bypass || strings.HasSuffix(host, "."+bypass)
}

// IsGofileURL reports whether the URL is a GoFile share or bypass link.
func IsGofileURL(cfg *config.Config, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, p := range gofilePrefixes {
		if strings.HasPrefix(raw, p) {
			return true
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return gofileIDRE.MatchString(raw)
	}
	return isBypassHost(host, cfg.GofileBypassHost) || (host != "" && hostAllowed(host))
}

// ExtractGofileInfo returns (contentID, trailingPath) for a share URL.
func ExtractGofileInfo(cfg *config.Config, raw string) (string, string, bool) {
	clean := StripGofilePrefix(raw)
	if clean == "" {
		return "", "", false
	}
	if gofileIDRE.MatchString(clean) {
		return clean, "", true
	}
	u, err := url.Parse(clean)
	if err != nil {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	path := u.Path
	if path == "" {
		path = "/"
	}
	lower := strings.ToLower(path)
	if (lower == "/" || lower == "/index.html") && !fileQueryRE.MatchString(clean) {
		return "", "", false
	}
	if strings.Contains(strings.ToLower(u.RawQuery), "noredirect") {
		return "", "", false
	}
	for _, p := range gofileIgnore {
		if strings.HasPrefix(lower, p) {
			return "", "", false
		}
	}
	trim := func(t string) string {
		if t == "/" {
			return ""
		}
		return t
	}
	if isBypassHost(host, cfg.GofileBypassHost) {
		if m := bypassPathRE.FindStringSubmatch(path); m != nil {
			return m[1], trim(m[2]), true
		}
	}
	if !hostAllowed(host) {
		return "", "", false
	}
	for _, re := range gofileShare {
		m := re.FindStringSubmatch(path)
		if m == nil {
			m = re.FindStringSubmatch(clean)
		}
		if m != nil && gofileIDRE.MatchString(m[1]) {
			t := ""
			if len(m) > 2 {
				t = trim(m[2])
			}
			return m[1], t, true
		}
	}
	return "", "", false
}

// GofileBypassURL rewrites a share link to the bypass streaming host.
func GofileBypassURL(cfg *config.Config, raw string) string {
	cid, trailing, ok := ExtractGofileInfo(cfg, raw)
	if !ok {
		return raw
	}
	if trailing != "" && !strings.HasPrefix(trailing, "/") {
		trailing = "/" + trailing
	}
	clean := StripGofilePrefix(raw)
	out := url.URL{Scheme: "https", Host: cfg.GofileBypassHost, Path: "/" + cid + trailing}
	if u, err := url.Parse(clean); err == nil {
		q := u.Query()
		q.Del("noredirect")
		out.RawQuery = q.Encode()
		out.Fragment = u.Fragment
	}
	return out.String()
}

// DownloadGofile downloads GoFile links through the bypass host.
func DownloadGofile(ctx context.Context, cfg *config.Config, dest, contents string, progress ProgressFunc) ([]string, error) {
	contents = strings.TrimSpace(contents)
	var raw []string
	if strings.HasPrefix(contents, "[") {
		_ = json.Unmarshal([]byte(contents), &raw)
	}
	if len(raw) == 0 {
		raw = strings.Fields(contents)
	}
	var items []item
	for _, r := range raw {
		if r = strings.TrimSpace(r); r != "" {
			items = append(items, item{URL: GofileBypassURL(cfg, r)})
		}
	}
	if len(items) == 0 {
		return nil, errors.New("no valid GoFile URLs provided for download")
	}
	sort.SliceStable(items, func(i, j int) bool { return fsutil.NaturalPathLess(itemKey(items[i]), itemKey(items[j])) })
	d := NewDirect(cfg, progress)
	files, err := d.DownloadItems(ctx, dest, items)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool { return fsutil.NaturalPathLess(files[i], files[j]) })
	return files, nil
}

var _ = filepath.Join
