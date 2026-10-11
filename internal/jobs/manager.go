package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/burhanverse/tgdl/internal/archive"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/media"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

// Manager schedules jobs and runs the download → post-process → upload
// pipeline. One goroutine group per job; global semaphores bound concurrency.
type Manager struct {
	d       *Deps
	sources []Source
	lim     *pacing.TelegramLimiter
	up      *tgUploader

	dlSem    *semaphore.Weighted
	ulSem    *semaphore.Weighted
	heavySem *semaphore.Weighted

	mu     sync.Mutex
	states map[string]*State
	wg     sync.WaitGroup
	root   context.Context
	stop   context.CancelFunc
}

// NewManager wires a manager. lim is shared with the Telegram-facing code.
func NewManager(d *Deps, lim *pacing.TelegramLimiter) *Manager {
	media.SetTempDir(filepath.Join(d.Cfg.DataDir, "tmp"))
	hs := semaphore.NewWeighted(int64(d.Cfg.MaxConcurrentHeavy))
	d.HeavySem = hs
	return &Manager{
		d: d, sources: DefaultSources(d), lim: lim,
		up:       newTGUploader(d.TG, lim, d.Cfg.UploadLimit(), d.Cfg.AlbumMaxTotalBytes),
		dlSem:    semaphore.NewWeighted(int64(d.Cfg.MaxConcurrentDL)),
		ulSem:    semaphore.NewWeighted(int64(d.Cfg.MaxConcurrentUL)),
		heavySem: hs,
		states:   map[string]*State{},
	}
}

// Start resumes unfinished jobs and begins background maintenance.
func (m *Manager) Start(ctx context.Context) error {
	m.root, m.stop = context.WithCancel(ctx)
	pending, err := m.d.Store.ResumableJobs(ctx)
	if err != nil {
		return fmt.Errorf("load resumable jobs: %w", err)
	}
	m.cleanupOrphans(pending)
	m.cleanupOldTempFiles()
	for _, j := range pending {
		slog.Info("resuming job", "job", j.ID, "status", j.Status)
		m.Add(j.ID)
	}
	go m.lim.RunSweeper(m.root, m.activeChats)
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-m.root.Done():
				return
			case <-t.C:
				if p, err := m.d.Store.ResumableJobs(m.root); err == nil {
					m.cleanupOrphans(p)
				}
				m.cleanupOldTempFiles()
			}
		}
	}()
	return nil
}

// Stop cancels running jobs and waits briefly for them to wind down.
func (m *Manager) Stop() {
	if m.stop != nil {
		m.stop()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		slog.Warn("timed out waiting for jobs to stop")
	}
}

func (m *Manager) cleanupOrphans(keep []*store.Job) {
	entries, err := os.ReadDir(m.d.Cfg.DownloadsDir())
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, j := range keep {
		base := j.DownloadDir()
		live[base] = true
		live[base+"_extract"] = true
		live[base+"_patch_work"] = true
		live[base+"_pd_temp"] = true
	}
	m.mu.Lock()
	for _, s := range m.states {
		base := s.Job().DownloadDir()
		live[base] = true
		live[base+"_extract"] = true
		live[base+"_patch_work"] = true
		live[base+"_pd_temp"] = true
	}
	m.mu.Unlock()

	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		isTarget := strings.HasPrefix(name, "job_") ||
			strings.HasSuffix(name, "_extract") ||
			strings.HasSuffix(name, "_patch_work") ||
			strings.HasPrefix(name, "tmp_up_") ||
			strings.HasPrefix(name, "tmp_conf_") ||
			strings.HasSuffix(name, "_pd_temp")

		if !isTarget || live[name] {
			continue
		}

		if strings.HasPrefix(name, "tmp_up_") || strings.HasPrefix(name, "tmp_conf_") {
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) < 10*time.Minute {
				continue
			}
		}

		slog.Info("removing orphaned directory", "dir", name)
		_ = os.RemoveAll(filepath.Join(m.d.Cfg.DownloadsDir(), name))
	}
}

func (m *Manager) cleanupOldTempFiles() {
	now := time.Now()
	cleanDir := func(dir string, match func(string) bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !match(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) > time.Hour {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}

	cleanDir(filepath.Join(m.d.Cfg.DataDir, "tmp"), func(name string) bool { return true })
	cleanDir("/tmp", func(name string) bool {
		return strings.HasSuffix(name, ".jpg") && (strings.Contains(name, "thumb") || strings.Contains(name, "_shot_"))
	})
}

func (m *Manager) activeChats() map[int64]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]bool{}
	for _, s := range m.states {
		out[s.Job().ChatID] = true
	}
	return out
}

