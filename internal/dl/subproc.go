package dl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/upload"
)

// ErrToolMissing indicates a required external binary is not installed.
var ErrToolMissing = errors.New("required tool not found")

// Subprocess engines (gallery-dl, cyberdrop-dl) share URL parsing, per-URL
// execution and fallback logic. The mutual gdl<->cdl fallback of the original
// is expressed here as an ordered chain evaluated per URL, so no engine can
// recurse into another.

// Engine identifies an external downloader.
type Engine string

const (
	EngineGalleryDL Engine = "gallery-dl"
	EngineCyberdrop Engine = "cyberdrop-dl"
)

// Runner executes external download tools and the direct fallback.
type Runner struct {
	cfg  *config.Config
	keys *upload.Keys
}

func NewRunner(cfg *config.Config, keys *upload.Keys) *Runner { return &Runner{cfg: cfg, keys: keys} }

// RunOpts controls a chain run.
type RunOpts struct {
	UserID     int64
	ExtraArgs  []string
	OnProgress CountFunc
	Procs      ProcRegistry
	// Chain is the ordered list of engines to try for each URL before the
	// direct-download fallback.
	Chain []Engine
	// ConfigPath overrides config discovery for the first engine.
	ConfigPath string
}

// ---- path discovery ------------------------------------------------------

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }

func (r *Runner) userDir(uid int64) string { return filepath.Join(r.cfg.AuthDir, strconv.FormatInt(uid, 10)) }

