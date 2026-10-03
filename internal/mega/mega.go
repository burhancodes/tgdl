// Package mega downloads public MEGA file and folder links. It implements the
// public-link protocol directly (no account required): key derivation,
// AES-CBC attribute decryption and AES-CTR content decryption.
package mega

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
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
	"strings"
	"sync/atomic"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/pacing"
)

const apiURL = "https://g.api.mega.co.nz/cs"

// IsMegaURL reports whether s is a MEGA link.
func IsMegaURL(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "mega:") {
		return true
	}
	for _, d := range []string{"mega.nz", "mega.co.nz", "mega.io"} {
		if strings.Contains(s, d) {
			return true
		}
	}
	return false
}

// ProgressFunc reports cumulative bytes, speed (B/s), and the current file.
type ProgressFunc func(downloaded int64, speed float64, filename string)

type link struct {
	handle   string
	key      []byte
	isFolder bool
	selected string
}

var (
	newFmt    = regexp.MustCompile(`^/(file|folder)/([A-Za-z0-9_-]{8})(?:/(file|folder)/([A-Za-z0-9_-]{8}))?$`)
	legacyFmt = regexp.MustCompile(`^#(F?)!([A-Za-z0-9_-]{8})!([A-Za-z0-9_-]+)`)
)

func b64(s string) ([]byte, error) {
	s = strings.NewReplacer("-", "+", "_", "/", ",", "").Replace(s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

func parseLink(raw string) (*link, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "mega:")
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	l := &link{}
	if m := newFmt.FindStringSubmatch(u.Path); m != nil {
		l.isFolder = m[1] == "folder"
		l.handle = m[2]
		l.selected = m[4]
		if u.Fragment == "" {
			return nil, errors.New("MEGA link is missing its decryption key")
		}
		key := u.Fragment
		if i := strings.Index(key, "/"); i >= 0 {
			key = key[:i]
		}
		if l.key, err = b64(key); err != nil {
			return nil, fmt.Errorf("invalid MEGA key: %w", err)
		}
		return l, nil
	}
	if m := legacyFmt.FindStringSubmatch("#" + strings.TrimPrefix(u.Fragment, "#")); m != nil {
		l.isFolder, l.handle = m[1] == "F", m[2]
		if l.key, err = b64(m[3]); err != nil {
			return nil, fmt.Errorf("invalid MEGA key: %w", err)
		}
		return l, nil
	}
	return nil, fmt.Errorf("unrecognised MEGA link format: %q", raw)
}

// ---- crypto --------------------------------------------------------------

func fileKey(k []byte) (aesKey, nonce []byte, err error) {
	if len(k) != 32 {
		return nil, nil, fmt.Errorf("unexpected file key length %d", len(k))
	}
	aesKey = make([]byte, 16)
	for i := range aesKey {
		aesKey[i] = k[i] ^ k[i+16]
	}
	return aesKey, k[16:24], nil
}

func cbcDecrypt(key, data []byte) ([]byte, error) {
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("attribute block not aligned")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(out, data)
	return out, nil
}

func ecbDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("key block not aligned")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	return out, nil
}

func attrName(key []byte, enc string) (string, error) {
	raw, err := b64(enc)
	if err != nil {
		return "", err
	}
	dec, err := cbcDecrypt(key, raw)
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(dec, []byte("MEGA")) {
		return "", errors.New("attribute decryption failed (wrong key?)")
	}
	dec = bytes.TrimRight(dec[4:], "\x00")
	var a struct {
		N string `json:"n"`
	}
	if err := json.Unmarshal(dec, &a); err != nil {
		return "", err
	}
	return a.N, nil
}

// ---- API -----------------------------------------------------------------

// Downloader downloads public MEGA links.
type Downloader struct {
	cfg      *config.Config
	http     *http.Client
	Progress ProgressFunc

	total int64
	start time.Time
	seq   atomic.Int64
}

func NewDownloader(cfg *config.Config, p ProgressFunc) *Downloader {
	return &Downloader{cfg: cfg, Progress: p, http: &http.Client{Timeout: 0}}
}