// Add schedules a job that already exists in the store.
func (m *Manager) Add(id string) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(id)
	}()
}

// Cancel aborts a running job. It returns false when the job is not running
// (the caller should then just mark it cancelled in the store).
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	st := m.states[id]
	m.mu.Unlock()
	if st == nil {
		return false
	}
	st.cancelled.Store(true)
	st.cancel()
	st.kill()
	return true
}

// State returns the live state of a running job, or nil.
func (m *Manager) State(id string) *State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[id]
}

// Running returns live states, optionally filtered to one chat (0 = all).
func (m *Manager) Running(chatID int64) []*State {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*State
	for _, s := range m.states {
		if chatID == 0 || s.Job().ChatID == chatID {
			out = append(out, s)
		}
	}
	return out
}

// Render produces the current card for a running job.
func (m *Manager) Render(st *State) string {
	return status.Job(st.Snapshot(st.Job(), m.d.Cfg.ShowSystemStats))
}

// ---- interactive callbacks -------------------------------------------------

// SetArchiveChoice resolves the archive prompt; it returns the file name.
func (m *Manager) SetArchiveChoice(jobID, archiveID, choice string) (string, bool) {
	st := m.State(jobID)
	if st == nil {
		return "", false
	}
	name := st.sess.archiveName(archiveID)
	st.sess.setArchiveChoice(archiveID, choice)
	return filepath.Base(name), true
}

// SetAudioChoice resolves the audio-conversion prompt.
func (m *Manager) SetAudioChoice(jobID, convID, choice string) (string, bool) {
	st := m.State(jobID)
	if st == nil {
		return "", false
	}
	name := st.sess.convName(convID)
	st.sess.setAudioChoice(convID, choice)
	return name, true
}

// DeliverPassword routes a reply to a password prompt to the waiting job.
func (m *Manager) DeliverPassword(chatID int64, replyToMsgID int, password string) bool {
	for _, st := range m.Running(chatID) {
		if st.sess.deliverPassword(replyToMsgID, password) {
			return true
		}
	}
	return false
}

// ---- job lifecycle -----------------------------------------------------------

func (m *Manager) run(id string) {
	job, err := m.d.Store.GetJob(m.root, id)
	if err != nil {
		slog.Error("load job failed", "job", id, "err", err)
		return
	}
	if job.Status.IsTerminal() {
		return
	}
	dest := filepath.Join(m.d.Cfg.DownloadsDir(), job.DownloadDir())
	st := newState(job, dest, dest+"_extract", m.root)
	if m.d.Cfg.HasMaxTotalDownloads {
		st.SetMaxBytes(m.d.Cfg.MaxTotalDownloadsB)
	}

	m.mu.Lock()
	if _, exists := m.states[id]; exists {
		m.mu.Unlock()
		st.cancel()
		return
	}
	m.states[id] = st
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.states, id)
		m.mu.Unlock()
		st.cancel()
	}()

	m.ensureStatusMessage(st)

	stopStatus := make(chan struct{})
	statusExited := make(chan struct{})
	go func() { defer close(statusExited); m.statusLoop(st, stopStatus) }()

	go m.downloadStage(st)
	go m.uploadStage(st)
	<-st.downloaderDone
	<-st.uploaderDone

	close(stopStatus)
	<-statusExited
	m.finalize(st)
}

func (m *Manager) ensureStatusMessage(st *State) {
	job := st.Job()
	if job.StatusMsgID != 0 {
		return
	}
	target := status.Queued(job.ID, dl.FirstURL(job.URL), st.Args().Display())
	ref, err := m.d.TG.Send(st.ctx, job.ChatID, target, tg.SendOpts{Keyboard: cancelKeyboard(job.ID), Silent: true})
	if err != nil {
		slog.Warn("could not send status message", "job", job.ID, "err", err)
		return
	}
	st.mu.Lock()
	st.msg = ref
	st.mu.Unlock()
	_ = m.d.Store.SetStatusMessage(context.Background(), job.ID, ref.ID)
}

func cancelKeyboard(jobID string) tg.Keyboard {
	return tg.Keyboard{{tg.Button{Text: "Cancel", Data: "cancel_job:" + jobID}}}
}

func (m *Manager) setStatus(st *State, s store.Status) {
	if err := m.d.Store.SetStatus(context.Background(), st.Job().ID, s); err != nil {
		slog.Warn("persist status failed", "err", err)
	}
	st.mu.Lock()
	j := *st.job
	j.Status = s
	st.job = &j
	st.mu.Unlock()
	st.Poke()
}