// GDLConfigPath resolves the gallery-dl config: user -> configs/ -> global.
func (r *Runner) GDLConfigPath(uid int64) string {
	cands := []string{}
	if uid > 0 {
		cands = append(cands, filepath.Join(r.userDir(uid), "gallery-dl.conf"))
	}
	cands = append(cands, r.cfg.GDLConfigPath, filepath.Join(r.cfg.AuthDir, "gallery-dl.conf"))
	for _, c := range cands {
		if isFile(c) {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

// CDLConfigPath resolves the cyberdrop-dl config.
func (r *Runner) CDLConfigPath(uid int64) string {
	cands := []string{}
	if uid > 0 {
		cands = append(cands, filepath.Join(r.userDir(uid), "config.yaml"), filepath.Join(r.userDir(uid), "cyberdrop-dl.yaml"))
	}
	cands = append(cands, r.cfg.CDLConfigPath,
		filepath.Join(r.cfg.AuthDir, "cyberdrop-dl.yaml"), filepath.Join(r.cfg.AuthDir, "config.yaml"))
	for _, c := range cands {
		if isFile(c) {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

// CookiesPath resolves cookies.txt: user -> auth/ -> cwd.
func (r *Runner) CookiesPath(uid int64) string {
	cands := []string{}
	if uid > 0 {
		cands = append(cands, filepath.Join(r.userDir(uid), "cookies.txt"))
	}
	cands = append(cands, filepath.Join(r.cfg.AuthDir, "cookies.txt"), "cookies.txt")
	for _, c := range cands {
		if isFile(c) {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

// ---- URL parsing ---------------------------------------------------------

var cdlPrefixes = []string{"cdl:", "cyberdrop-dl:"}

func trimCDL(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range cdlPrefixes {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(strings.TrimPrefix(s, p))
		}
	}
	return s
}

// ParseURLs splits a job URL (JSON array, whitespace list, or single value)
// into naturally sorted URLs.
func ParseURLs(raw string) []string {
	raw = trimCDL(raw)
	var urls []string
	if strings.HasPrefix(raw, "[") {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, u := range list {
				if u = trimCDL(u); u != "" {
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
	if len(urls) == 0 && raw != "" {
		urls = []string{raw}
	}
	fsutil.SortNatural(urls)
	return urls
}

// ---- command builders ----------------------------------------------------

func (r *Runner) gdlCmd(url, dest string, o RunOpts) *exec.Cmd {
	args := []string{}
	conf := o.ConfigPath
	if conf == "" {
		conf = r.GDLConfigPath(o.UserID)
	}
	if conf != "" {
		args = append(args, "--config", conf)
	}
	if ck := r.CookiesPath(o.UserID); ck != "" {
		args = append(args, "--cookies", ck)
	}
	args = append(args,
		"--no-mtime", "-D", dest,
		"--sleep", fmt.Sprintf("%g-%g", r.cfg.GDLSleepMin, r.cfg.GDLSleepMax),
		"--sleep-request", r.cfg.GDLSleepRequest,
		"--retries", strconv.Itoa(r.cfg.GDLRetries),
		"-v")
	if pacing.ParseSpeedLimit(r.cfg.GlobalSpeedLimit) > 0 {
		args = append(args, "--limit-rate", r.cfg.GlobalSpeedLimit)
	}
	src := r.cfg.SourceAddress
	if src == "" && r.cfg.ForceIPv6 {
		src = "::"
	}
	if src != "" {
		args = append(args, "--source-address", src)
	}
	if tok := strings.TrimSpace(r.keys.Resolve(o.UserID, "gofile")); tok != "" {
		args = append(args, "-o", "extractor.gofile.api-token="+tok)
	} else if tok := strings.TrimSpace(r.cfg.GofileAPIKey); tok != "" && r.cfg.AllowSharedKeys {
		args = append(args, "-o", "extractor.gofile.api-token="+tok)
	}
	args = append(args, o.ExtraArgs...)
	args = append(args, url)
	return exec.Command("gallery-dl", args...)
}

func findCDL() (string, bool) {
	p, err := exec.LookPath("cyberdrop-dl")
	return p, err == nil
}

func (r *Runner) cdlCmd(url, dest string, o RunOpts) (*exec.Cmd, error) {
	bin, ok := findCDL()
	if !ok {
		return nil, fmt.Errorf("%w: cyberdrop-dl", ErrToolMissing)
	}
	_ = os.MkdirAll(filepath.Dir(r.cfg.CDLArchivePath()), 0o755)
	if f, err := os.OpenFile(r.cfg.CDLArchivePath(), os.O_CREATE, 0o644); err == nil {
		_ = f.Close()
	}
	absDest, _ := filepath.Abs(dest)
	absDB, _ := filepath.Abs(r.cfg.CDLArchivePath())
	args := []string{"download", "--ui", "disabled", "--no-stats", "--min-free-space", "0", "--ignore-history",
		"--no-mtime", "--download-folder", absDest, "--attempts", strconv.Itoa(r.cfg.CDLRetries), "--database-file", absDB}
	conf := o.ConfigPath
	if conf == "" {
		conf = r.CDLConfigPath(o.UserID)
	}
	if conf != "" {
		args = append(args, "--config-file", conf)
	}
	if ck := r.CookiesPath(o.UserID); ck != "" {
		args = append(args, "--cookies", ck)
	}
	if n := pacing.ParseSpeedLimit(r.cfg.GlobalSpeedLimit); n > 0 {
		args = append(args, "--speed-limit", strconv.FormatInt(n, 10))
	}
	args = append(args, o.ExtraArgs...)
	args = append(args, url)
	return exec.Command(bin, args...), nil
}

// ---- process execution ---------------------------------------------------

type procOut struct {
	code   int
	stdout []string // last 300 lines
	stderr []string // last 200 lines
	count  int
}

var (
	cdlLockRE      = regexp.MustCompile(`(?i)Lock for '([^']+)' acquired`)
	cdlDoneRE      = regexp.MustCompile(`(?i)(?:Download Complete|Completed):\s*([^\n\r]+)`)
	cdlFailedRE    = regexp.MustCompile(`(?i)Failed:\s*(\d+)\s*files`)
	cdlDownloadRE  = regexp.MustCompile(`(?i)Downloaded:\s*(\d+)\s*files`)
)

func tail(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// runProc starts cmd, streams stdout through onLine, collects stderr, and kills
// the process when ctx is cancelled.
func runProc(ctx context.Context, cmd *exec.Cmd, procs ProcRegistry, onLine func(string) bool) (*procOut, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if procs != nil {
		procs.Register(cmd)
		defer procs.Unregister()
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-stop:
		}
	}()

	out := &procOut{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			mu.Lock()
			out.stdout = append(tail(out.stdout, 299), line)
			if onLine != nil && onLine(line) {
				out.count++
			}
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			mu.Lock()
			out.stderr = append(tail(out.stderr, 199), sc.Text())
			mu.Unlock()
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	wg.Wait()
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if ee := (*exec.ExitError)(nil); errors.As(werr, &ee) {
		out.code = ee.ExitCode()
	} else if werr != nil {
		return out, werr
	}
	return out, nil
}

func fileSet(dir string) map[string]struct{} {
	files, _ := fsutil.ListFiles(dir)
	m := make(map[string]struct{}, len(files))
	for _, f := range files {
		m[f] = struct{}{}
	}
	return m
}

func countNew(before, after map[string]struct{}) int {
	n := 0
	for f := range after {
		if _, ok := before[f]; !ok {
			n++
		}
	}
	return n
}

// runGDL runs gallery-dl once for a URL, returning success and stderr tail.
func (r *Runner) runGDL(ctx context.Context, url, dest string, o RunOpts, base int) (bool, string, error) {
	if _, err := exec.LookPath("gallery-dl"); err != nil {
		return false, "", fmt.Errorf("%w: gallery-dl (install with: pip install gallery-dl)", ErrToolMissing)
	}
	before := fileSet(dest)
	cmd := r.gdlCmd(url, dest, o)
	slog.Info("gallery-dl run", "url", url, "user_id", o.UserID)
	out, err := runProc(ctx, cmd, o.Procs, func(line string) bool {
		name := ""
		if parts := strings.Fields(line); len(parts) > 0 {
			last := strings.Trim(parts[len(parts)-1], `'"`)
			if strings.ContainsAny(last, `/\.`) {
				name = filepath.Base(last)
			}
		}
		if o.OnProgress != nil {
			o.OnProgress(base+1, name, url) // approximate; refined after run
		}
		return true
	})
	if err != nil {
		return false, "", err
	}
	after := fileSet(dest)
	stderr := lastChars(strings.Join(out.stderr, "\n"), 3000)
	ok := out.code == 0 && (countNew(before, after) > 0 || len(after) > 0)
	if !ok {
		slog.Info("gallery-dl failed or produced no files", "url", url, "code", out.code)
	}
	return ok, stderr, nil
}

func (r *Runner) runCDL(ctx context.Context, url, dest string, o RunOpts, base int) (bool, string, error) {
	cmd, err := r.cdlCmd(url, dest, o)
	if err != nil {
		return false, "", err
	}
	before := fileSet(dest)
	slog.Info("cyberdrop-dl run", "url", url, "user_id", o.UserID)
	count := 0
	out, err := runProc(ctx, cmd, o.Procs, func(line string) bool {
		name := ""
		if m := cdlLockRE.FindStringSubmatch(line); m != nil {
			name = strings.TrimSpace(m[1])
		}
		if m := cdlDoneRE.FindStringSubmatch(line); m != nil {
			name = strings.TrimSpace(m[1])
			count++
		}
		if name != "" || strings.Contains(line, "Download attempt") || strings.Contains(line, "Downloading") {
			if o.OnProgress != nil {
				o.OnProgress(base+count, name, url)
			}
		}
		return false
	})
	if err != nil {
		return false, "", err
	}
	after := fileSet(dest)
	combined := strings.Join(out.stdout, "\n") + "\n" + strings.Join(out.stderr, "\n")
	stderr := lastChars(strings.Join(out.stderr, "\n"), 3000)
	if stderr == "" {
		stderr = lastChars(combined, 3000)
	}
	downloaded, failed := countNew(before, after), 0
	if m := cdlDownloadRE.FindStringSubmatch(combined); m != nil {
		downloaded, _ = strconv.Atoi(m[1])
	}
	if m := cdlFailedRE.FindStringSubmatch(combined); m != nil {
		failed, _ = strconv.Atoi(m[1])
	}
	ok := out.code == 0 && (downloaded > 0 || len(after) > 0) && failed == 0
	if !ok {
		slog.Info("cyberdrop-dl failed or produced no files", "url", url, "code", out.code)
	}
	return ok, stderr, nil
}

func lastChars(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Run executes the engine chain per URL, then the direct fallback, and reports
// the union of files present in dest. Like the original, the run is "ok" when
// any file exists at the end.
func (r *Runner) Run(ctx context.Context, rawURL, dest string, o RunOpts) (Result, error) {
	if len(o.Chain) == 0 {
		o.Chain = []Engine{EngineGalleryDL, EngineCyberdrop}
	}
	urls := ParseURLs(rawURL)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Result{}, err
	}
	var lastStderr string
	total := 0
	var toolErr error
	for _, u := range urls {
		if o.OnProgress != nil {
			o.OnProgress(total, "", u)
		}
		handled := false
		for i, eng := range o.Chain {
			var ok bool
			var se string
			var err error
			opts := o
			if i > 0 {
				opts.ConfigPath = "" // config override only applies to the primary engine
			}
			before := len(fileSet(dest))
			switch eng {
			case EngineGalleryDL:
				ok, se, err = r.runGDL(ctx, u, dest, opts, total)
			case EngineCyberdrop:
				ok, se, err = r.runCDL(ctx, u, dest, opts, total)
			}
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if err != nil {
				if errors.Is(err, ErrToolMissing) {
					toolErr = err
				}
				slog.Warn("engine error", "engine", eng, "url", u, "err", err)
				continue
			}
			if se != "" {
				lastStderr = se
			}
			if ok {
				total += max(len(fileSet(dest))-before, 1)
				handled = true
				break
			}
			if i < len(o.Chain)-1 {
				slog.Info("passing failed URL to next engine", "url", u, "next", o.Chain[i+1])
			}
		}
		if !handled {
			slog.Info("attempting direct download fallback", "url", u)
			d := NewDirect(r.cfg, func(p Progress) {
				if o.OnProgress != nil {
					o.OnProgress(total+1, p.File, u)
				}
			})
			if paths, err := d.Download(ctx, dest, u); err == nil && len(paths) > 0 {
				total += len(paths)
			} else if err != nil && ctx.Err() != nil {
				return Result{}, ctx.Err()
			} else if err != nil {
				slog.Warn("direct fallback failed", "url", u, "err", err)
			}
		}
		if o.OnProgress != nil {
			o.OnProgress(total, "", u)
		}
	}
	files, _ := fsutil.ListFiles(dest)
	fsutil.SortNatural(files)
	res := Result{OK: len(files) > 0, Files: files, ErrorTail: lastStderr}
	if !res.OK && toolErr != nil && res.ErrorTail == "" {
		res.ErrorTail = toolErr.Error()
	}
	return res, nil
}

// RunWithBackoff wraps Run with adaptive retries when rate limiting is
// detected in tool output.
func (r *Runner) RunWithBackoff(ctx context.Context, rawURL, dest string, o RunOpts) (Result, error) {
	bo := pacing.NewBackoff(r.cfg.GDLBackoffBase, r.cfg.GDLBackoffMultiple, r.cfg.GDLMaxRunRetries)
	for {
		res, err := r.Run(ctx, rawURL, dest, o)
		if err != nil || res.OK || !pacing.LooksRateLimited(res.ErrorTail) || bo.Exhausted() {
			return res, err
		}
		d := bo.Next()
		slog.Warn("rate limited; backing off", "delay", d)
		if err := pacing.Sleep(ctx, d); err != nil {
			return res, err
		}
	}
}
