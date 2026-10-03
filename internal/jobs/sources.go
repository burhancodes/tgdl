package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/gdrive"
	"github.com/burhanverse/tgdl/internal/mega"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/torrent"
	"github.com/burhanverse/tgdl/internal/upload"
)

// Patcher signs/patches an APK (implemented in internal/apk).
type Patcher interface {
	Patch(ctx context.Context, input, outDir, originalName string, ks *config.KeystoreInfo, stage func(string)) (string, error)
}

// Deps are the collaborators shared by all sources and the pipeline.
type Deps struct {
	Cfg     *config.Config
	Store   *store.Store
	TG      tg.Client
	Keys    *upload.Keys
	Hosts   *upload.Hosts
	Runner  *dl.Runner
	Aria    *torrent.Aria2
	Xenforo *dl.Xenforo
	Patcher Patcher
}

// Request is what a Source sees.
type Request struct {
	Job  *store.Job
	Args dl.Args
	URL  string // first URL of the job, prefixes intact
	Dest string
	St   *State
}

// Source downloads one family of URLs. The manager tries sources in
// registration order; the first whose Match returns true fetches the job.
// Adding a new provider means implementing this interface and registering it —
// there is no central if/elif chain to edit.
type Source interface {
	Name() string
	Match(r *Request) bool
	Fetch(ctx context.Context, r *Request) dl.Result
}

// DiskMonitored is implemented by sources that do not report bytes themselves;
// the manager then measures progress by scanning the destination directory.
type DiskMonitored interface{ DiskMonitored() bool }

func isTorrentURL(u string) bool {
	return strings.HasPrefix(u, "magnet:") || strings.HasPrefix(u, "torrent:") ||
		strings.HasSuffix(u, ".torrent") || strings.Contains(u, "magnet:?xt=")
}

func fail(format string, a ...any) dl.Result { return dl.Result{ErrorTail: fmt.Sprintf(format, a...)} }

func resultFromFiles(files []string, err error) dl.Result {
	if err != nil {
		return dl.Result{ErrorTail: err.Error()}
	}
	return dl.Result{OK: true, Files: files}
}

func listAll(dir string) []string {
	f, _ := fsutil.ListFiles(dir)
	fsutil.SortNatural(f)
	return f
}

// DefaultSources builds the ordered source chain.
func DefaultSources(d *Deps) []Source {
	return []Source{
		&unzipSource{d}, &patchSource{d}, &driveSource{d}, &megaSource{d}, &gofileSource{d},
		&ariaSource{d}, &mirrorTGSource{d}, &mirrorSource{d}, &xenforoSource{d},
		&cyberdropSource{d}, &directSource{d}, &galleryDLSource{d},
	}
}

// ---- Telegram-file based sources -------------------------------------------

func downloadTG(ctx context.Context, d *Deps, st *State, f *dl.TGFile, dest string) (string, error) {
	if f == nil || f.FileID == "" {
		return "", fmt.Errorf("no Telegram file attached to the job")
	}
	st.SetEngine("Telegram")
	return d.TG.Download(ctx, tg.FileRef{FileID: f.FileID, Name: f.Name, Size: f.Size}, dest, func(cur, total int64) {
		st.Bytes(dl.Progress{Current: cur, Total: total, File: f.Name})
	})
}

type unzipSource struct{ d *Deps }

func (unzipSource) Name() string { return "unzip" }
func (unzipSource) Match(r *Request) bool {
	return strings.HasPrefix(r.URL, "unzip:") || r.Args.Unzip && r.Args.TGFile != nil
}
func (s unzipSource) Fetch(ctx context.Context, r *Request) dl.Result {
	if existing := listAll(r.Dest); len(existing) == 0 && r.Args.TGFile != nil {
		if _, err := downloadTG(ctx, s.d, r.St, r.Args.TGFile, r.Dest); err != nil {
			slog.Error("unzip: telegram download failed", "job", r.Job.ID, "err", err)
			return fail("download replied Telegram media: %v", err)
		}
	}
	return dl.Result{OK: true, Files: listAll(r.Dest)}
}

type mirrorTGSource struct{ d *Deps }

func (mirrorTGSource) Name() string { return "mirror-telegram" }
func (mirrorTGSource) Match(r *Request) bool {
	return strings.HasPrefix(r.URL, "mirror_tg:") && r.Args.TGFile != nil
}
func (s mirrorTGSource) Fetch(ctx context.Context, r *Request) dl.Result {
	p, err := downloadTG(ctx, s.d, r.St, r.Args.TGFile, r.Dest)
	if err != nil {
		return fail("%v", err)
	}
	return dl.Result{OK: true, Files: []string{p}}
}

type patchSource struct{ d *Deps }