func (m *Manager) statusLoop(st *State, stop <-chan struct{}) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-st.ctx.Done():
			return
		case <-st.trigger:
		case <-t.C:
		}
		m.render(st, false)
	}
}

func (m *Manager) render(st *State, force bool) {
	st.mu.Lock()
	ref, last := st.msg, st.lastText
	st.mu.Unlock()
	if !ref.Valid() {
		return
	}
	text := m.Render(st)
	if text == last && !force {
		return
	}
	ctx := st.ctx
	if ctx.Err() != nil {
		ctx = context.Background()
	}
	if err := m.lim.Acquire(ctx, ref.ChatID); err != nil {
		return
	}
	err := m.d.TG.Edit(ctx, ref, text, tg.SendOpts{Keyboard: cancelKeyboard(st.Job().ID)})
	if secs, ok := tg.IsFlood(err); ok {
		m.lim.NotifyFloodWait(secs, ref.ChatID)
		return
	}
	if err != nil {
		slog.Debug("status edit failed", "job", st.Job().ID, "err", err)
		return
	}
	st.mu.Lock()
	st.lastText = text
	st.mu.Unlock()
}

// send is a best-effort notification that never fails the job.
func (m *Manager) send(st *State, html string, o tg.SendOpts) tg.MessageRef {
	ctx := st.ctx
	if ctx.Err() != nil {
		ctx = context.Background()
	}
	_ = m.lim.Acquire(ctx, st.Job().ChatID)
	ref, err := m.d.TG.Send(ctx, st.Job().ChatID, html, o)
	if err != nil {
		slog.Debug("send failed", "err", err)
	}
	return ref
}

func (m *Manager) del(ref tg.MessageRef) {
	if !ref.Valid() {
		return
	}
	if err := m.d.TG.Delete(context.Background(), ref); err != nil {
		slog.Debug("delete failed", "err", err)
	}
}

func (m *Manager) delLater(ref tg.MessageRef, d time.Duration) {
	if !ref.Valid() {
		return
	}
	time.AfterFunc(d, func() { m.del(ref) })
}

// ---- download stage ---------------------------------------------------------

func pickSource(srcs []Source, r *Request) Source {
	// An explicit engine choice (from the command used) beats URL sniffing.
	if e := r.Args.Engine; e != "" {
		for _, s := range srcs {
			if s.Name() == e {
				return s
			}
		}
	}
	for _, s := range srcs {
		if s.Match(r) {
			return s
		}
	}
	return srcs[len(srcs)-1]
}

func (m *Manager) downloadStage(st *State) {
	defer st.finishDownload()
	job := st.Job()
	if err := m.dlSem.Acquire(st.ctx, 1); err != nil {
		st.setResult(dl.Result{ErrorTail: "cancelled"})
		return
	}
	defer m.dlSem.Release(1)
	m.setStatus(st, store.StatusDownloading)

	// Check free disk space before starting
	if free, err := fsutil.FreeDiskSpace(m.d.Cfg.DownloadsDir()); err == nil {
		if int64(free) < m.d.Cfg.MinFreeDiskBytes {
			errMsg := fmt.Sprintf("insufficient free disk space before download: %s free, %s required",
				status.Size(float64(free)), status.Size(float64(m.d.Cfg.MinFreeDiskBytes)))
			st.setFatal(errMsg)
			st.setResult(dl.Result{ErrorTail: errMsg})
			return
		}
	}

	if err := os.MkdirAll(st.Dest, 0o755); err != nil {
		st.setResult(dl.Result{ErrorTail: err.Error()})
		return
	}
	args := st.Args()
	req := &Request{Job: job, Args: args, URL: dl.FirstURL(job.URL), Dest: st.Dest, St: st}
	src := pickSource(m.sources, req)
	slog.Info("starting download", "job", job.ID, "source", src.Name())

	// Every 5s during download, check free disk space
	diskCtx, cancelDisk := context.WithCancel(st.ctx)
	defer cancelDisk()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-diskCtx.Done():
				return
			case <-t.C:
				if free, err := fsutil.FreeDiskSpace(m.d.Cfg.DownloadsDir()); err == nil {
					if int64(free) < m.d.Cfg.MinFreeDiskBytes {
						errMsg := fmt.Sprintf("free disk space dropped below minimum limit (%s < %s)",
							status.Size(float64(free)), status.Size(float64(m.d.Cfg.MinFreeDiskBytes)))
						st.setFatal(errMsg)
						st.cancel()
						st.kill()
						return
					}
				}
			}
		}
	}()

	stopMon := func() {}
	if diskMonitored(src) {
		ctx, cancel := context.WithCancel(st.ctx)
		go m.monitorDisk(ctx, st)
		stopMon = cancel
	}
	res := src.Fetch(st.ctx, req)
	stopMon()
	cancelDisk()
	if st.ctx.Err() != nil {
		res = dl.Result{ErrorTail: "cancelled"}
		st.setResult(res)
		return
	}

	if res.OK && args.ArchiveFormat != "" {
		st.setArchiving(true, args.ArchiveFormat)
		if err := m.archiveAll(st, args); err != nil {
			slog.Error("archiving failed", "job", job.ID, "err", err)
		}
		st.setArchiving(false, "")
	}

	if res.OK {
		total := len(fsutil.ListUploadable(st.Dest))
		_ = m.d.Store.UpdateProgress(context.Background(), job.ID, store.Update{TotalFiles: &total})
		st.mu.Lock()
		j := *st.job
		j.TotalFiles = total
		st.job = &j
		st.mu.Unlock()
	}
	st.setResult(res)
}

