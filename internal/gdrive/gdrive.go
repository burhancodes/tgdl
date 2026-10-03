// Package gdrive downloads Google Drive files and folders.
package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/pacing"
)

const (
	folderMIME = "application/vnd.google-apps.folder"
	scope      = drive.DriveScope
)

var exportMap = map[string]struct{ MIME, Ext string }{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"application/vnd.google-apps.drawing":      {"image/png", ".png"},
}

// ProgressFunc reports cumulative bytes, speed (B/s) and the current file.
type ProgressFunc func(downloaded int64, speed float64, filename string)

var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{25,}$`)

// IsDriveURL reports whether a job URL targets Google Drive.
func IsDriveURL(u string) bool {
	return strings.HasPrefix(u, "gdrive:") || strings.HasPrefix(u, "gd2tg:") ||
		strings.Contains(u, "drive.google.com") || strings.Contains(u, "docs.google.com")
}

// IDFromURL extracts a Drive file/folder ID from a link or bare ID.
func IDFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if idRE.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err == nil && (strings.Contains(u.Host, "drive.google.com") || strings.Contains(u.Host, "docs.google.com")) {
		parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
		for _, marker := range []string{"d", "folders"} {
			for i, p := range parts {
				if p == marker && i+1 < len(parts) {
					return parts[i+1], nil
				}
			}
		}
		if id := u.Query().Get("id"); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not extract Google Drive ID from link: %q", raw)
}

var badName = regexp.MustCompile(`[/\\:*?"<>|]`)

func sanitize(name string) string {
	name = strings.TrimSpace(badName.ReplaceAllString(name, "_"))
	if name == "" || name == "." || name == ".." {
		return "unnamed_file"
	}
	return name
}

// ---- authentication ------------------------------------------------------

// Auth resolves Drive credentials for a user.
type Auth struct {
	cfg    *config.Config
	userID int64
}

func NewAuth(cfg *config.Config, userID int64) *Auth {
	if userID < 0 {
		userID = 0
	}
	return &Auth{cfg: cfg, userID: userID}
}

func (a *Auth) paths() (accounts, token string) {
	if a.userID > 0 {
		d := filepath.Join(a.cfg.AuthDir, strconv.FormatInt(a.userID, 10))
		return filepath.Join(d, "accounts"), filepath.Join(d, "token.json")
	}
	return a.cfg.GDriveAccountsDir, a.cfg.GDriveTokenPath
}

func serviceAccounts(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	rand.Shuffle(len(m), func(i, j int) { m[i], m[j] = m[j], m[i] })
	return m
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// HasCredentials reports whether any usable credential source exists.
func (a *Auth) HasCredentials() bool {
	acc, tok := a.paths()
	if a.cfg.UseServiceAccts && len(serviceAccounts(acc)) > 0 || exists(tok) {
		return true
	}
	if a.userID > 0 {
		return a.cfg.UseServiceAccts && len(serviceAccounts(a.cfg.GDriveAccountsDir)) > 0 || exists(a.cfg.GDriveTokenPath)
	}
	return false
}

type authorizedUser struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	TokenURI     string `json:"token_uri"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Expiry       string `json:"expiry,omitempty"`
}

// persistSource saves refreshed tokens back to disk.
type persistSource struct {
	src  oauth2.TokenSource
	path string
	au   authorizedUser
	last string
}

func (p *persistSource) Token() (*oauth2.Token, error) {
	t, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if t.AccessToken != p.last {
		p.last = t.AccessToken
		p.au.Token, p.au.Expiry = t.AccessToken, t.Expiry.UTC().Format(time.RFC3339)
		if t.RefreshToken != "" {
			p.au.RefreshToken = t.RefreshToken
		}
		if b, err := json.MarshalIndent(p.au, "", "  "); err == nil {
			_ = os.WriteFile(p.path, b, 0o600)
		}
	}
	return t, nil
}

// Service builds a Drive service using service accounts first, then OAuth.
func (a *Auth) Service(ctx context.Context) (*drive.Service, error) {
	acc, tok := a.paths()
	type pair struct{ acc, tok string }
	searches := []pair{{acc, tok}}
	if a.userID > 0 {
		searches = append(searches, pair{a.cfg.GDriveAccountsDir, a.cfg.GDriveTokenPath})
	}
	for _, s := range searches {
		if a.cfg.UseServiceAccts {
			for _, f := range serviceAccounts(s.acc) {
				b, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				creds, err := google.CredentialsFromJSON(ctx, b, scope)
				if err != nil {
					slog.Warn("service account rejected", "file", filepath.Base(f), "err", err)
					continue
				}
				slog.Info("gdrive: using service account", "file", filepath.Base(f))
				return drive.NewService(ctx, option.WithCredentials(creds))
			}
		}
		if b, err := os.ReadFile(s.tok); err == nil {
			var au authorizedUser
			if err := json.Unmarshal(b, &au); err != nil {
				slog.Error("gdrive: bad token file", "path", s.tok, "err", err)
				continue
			}
			uri := au.TokenURI
			if uri == "" {
				uri = google.Endpoint.TokenURL
			}
			conf := &oauth2.Config{ClientID: au.ClientID, ClientSecret: au.ClientSecret, Scopes: []string{scope},
				Endpoint: oauth2.Endpoint{TokenURL: uri, AuthURL: google.Endpoint.AuthURL}}
			t := &oauth2.Token{AccessToken: au.Token, RefreshToken: au.RefreshToken, TokenType: "Bearer"}
			if e, err := time.Parse(time.RFC3339, au.Expiry); err == nil {
				t.Expiry = e
			}
			src := &persistSource{src: conf.TokenSource(ctx, t), path: s.tok, au: au, last: au.Token}
			slog.Info("gdrive: using OAuth token", "path", s.tok)
			return drive.NewService(ctx, option.WithTokenSource(oauth2.ReuseTokenSource(t, src)))
		}
	}
	who := "default"
	if a.userID > 0 {
		who = strconv.FormatInt(a.userID, 10)
	}
	return nil, fmt.Errorf("no valid GDrive credentials found for user %q: provide credentials.json or service account JSON files", who)
}

// OAuthFlow begins an installed-app OAuth flow from a client-secret file.
func OAuthFlow(credsJSON []byte, redirect string) (*oauth2.Config, string, error) {
	if redirect == "" {
		redirect = "http://127.0.0.1:8080/"
	}
	conf, err := google.ConfigFromJSON(credsJSON, scope)
	if err != nil {
		return nil, "", err
	}
	conf.RedirectURL = redirect
	return conf, conf.AuthCodeURL("tgdl", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("include_granted_scopes", "true")), nil
}

// FinishOAuth exchanges the code and saves the token JSON (mode 0600).
func (a *Auth) FinishOAuth(ctx context.Context, conf *oauth2.Config, code string) error {
	t, err := conf.Exchange(ctx, strings.TrimSpace(code))
	if err != nil {
		return err
	}
	_, path := a.paths()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	au := authorizedUser{Token: t.AccessToken, RefreshToken: t.RefreshToken, TokenURI: conf.Endpoint.TokenURL,
		ClientID: conf.ClientID, ClientSecret: conf.ClientSecret, Expiry: t.Expiry.UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(au, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// ---- downloader ----------------------------------------------------------

// Downloader fetches Drive links.
type Downloader struct {
	cfg      *config.Config
	svc      *drive.Service
	auth     *Auth
	Progress ProgressFunc

	downloaded int64
	start      time.Time
}

func NewDownloader(cfg *config.Config, userID int64, p ProgressFunc) *Downloader {
	return &Downloader{cfg: cfg, auth: NewAuth(cfg, userID), Progress: p}
}

func (d *Downloader) service(ctx context.Context) (*drive.Service, error) {
	if d.svc == nil {
		s, err := d.auth.Service(ctx)
		if err != nil {
			return nil, err
		}
		d.svc = s
	}
	return d.svc, nil
}

func retry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var v T
		if v, err = fn(); err == nil {
			return v, nil
		}
		var ge *googleapi.Error
		if errors.As(err, &ge) && (ge.Code == 404 || ge.Code == 403 && !strings.Contains(ge.Message, "rate")) {
			return zero, err
		}
		if e := pacing.Sleep(ctx, time.Duration(min(2<<attempt, 10))*time.Second); e != nil {
			return zero, e
		}
	}
	return zero, err
}

func (d *Downloader) meta(ctx context.Context, id string) (*drive.File, error) {
	svc, err := d.service(ctx)
	if err != nil {
		return nil, err
	}
	f, err := retry(ctx, func() (*drive.File, error) {
		return svc.Files.Get(id).Fields("id, name, mimeType, size, parents").SupportsAllDrives(true).Context(ctx).Do()
	})
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		switch ge.Code {
		case 404:
			return nil, fmt.Errorf("google drive item %q not found or inaccessible", id)
		case 403:
			return nil, fmt.Errorf("permission denied accessing google drive item %q", id)
		}
	}
	return f, err
}

func (d *Downloader) list(ctx context.Context, folderID string) ([]*drive.File, error) {
	svc, err := d.service(ctx)
	if err != nil {
		return nil, err
	}
	var out []*drive.File
	token := ""
	for {
		resp, err := retry(ctx, func() (*drive.FileList, error) {
			c := svc.Files.List().Q(fmt.Sprintf("'%s' in parents and trashed = false", folderID)).Spaces("drive").
				Fields("nextPageToken, files(id, name, mimeType, size)").SupportsAllDrives(true).IncludeItemsFromAllDrives(true).Context(ctx)
			if token != "" {
				c = c.PageToken(token)
			}
			return c.Do()
		})
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Files...)
		if token = resp.NextPageToken; token == "" {
			return out, nil
		}
	}
}

// DownloadLink downloads one or more links (JSON array, whitespace list, or a
// single link) into dest and returns all downloaded files.
func (d *Downloader) DownloadLink(ctx context.Context, link, dest string) ([]string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	link = strings.TrimSpace(link)
	var urls []string
	if strings.HasPrefix(link, "[") {
		_ = json.Unmarshal([]byte(link), &urls)
	}
	if len(urls) == 0 {
		urls = strings.Fields(link)
	}
	if len(urls) == 0 && link != "" {
		urls = []string{link}
	}
	fsutil.SortNatural(urls)
	failed, total := 0, len(urls)
	var lastErr error
	for _, raw := range urls {
		clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "gdrive:"), "gd2tg:")
		if err := d.downloadOne(ctx, clean, dest); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed++
			lastErr = err
			slog.Error("gdrive download failed", "link", clean, "err", err)
		}
	}
	if total > 0 && failed == total {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("all %d Google Drive downloads failed", total)
	}
	files, _ := fsutil.ListFiles(dest)
	fsutil.SortNatural(files)
	return files, nil
}

func (d *Downloader) downloadOne(ctx context.Context, link, dest string) error {
	id, err := IDFromURL(link)
	if err != nil {
		return err
	}
	m, err := d.meta(ctx, id)
	if err != nil {
		return err
	}
	d.start = time.Now()
	if m.MimeType == folderMIME {
		return d.downloadFolder(ctx, id, filepath.Join(dest, sanitize(m.Name)))
	}
	_, err = d.downloadFile(ctx, m, dest)
	return err
}

type entry struct {
	f   *drive.File
	rel string
}

func (d *Downloader) collect(ctx context.Context, folderID, rel string) ([]entry, error) {
	items, err := d.list(ctx, folderID)
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, it := range items {
		r := filepath.Join(rel, sanitize(it.Name))
		if it.MimeType == folderMIME {
			sub, err := d.collect(ctx, it.Id, r)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		cp := *it
		cp.Name = sanitize(it.Name)
		out = append(out, entry{&cp, r})
	}
	return out, nil
}

func (d *Downloader) downloadFolder(ctx context.Context, id, path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	entries, err := d.collect(ctx, id, "")
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool { return fsutil.NaturalPathLess(entries[i].rel, entries[j].rel) })
	for _, e := range entries {
		parent := filepath.Join(path, filepath.Dir(e.rel))
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
		if _, err := d.downloadFile(ctx, e.f, parent); err != nil {
			return err
		}
	}
	return nil
}

func (d *Downloader) downloadFile(ctx context.Context, m *drive.File, parent string) (string, error) {
	svc, err := d.service(ctx)
	if err != nil {
		return "", err
	}
	name := sanitize(m.Name)
	var resp *http.Response
	if exp, ok := exportMap[m.MimeType]; ok {
		if !strings.HasSuffix(strings.ToLower(name), exp.Ext) {
			name += exp.Ext
		}
		resp, err = retry(ctx, func() (*http.Response, error) {
			return svc.Files.Export(m.Id, exp.MIME).Context(ctx).Download()
		})
	} else {
		resp, err = retry(ctx, func() (*http.Response, error) {
			return svc.Files.Get(m.Id).SupportsAllDrives(true).Context(ctx).Download()
		})
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	out := filepath.Join(parent, name)
	part := out + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	thr := pacing.NewThrottler(pacing.ParseSpeedLimit(d.cfg.GlobalSpeedLimit))
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				_ = os.Remove(part)
				return "", werr
			}
			if terr := thr.Consume(ctx, n); terr != nil {
				_ = f.Close()
				_ = os.Remove(part)
				return "", terr
			}
			d.downloaded += int64(n)
			if d.Progress != nil {
				d.Progress(d.downloaded, float64(d.downloaded)/max(time.Since(d.start).Seconds(), 0.1), name)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			_ = os.Remove(part)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return out, os.Rename(part, out)
}