func (patchSource) Name() string { return "apk-patch" }
func (patchSource) Match(r *Request) bool {
	return strings.HasPrefix(r.URL, "patch:")
}
func (s patchSource) Fetch(ctx context.Context, r *Request) dl.Result {
	st := r.St
	st.SetPatch(true)
	st.SetEngine("APKEditor + tgpatcher")
	if s.d.Patcher == nil {
		return fail("APK patcher is not configured")
	}
	work := r.Dest + "_patch_work"
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fail("%v", err)
	}
	defer os.RemoveAll(work)

	orig := r.Args.OriginalFilename
	if orig == "" {
		orig = "app.apk"
	}
	var input string
	switch {
	case r.Args.TGFile != nil:
		p, err := downloadTG(ctx, s.d, st, r.Args.TGFile, work)
		if err != nil {
			return fail("download Telegram APK: %v", err)
		}
		input = p
	case r.Args.TargetURL != "":
		d := dl.NewDirect(s.d.Cfg, st.Bytes)
		files, err := d.Download(ctx, work, r.Args.TargetURL)
		if err != nil || len(files) == 0 {
			return fail("download APK from URL: %v", err)
		}
		input = files[0]
	default:
		return fail("no APK source provided")
	}
	out, err := s.d.Patcher.Patch(ctx, input, work, orig, s.d.Cfg.UserKeystore(r.Args.UserIDInt()), st.SetPatchStage)
	if err != nil {
		return fail("%v", err)
	}
	if err := os.MkdirAll(r.Dest, 0o755); err != nil {
		return fail("%v", err)
	}
	final := filepath.Join(r.Dest, filepath.Base(out))
	if err := os.Rename(out, final); err != nil {
		return fail("move patched APK: %v", err)
	}
	return dl.Result{OK: true, Files: []string{final}}
}

// ---- cloud/file-host sources ---------------------------------------------

type driveSource struct{ d *Deps }

func (driveSource) Name() string { return "google-drive" }
func (driveSource) Match(r *Request) bool {
	return gdrive.IsDriveURL(r.URL)
}
func (s driveSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("Google Drive API")
	link := strings.TrimPrefix(strings.TrimPrefix(r.Job.URL, "gdrive:"), "gd2tg:")
	dr := gdrive.NewDownloader(s.d.Cfg, r.Args.UserIDInt(), r.St.SpeedBytes)
	if _, err := dr.DownloadLink(ctx, link, r.Dest); err != nil {
		return fail("%v", err)
	}
	return dl.Result{OK: true, Files: listAll(r.Dest)}
}

type megaSource struct{ d *Deps }

func (megaSource) Name() string           { return "mega" }
func (megaSource) Match(r *Request) bool  { return mega.IsMegaURL(r.URL) }
func (s megaSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("Mega API")
	dr := mega.NewDownloader(s.d.Cfg, r.St.SpeedBytes)
	files, err := dr.DownloadLinks(ctx, r.Job.URL, r.Dest)
	return resultFromFiles(files, err)
}

type gofileSource struct{ d *Deps }

func (gofileSource) Name() string { return "gofile" }
func (s gofileSource) Match(r *Request) bool {
	return dl.IsGofileURL(s.d.Cfg, r.URL)
}
func (s gofileSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("GoFile Bypass")
	files, err := dl.DownloadGofile(ctx, s.d.Cfg, r.Dest, r.Job.URL, r.St.Bytes)
	if err == nil {
		return dl.Result{OK: true, Files: files}
	}
	slog.Warn("gofile bypass failed; falling back to engine chain", "err", err)
	raw := stripGofile(r.Job.URL)
	res, cerr := s.d.Runner.RunWithBackoff(ctx, raw, r.Dest, dl.RunOpts{
		UserID: r.Args.UserIDInt(), ExtraArgs: r.Args.CLIExtra(), OnProgress: r.St.Count, Procs: r.St,
		Chain: []dl.Engine{dl.EngineGalleryDL, dl.EngineCyberdrop},
	})
	if cerr != nil {
		return fail("%v", cerr)
	}
	return res
}

func stripGofile(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil {
			for i, s := range list {
				list[i] = dl.StripGofilePrefix(s)
			}
			if len(list) == 1 {
				return list[0]
			}
			b, _ := json.Marshal(list)
			return string(b)
		}
	}
	return dl.StripGofilePrefix(raw)
}

// ---- aria2 (torrents, magnets, explicit engine) ---------------------------

type ariaSource struct{ d *Deps }

func (ariaSource) Name() string { return "aria2" }
func (ariaSource) Match(r *Request) bool {
	return isTorrentURL(r.URL) || r.Args.Engine == "aria2"
}
func (s ariaSource) Fetch(ctx context.Context, r *Request) dl.Result {
	st := r.St
	target := strings.TrimPrefix(dl.FirstURL(r.Job.URL), "mirror:")
	target = strings.TrimPrefix(target, "direct:")
	torrentJob := isTorrentURL(target)
	st.SetEngine("aria2c")
	st.SetTorrent(torrentJob)
	h := func(p torrent.Progress) { st.Aria(p.Pct, p.Completed, p.Speed, p.Seeders, p.Connection, p.Name) }
	return s.d.Aria.Download(ctx, target, r.Dest, r.Args.AriaOptions, h)
}

// ---- generic HTTP mirror ---------------------------------------------------