func (m *Manager) monitorDisk(ctx context.Context, st *State) {
	interval := 2 * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			var totalSize int64
			var fileCount int
			var newestFile string
			var newestTime time.Time

			_ = filepath.WalkDir(st.Dest, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if fsutil.ShouldIgnoreName(d.Name()) && path != st.Dest {
						return filepath.SkipDir
					}
					return nil
				}
				if fsutil.ShouldIgnoreName(d.Name()) {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				sz := info.Size()
				if sz == 0 {
					return nil
				}
				totalSize += sz
				fileCount++
				mt := info.ModTime()
				if mt.After(newestTime) {
					newestTime = mt
					newestFile = d.Name()
				}
				return nil
			})

			st.observeDisk(totalSize, newestFile)

			if free, err := fsutil.FreeDiskSpace(m.d.Cfg.DownloadsDir()); err == nil && int64(free) < m.d.Cfg.MinFreeDiskBytes {
				errMsg := fmt.Sprintf("free disk space dropped below minimum limit (%s < %s)",
					status.Size(float64(free)), status.Size(float64(m.d.Cfg.MinFreeDiskBytes)))
				st.setFatal(errMsg)
				st.cancel()
				st.kill()
				return
			}

			// Adaptive interval: 2s when < 500 files, up to 10s when > 5000 files
			if fileCount < 500 {
				interval = 2 * time.Second
			} else if fileCount >= 5000 {
				interval = 10 * time.Second
			} else {
				fraction := float64(fileCount-500) / 4500.0
				interval = 2*time.Second + time.Duration(fraction*float64(8*time.Second))
			}
			timer.Reset(interval)
		}
	}
}

// archiveAll compresses every top-level folder (and loose files) into archives
// so they upload as a small number of files.
func (m *Manager) archiveAll(st *State, args dl.Args) error {
	entries, err := os.ReadDir(st.Dest)
	if err != nil {
		return err
	}
	var mirror archive.Uploader
	if args.MirrorPixeldrain {
		uid := args.UserIDInt()
		mirror = func(ctx context.Context, p string) (string, error) { return m.d.Hosts.Pixeldrain(ctx, p, uid, nil) }
	}
	tempDir := filepath.Join(m.d.Cfg.DownloadsDir(), st.Job().ID+"_pd_temp")
	opts := archive.FolderOpts{
		Format:     args.ArchiveFormat,
		MirrorPD:   mirror,
		OnMirror:   func(l archive.Link) { st.addPDLink(l.Name, l.URL) },
		MirrorDone: &st.mirrorWG,
		TempDir:    tempDir,
		Threads:    m.d.Cfg.ArchiveThreads,
		ZipLevel:   m.d.Cfg.ArchiveZipLevel,
	}

	if m.heavySem != nil {
		if err := m.heavySem.Acquire(st.ctx, 1); err != nil {
			return err
		}
		defer m.heavySem.Release(1)
	}

	var loose []string
	for _, e := range entries {
		p := filepath.Join(st.Dest, e.Name())
		if e.IsDir() {
			if _, err := archive.ArchiveFolder(st.ctx, p, opts); err != nil {
				slog.Warn("archive folder failed; keeping files", "dir", e.Name(), "err", err)
			}
		} else {
			loose = append(loose, p)
		}
	}
	if len(loose) > 0 {
		dir := filepath.Join(st.Dest, "Files")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, f := range loose {
			_ = os.Rename(f, filepath.Join(dir, filepath.Base(f)))
		}
		if _, err := archive.ArchiveFolder(st.ctx, dir, opts); err != nil {
			slog.Warn("archive loose files failed", "err", err)
		}
	}
	return nil
}