func (d *Downloader) api(ctx context.Context, folder string, req any, out any) error {
	body, _ := json.Marshal([]any{req})
	q := url.Values{"id": {fmt.Sprint(d.seq.Add(1))}}
	if folder != "" {
		q.Set("n", folder)
	}
	for attempt := 0; attempt < 5; attempt++ {
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"?"+q.Encode(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		hreq.Header.Set("Content-Type", "application/json")
		resp, err := d.http.Do(hreq)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		var code int
		if json.Unmarshal(raw, &code) == nil { // bare error integer
			if code == -3 { // EAGAIN
				if err := pacing.Sleep(ctx, time.Duration(1<<attempt)*250*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			return apiErr(code)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
			return errors.New("invalid MEGA API response")
		}
		if json.Unmarshal(arr[0], &code) == nil {
			if code == -3 {
				if err := pacing.Sleep(ctx, time.Duration(1<<attempt)*250*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			return apiErr(code)
		}
		return json.Unmarshal(arr[0], out)
	}
	return errors.New("MEGA API kept returning EAGAIN")
}

func apiErr(code int) error {
	switch code {
	case -9, -8:
		return errors.New("MEGA link not found or has been removed")
	case -16:
		return errors.New("MEGA link was taken down (terms of service violation)")
	case -17:
		return errors.New("MEGA transfer quota exceeded; try again later")
	case -14, -15:
		return errors.New("MEGA decryption key is invalid")
	}
	return fmt.Errorf("MEGA API error %d", code)
}

type gResp struct {
	Size int64  `json:"s"`
	At   string `json:"at"`
	G    string `json:"g"`
}

type node struct {
	H string `json:"h"`
	P string `json:"p"`
	T int    `json:"t"`
	A string `json:"a"`
	K string `json:"k"`
	S int64  `json:"s"`
}

type fileNode struct {
	handle, rel string
	size        int64
	key         []byte
	name        string
}

// DownloadLinks downloads each link (JSON array / whitespace list / single).
func (d *Downloader) DownloadLinks(ctx context.Context, raw, dest string) ([]string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	var urls []string
	if strings.HasPrefix(raw, "[") {
		_ = json.Unmarshal([]byte(raw), &urls)
	}
	if len(urls) == 0 {
		for _, f := range strings.Fields(raw) {
			if strings.HasPrefix(f, "mega:") || IsMegaURL(f) || strings.HasPrefix(f, "http") {
				urls = append(urls, f)
			}
		}
	}
	if len(urls) == 0 && raw != "" {
		urls = []string{raw}
	}
	d.start, d.total = time.Now(), 0
	failed := 0
	var lastErr error
	for _, u := range urls {
		if err := d.downloadURL(ctx, strings.TrimPrefix(u, "mega:"), dest); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed++
			lastErr = err
			slog.Error("mega download failed", "url", u, "err", err)
		}
	}
	if len(urls) > 0 && failed == len(urls) {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("all %d MEGA downloads failed", len(urls))
	}
	files, _ := fsutil.ListFiles(dest)
	fsutil.SortNatural(files)
	return files, nil
}

func (d *Downloader) downloadURL(ctx context.Context, raw, dest string) error {
	l, err := parseLink(raw)
	if err != nil {
		return err
	}
	if !l.isFolder {
		var g gResp
		if err := d.api(ctx, "", map[string]any{"a": "g", "g": 1, "p": l.handle}, &g); err != nil {
			return err
		}
		key, _, err := fileKey(l.key)
		if err != nil {
			return err
		}
		name, err := attrName(key, g.At)
		if err != nil {
			return err
		}
		return d.fetch(ctx, g.G, l.key, filepath.Join(dest, fsutil.SanitizeFilename(name)), g.Size)
	}
	return d.downloadFolder(ctx, l, dest)
}

func (d *Downloader) downloadFolder(ctx context.Context, l *link, dest string) error {
	var listing struct {
		F []node `json:"f"`
	}
	if err := d.api(ctx, l.handle, map[string]any{"a": "f", "c": 1, "r": 1}, &listing); err != nil {
		return err
	}
	if len(l.key) != 16 {
		return fmt.Errorf("unexpected folder key length %d", len(l.key))
	}
	type dec struct {
		n    node
		key  []byte
		name string
	}
	byHandle := map[string]*dec{}
	for _, n := range listing.F {
		if n.T > 1 {
			continue
		}
		var enc string
		for _, part := range strings.Split(n.K, "/") {
			if i := strings.Index(part, ":"); i >= 0 {
				enc = part[i+1:]
				break
			}
		}
		if enc == "" {
			continue
		}
		raw, err := b64(enc)
		if err != nil {
			continue
		}
		k, err := ecbDecrypt(l.key, raw)
		if err != nil {
			continue
		}
		attrKey := k
		if n.T == 0 {
			if attrKey, _, err = fileKey(k); err != nil {
				continue
			}
		}
		name, err := attrName(attrKey, n.A)
		if err != nil {
			continue
		}
		byHandle[n.H] = &dec{n: n, key: k, name: fsutil.SanitizeFilename(name)}
	}
	var relPath func(h string, depth int) string
	relPath = func(h string, depth int) string {
		x, ok := byHandle[h]
		if !ok || depth > 64 {
			return ""
		}
		if parent := relPath(x.n.P, depth+1); parent != "" {
			return filepath.Join(parent, x.name)
		}
		return x.name
	}
	// inSelection reports whether handle h is the selected node or lives beneath it.
	inSelection := func(h string) bool {
		if l.selected == "" {
			return true
		}
		for depth := 0; h != "" && depth < 64; depth++ {
			if h == l.selected {
				return true
			}
			x, ok := byHandle[h]
			if !ok {
				return false
			}
			h = x.n.P
		}
		return false
	}
	var files []fileNode
	for h, x := range byHandle {
		if x.n.T != 0 || !inSelection(h) {
			continue
		}
		files = append(files, fileNode{handle: h, rel: relPath(h, 0), size: x.n.S, key: x.key, name: x.name})
	}
	sortNodes(files)
	if len(files) == 0 {
		return errors.New("MEGA folder contains no downloadable files")
	}
	failed := 0
	var last error
	for _, f := range files {
		var g gResp
		if err := d.api(ctx, l.handle, map[string]any{"a": "g", "g": 1, "n": f.handle}, &g); err != nil {
			failed++
			last = err
			slog.Error("mega file info failed", "file", f.rel, "err", err)
			continue
		}
		out, err := fsutil.SafeJoin(dest, f.rel)
		if err != nil {
			failed++
			last = err
			continue
		}
		if err := d.fetch(ctx, g.G, f.key, out, g.Size); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			last = err
			slog.Error("mega file download failed", "file", f.rel, "err", err)
		}
	}
	if failed == len(files) {
		return last
	}
	return nil
}

func sortNodes(f []fileNode) {
	paths := make([]string, len(f))
	idx := map[string]fileNode{}
	for i, n := range f {
		paths[i] = n.rel
		idx[n.rel] = n
	}
	fsutil.SortNatural(paths)
	for i, p := range paths {
		f[i] = idx[p]
	}
}

func (d *Downloader) fetch(ctx context.Context, dlURL string, key32 []byte, out string, size int64) error {
	aesKey, nonce, err := fileKey(key32)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MEGA download HTTP %d", resp.StatusCode)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return err
	}
	iv := make([]byte, 16)
	copy(iv, nonce)
	stream := cipher.NewCTR(block, iv)

	part := out + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	name := filepath.Base(out)
	thr := pacing.NewThrottler(pacing.ParseSpeedLimit(d.cfg.GlobalSpeedLimit))
	buf := make([]byte, 1<<20)
	fail := func(e error) error { _ = f.Close(); _ = os.Remove(part); return e }
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stream.XORKeyStream(buf[:n], buf[:n])
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			if terr := thr.Consume(ctx, n); terr != nil {
				return fail(terr)
			}
			d.total += int64(n)
			if d.Progress != nil {
				d.Progress(d.total, float64(d.total)/max(time.Since(d.start).Seconds(), 0.1), name)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, out)
}