type mirrorSource struct{ d *Deps }

func (mirrorSource) Name() string          { return "mirror" }
func (mirrorSource) Match(r *Request) bool { return strings.HasPrefix(r.URL, "mirror:") }
func (mirrorSource) DiskMonitored() bool   { return false }
func (s mirrorSource) Fetch(ctx context.Context, r *Request) dl.Result {
	target := strings.TrimPrefix(r.URL, "mirror:")
	r.St.SetEngine("Direct HTTP")
	d := dl.NewDirect(s.d.Cfg, r.St.Bytes)
	if files, err := d.Download(ctx, r.Dest, target); err == nil {
		return dl.Result{OK: true, Files: files}
	} else if ctx.Err() != nil {
		return fail("cancelled")
	} else {
		slog.Warn("direct download failed for mirror link; trying engine chain", "url", target, "err", err)
	}
	res, err := s.d.Runner.Run(ctx, target, r.Dest, dl.RunOpts{
		UserID: r.Args.UserIDInt(), OnProgress: r.St.Count, Procs: r.St,
		Chain: []dl.Engine{dl.EngineGalleryDL, dl.EngineCyberdrop},
	})
	if err != nil {
		return fail("%v", err)
	}
	return res
}

// ---- XenForo forums --------------------------------------------------------

type xenforoSource struct{ d *Deps }

func (xenforoSource) Name() string { return "xenforo" }
func (xenforoSource) Match(r *Request) bool {
	return dl.IsXenforoURL(r.URL) || r.Args.Engine == "xenforo"
}
func (s xenforoSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("XenForo Scraper")
	target := dl.StripXenforoPrefix(dl.FirstURL(r.Job.URL))
	opts := dl.ParseXenforoArgs(r.Args.CLIExtra())
	return s.d.Xenforo.Download(ctx, target, r.Dest, r.Args.UserIDInt(), opts, r.St.Count)
}

// ---- cyberdrop-dl ----------------------------------------------------------

type cyberdropSource struct{ d *Deps }

func (cyberdropSource) Name() string { return "cyberdrop-dl" }
func (cyberdropSource) Match(r *Request) bool {
	return strings.HasPrefix(r.URL, "cdl:") || strings.HasPrefix(r.URL, "cyberdrop-dl:") || r.Args.Engine == "cyberdrop-dl"
}
func (s cyberdropSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("cyberdrop-dl")
	target := strings.TrimPrefix(strings.TrimPrefix(dl.FirstURL(r.Job.URL), "cdl:"), "cyberdrop-dl:")
	res, err := s.d.Runner.Run(ctx, target, r.Dest, dl.RunOpts{
		UserID: r.Args.UserIDInt(), ExtraArgs: r.Args.CLIExtra(), OnProgress: r.St.Count, Procs: r.St,
		Chain: []dl.Engine{dl.EngineCyberdrop, dl.EngineGalleryDL},
	})
	if err != nil {
		return fail("%v", err)
	}
	return res
}

// ---- direct links / HLS ----------------------------------------------------

type directSource struct{ d *Deps }

func (directSource) Name() string { return "direct" }
func (s directSource) Match(r *Request) bool {
	if strings.HasPrefix(r.URL, "direct:") || dl.IsDirectURL(r.URL) {
		return true
	}
	if !strings.HasPrefix(r.URL, "http") {
		return false
	}
	return dl.NewDirect(s.d.Cfg, nil).IsM3U8URL(context.Background(), r.URL)
}
func (s directSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("Direct HTTP")
	raw := r.Job.URL
	if !strings.HasPrefix(raw, "[") {
		raw = strings.TrimPrefix(raw, "direct:")
	}
	d := dl.NewDirect(s.d.Cfg, r.St.Bytes)
	files, err := d.Download(ctx, r.Dest, raw)
	return resultFromFiles(files, err)
}

// ---- default: gallery-dl → cyberdrop-dl → direct ---------------------------

type galleryDLSource struct{ d *Deps }

func (galleryDLSource) Name() string          { return "gallery-dl" }
func (galleryDLSource) Match(*Request) bool   { return true }
func (s galleryDLSource) Fetch(ctx context.Context, r *Request) dl.Result {
	r.St.SetEngine("gallery-dl")
	res, err := s.d.Runner.RunWithBackoff(ctx, r.Job.URL, r.Dest, dl.RunOpts{
		UserID: r.Args.UserIDInt(), ExtraArgs: r.Args.CLIExtra(), OnProgress: r.St.Count, Procs: r.St,
		Chain: []dl.Engine{dl.EngineGalleryDL, dl.EngineCyberdrop},
	})
	if err != nil {
		return fail("%v", err)
	}
	return res
}

// diskMonitored reports whether the manager should scan the disk for progress.
func diskMonitored(s Source) bool {
	switch s.(type) {
	case *galleryDLSource, *cyberdropSource, *xenforoSource:
		return true
	}
	if m, ok := s.(DiskMonitored); ok {
		return m.DiskMonitored()
	}
	return false
}
