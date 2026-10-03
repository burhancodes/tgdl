// Package torrent wraps the aria2 RPC daemon and the Magnetio search sidecar.
package torrent

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

var trackerSources = []string{
	"https://raw.githubusercontent.com/XIU2/TrackersListCollection/master/best.txt",
	"https://raw.githubusercontent.com/XIU2/TrackersListCollection/master/all.txt",
	"https://fastly.jsdelivr.net/gh/XIU2/TrackersListCollection/best.txt",
	"https://fastly.jsdelivr.net/gh/XIU2/TrackersListCollection/all.txt",
	"https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_best.txt",
}

var defaultTrackers = []string{
	"http://1337.abcvg.info:80/announce",
	"http://bt1.archive.org:6969/announce",
	"http://bt2.archive.org:6969/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://tracker.internetwarriors.net:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://tracker.cyberia.is:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"udp://tracker.dump.cl:6969/announce",
	"udp://tracker.dler.org:6969/announce",
}

// Trackers maintains a cached public tracker list.
type Trackers struct {
	mu        sync.RWMutex
	list      []string
	cacheFile string
	client    *http.Client
}

func NewTrackers(dataDir string) *Trackers {
	return &Trackers{
		cacheFile: filepath.Join(dataDir, "trackers_cache.txt"),
		client:    &http.Client{Timeout: 8 * time.Second},
	}
}

func parseLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// List returns the cached list, loading the on-disk cache or defaults lazily.
func (t *Trackers) List() []string {
	t.mu.RLock()
	if len(t.list) > 0 {
		defer t.mu.RUnlock()
		return append([]string(nil), t.list...)
	}
	t.mu.RUnlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.list) == 0 {
		if b, err := os.ReadFile(t.cacheFile); err == nil {
			t.list = parseLines(string(b))
		}
		if len(t.list) == 0 {
			t.list = append([]string(nil), defaultTrackers...)
		}
	}
	return append([]string(nil), t.list...)
}

// Refresh downloads and merges the latest public trackers, updating the cache.
func (t *Trackers) Refresh(ctx context.Context) {
	set := map[string]struct{}{}
	for _, src := range trackerSources {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			continue
		}
		resp, err := t.client.Do(req)
		if err != nil {
			slog.Warn("tracker list fetch failed", "url", src, "err", err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			for _, l := range parseLines(string(b)) {
				set[l] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		return
	}
	list := make([]string, 0, len(set))
	for k := range set {
		list = append(list, k)
	}
	sort.Strings(list)
	t.mu.Lock()
	t.list = list
	t.mu.Unlock()
	if err := os.WriteFile(t.cacheFile, []byte(strings.Join(list, "\n")), 0o644); err != nil {
		slog.Warn("write trackers cache failed", "err", err)
		return
	}
	slog.Info("updated tracker list", "count", len(list))
}

// CSV returns trackers formatted for aria2's --bt-tracker.
func (t *Trackers) CSV() string { return strings.Join(t.List(), ",") }

// AddToMagnet appends trackers that are not already present in the magnet URI.
func (t *Trackers) AddToMagnet(magnet string) string {
	if !strings.HasPrefix(magnet, "magnet:") {
		return magnet
	}
	u, err := url.Parse(magnet)
	if err != nil {
		return magnet
	}
	q := u.Query()
	have := map[string]bool{}
	for _, tr := range q["tr"] {
		have[tr] = true
	}
	for _, tr := range t.List() {
		if !have[tr] {
			q.Add("tr", tr)
			have[tr] = true
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