// ---- finalisation -------------------------------------------------------------

func (m *Manager) finalize(st *State) {
	job := st.Job()
	ctx := context.Background()

	// Wait for background Pixeldrain mirroring if any
	st.mirrorWG.Wait()

	defer func() {
		_ = os.RemoveAll(st.Dest)
		_ = os.RemoveAll(st.Extract)
		_ = os.RemoveAll(filepath.Join(m.d.Cfg.DownloadsDir(), job.ID+"_pd_temp"))
	}()

	sent, skipped := st.counts()
	res := st.result()
	fatal := st.fatalErr()

	var final store.Status
	var text string
	switch {
	case st.cancelled.Load() && fatal == "":
		final = store.StatusCancelled
		text = fmt.Sprintf("<b>Job #%s Cancelled</b>\n<blockquote>Cancelled by user.</blockquote>", status.Esc(job.ID))
	case m.root.Err() != nil && fatal == "":
		// Shutting down: leave the job resumable.
		slog.Info("job interrupted by shutdown; will resume on restart", "job", job.ID)
		return
	case fatal != "":
		final = store.StatusFailed
		text = "<b>Job failed</b>: " + status.Code(status.Short(fatal, 500))
	case !res.OK && sent == 0:
		final = store.StatusFailed
		msg := res.ErrorTail
		if msg == "" {
			msg = "Download failed."
		}
		text = fmt.Sprintf("<b>Job #%s failed</b>\n<blockquote>%s</blockquote>", status.Esc(job.ID), status.Code(status.Short(tailChars(msg, 1500), 800)))
	default:
		final = store.StatusDone
		text = m.summary(st, sent, skipped)
	}

	errText := ""
	if final == store.StatusFailed {
		errText = tailChars(firstNonEmpty(fatal, res.ErrorTail), 1500)
	}
	empty := ""
	up := store.Update{Status: &final, SentFiles: &sent, SkippedFiles: &skipped, URL: &empty}
	if errText != "" {
		up.Error = &errText
	}
	if err := m.d.Store.UpdateProgress(ctx, job.ID, up); err != nil {
		slog.Error("persist final state failed", "job", job.ID, "err", err)
	}

	st.mu.Lock()
	ref := st.msg
	st.mu.Unlock()
	if ref.Valid() {
		if err := m.d.TG.Edit(ctx, ref, text, tg.SendOpts{}); err != nil {
			m.send(st, text, tg.SendOpts{})
		}
	} else {
		m.send(st, text, tg.SendOpts{})
	}
}

func (m *Manager) summary(st *State, sent, skipped int) string {
	job := st.Job()
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Job #%s done.</b> Uploaded %d file(s).", status.Esc(job.ID), sent)
	if skipped > 0 {
		fmt.Fprintf(&b, " Skipped %d.", skipped)
	}
	if mirrors := st.mirrorRecords(); len(mirrors) > 0 {
		b.WriteString("\n\n<b>Mirror links</b>")
		for _, r := range mirrors {
			fmt.Fprintf(&b, "\n<code>%s</code>", status.Esc(r.File))
			for _, host := range []string{"gofile", "fileditch", "pixeldrain"} {
				if u := r.Links[host]; u != "" {
					fmt.Fprintf(&b, "\n• <a href=\"%s\">%s</a>", status.Esc(u), host)
				}
			}
		}
	}
	if sk := st.skips(); len(sk) > 0 {
		b.WriteString("\n\n<b>Skipped</b>")
		for i, s := range sk {
			if i == 20 {
				fmt.Fprintf(&b, "\n…and %d more", len(sk)-20)
				break
			}
			fmt.Fprintf(&b, "\n• %s (%s)", status.Code(s.Name), status.Esc(status.Short(s.Reason, 80)))
		}
	}
	if links := st.pdLinkList(); len(links) > 0 {
		b.WriteString("\n\n<b>Pixeldrain mirror links</b>")
		for _, l := range links {
			fmt.Fprintf(&b, "\n• %s: %s", status.Code(l.Name), status.Esc(l.URL))
		}
	}
	return b.String()
}

func tailChars(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

var errNoPassword = errors.New("timed out waiting for archive password")
