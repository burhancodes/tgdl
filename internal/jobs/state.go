// Package jobs implements the download/upload pipeline and its queue manager.
package jobs

import (
	"context"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

// Skip records a file that was not uploaded and why.
type Skip struct{ Name, Reason string }

// MirrorRecord holds the web-host links produced for one file.
type MirrorRecord struct {
	File  string
	Links map[string]string // host -> url
}

// State is the live, concurrency-safe view of one job. Sources and the
// pipeline mutate it through methods; the status renderer takes Snapshots.
type State struct {
	mu sync.Mutex

	job     *store.Job
	args    dl.Args
	Dest    string
	Extract string

	ctx    context.Context
	cancel context.CancelFunc
	proc   *exec.Cmd
	killer func() // e.g. aria2 GID force-remove

	// Status message.
	msg      tg.MessageRef
	lastText string
	pinned   bool

	// Download metrics.
	engine       string
	determinate  bool
	pct          float64
	downloaded   int64
	expected     int64
	speed        float64
	speedInit    bool
	lastSpeedAt  time.Time
	lastSpeedB   int64
	currentFile  string
	fileCount    int
	torrentName  string
	seeders      int
	peers        int
	isTorrent    bool
	isPatch      bool
	patchStage   string
	deletedBytes int64

	// Post-processing flags.
	converting  bool
	convertFile string
	archiving   bool
	archiveFmt  string

	// Upload metrics.
	currentUpload string
	uploadPct     float64
	uploadSpeed   float64
	upSpeedAt     time.Time
	upSpeedB      int64
	sent          int
	totalFiles    int
	skipped       []Skip
	uploaded      map[string]struct{}
	uploading     map[string]struct{}
	failed        map[string]struct{}
	splitParts    map[string]struct{}
	sessionCount  int
	asDoc         bool
	pdLinks       []archiveLink
	hosts         map[string]status.HostState
	mirrors       []MirrorRecord
	webMirrorDone bool

	downloadResult dl.Result
	cancelled      atomic.Bool
	fatal          string
	downloaderDone chan struct{}
	uploaderDone   chan struct{}
	dlOnce, ulOnce sync.Once
	trigger        chan struct{}

	sess *sessions
}

type archiveLink struct{ Name, URL string }

func newState(job *store.Job, dest, extract string, parent context.Context) *State {
	ctx, cancel := context.WithCancel(parent)
	return &State{
		job: job, args: dl.ParseArgs(job.Args), Dest: dest, Extract: extract,
		ctx: ctx, cancel: cancel,
		msg:      tg.MessageRef{ChatID: job.ChatID, ID: job.StatusMsgID},
		uploaded: map[string]struct{}{}, uploading: map[string]struct{}{},
		failed: map[string]struct{}{}, splitParts: map[string]struct{}{},
		downloaderDone: make(chan struct{}), uploaderDone: make(chan struct{}),
		trigger: make(chan struct{}, 1),
		sess:    newSessions(),
	}
}

// Job returns the current persisted job (never nil).
func (s *State) Job() *store.Job { s.mu.Lock(); defer s.mu.Unlock(); return s.job }

func (s *State) setJob(j *store.Job) {
	s.mu.Lock()
	s.job, s.args = j, dl.ParseArgs(j.Args)
	s.mu.Unlock()
}

// Args returns the parsed job arguments.
func (s *State) Args() dl.Args { s.mu.Lock(); defer s.mu.Unlock(); return s.args }

// Context is cancelled when the job is cancelled or the manager stops.
func (s *State) Context() context.Context { return s.ctx }

// Poke asks the status updater to refresh soon.
func (s *State) Poke() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *State) finishDownload() { s.dlOnce.Do(func() { close(s.downloaderDone) }); s.Poke() }
func (s *State) finishUpload()   { s.ulOnce.Do(func() { close(s.uploaderDone) }); s.Poke() }

func (s *State) downloadFinished() bool { return isClosed(s.downloaderDone) }
func (s *State) uploadFinished() bool   { return isClosed(s.uploaderDone) }

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// ---- dl.ProcRegistry ------------------------------------------------------

// Register records the running subprocess so Cancel can kill it.
func (s *State) Register(cmd *exec.Cmd) { s.mu.Lock(); s.proc = cmd; s.mu.Unlock() }

// Unregister clears the recorded subprocess.
func (s *State) Unregister() { s.mu.Lock(); s.proc = nil; s.mu.Unlock() }

// SetKiller installs a custom cancellation hook (used by aria2).
func (s *State) SetKiller(f func()) { s.mu.Lock(); s.killer = f; s.mu.Unlock() }

func (s *State) kill() {
	s.mu.Lock()
	p, k := s.proc, s.killer
	s.mu.Unlock()
	if p != nil && p.Process != nil {
		_ = p.Process.Kill()
	}
	if k != nil {
		k()
	}
}

// ---- download metric setters ---------------------------------------------

func (s *State) SetEngine(name string) { s.mu.Lock(); s.engine = name; s.mu.Unlock() }
func (s *State) SetTorrent(v bool)     { s.mu.Lock(); s.isTorrent = v; s.mu.Unlock() }
func (s *State) SetPatch(v bool)       { s.mu.Lock(); s.isPatch = v; s.mu.Unlock() }
func (s *State) SetPatchStage(v string) {
	s.mu.Lock()
	s.patchStage = v
	s.mu.Unlock()
	s.Poke()
}

// Bytes is the ProgressFunc for byte-oriented downloaders.
func (s *State) Bytes(p dl.Progress) {
	s.mu.Lock()
	s.downloaded = p.Current
	if p.Total > 0 {
		s.expected = p.Total
		s.determinate = true
		s.pct = min(100, float64(p.Current)/float64(p.Total)*100)
	}
	if p.File != "" {
		s.currentFile = p.File
	}
	s.smoothSpeedLocked(p.Current)
	s.mu.Unlock()
	s.Poke()
}

// SpeedBytes reports cumulative bytes and an externally measured speed
// (Google Drive, MEGA).
func (s *State) SpeedBytes(downloaded int64, speed float64, file string) {
	s.mu.Lock()
	s.downloaded, s.speed = downloaded, speed
	if file != "" {
		s.currentFile = file
	}
	s.mu.Unlock()
	s.Poke()
}

// Count reports completed-file counts from subprocess engines.
func (s *State) Count(n int, file, _ string) {
	s.mu.Lock()
	s.fileCount = n
	if file != "" {
		s.currentFile = file
	}
	s.mu.Unlock()
	s.Poke()
}

// Aria reports aria2 progress.
func (s *State) Aria(pct float64, completed, speed int64, seeders, conns int, name string) {
	s.mu.Lock()
	s.determinate, s.pct, s.downloaded, s.speed = true, pct, completed, float64(speed)
	s.seeders, s.peers = seeders, conns
	if name != "" {
		s.currentFile = name
		if s.isTorrent {
			s.torrentName = name
		}
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) smoothSpeedLocked(cur int64) {
	now := time.Now()
	if s.lastSpeedAt.IsZero() {
		s.lastSpeedAt, s.lastSpeedB = now, cur
		return
	}
	dt := now.Sub(s.lastSpeedAt).Seconds()
	if dt < 0.5 {
		return
	}
	inst := float64(cur-s.lastSpeedB) / dt
	if inst < 0 {
		inst = 0
	}
	if s.speedInit {
		s.speed = 0.7*inst + 0.3*s.speed
	} else {
		s.speed, s.speedInit = inst, true
	}
	s.lastSpeedAt, s.lastSpeedB = now, cur
}

// observeDisk feeds the disk-scan monitor's measurement into the metrics.
func (s *State) observeDisk(size int64, currentFile string) {
	s.mu.Lock()
	s.downloaded = size
	s.smoothSpeedLocked(size)
	if currentFile != "" {
		s.currentFile = currentFile
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) addDeleted(n int64) { s.mu.Lock(); s.deletedBytes += n; s.mu.Unlock() }
func (s *State) deleted() int64     { s.mu.Lock(); defer s.mu.Unlock(); return s.deletedBytes }

// ---- upload bookkeeping ---------------------------------------------------

func (s *State) markUploaded(rel string) { s.mu.Lock(); s.uploaded[rel] = struct{}{}; s.mu.Unlock() }
func (s *State) markFailed(rel string)   { s.mu.Lock(); s.failed[rel] = struct{}{}; s.mu.Unlock() }
func (s *State) beginUpload(rel, name string) {
	s.mu.Lock()
	s.uploading[rel], s.currentUpload = struct{}{}, name
	s.uploadPct, s.uploadSpeed = 0, 0
	s.upSpeedAt, s.upSpeedB = time.Time{}, 0
	s.mu.Unlock()
	s.Poke()
}
func (s *State) endUpload(rel string) {
	s.mu.Lock()
	delete(s.uploading, rel)
	s.currentUpload, s.uploadPct, s.uploadSpeed = "", 0, 0
	s.mu.Unlock()
	s.Poke()
}
func (s *State) addSkip(name, why string) {
	s.mu.Lock()
	s.skipped = append(s.skipped, Skip{name, why})
	s.mu.Unlock()
}
func (s *State) incSent() int { s.mu.Lock(); defer s.mu.Unlock(); s.sent++; return s.sent }
func (s *State) counts() (sent, skipped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent, len(s.skipped)
}
func (s *State) isPending(rel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.uploaded[rel]; ok {
		return false
	}
	if _, ok := s.uploading[rel]; ok {
		return false
	}
	_, failed := s.failed[rel]
	return !failed
}
func (s *State) addSplitParts(rels ...string) {
	s.mu.Lock()
	for _, r := range rels {
		s.splitParts[r] = struct{}{}
	}
	s.mu.Unlock()
}
func (s *State) isSplitPart(keys ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if _, ok := s.splitParts[k]; ok {
			return true
		}
	}
	return false
}

// UploadProgress is the tg.ProgressFunc for the current upload.
func (s *State) UploadProgress(cur, total int64) {
	s.mu.Lock()
	if total > 0 {
		s.uploadPct = float64(cur) / float64(total) * 100
	}
	now := time.Now()
	if s.upSpeedAt.IsZero() {
		s.upSpeedAt, s.upSpeedB = now, cur
	} else if dt := now.Sub(s.upSpeedAt).Seconds(); dt >= 1 {
		s.uploadSpeed = max(0, float64(cur-s.upSpeedB)/dt)
		s.upSpeedAt, s.upSpeedB = now, cur
	}
	s.mu.Unlock()
}

func (s *State) setConverting(on bool, file string) {
	s.mu.Lock()
	s.converting, s.convertFile = on, file
	s.mu.Unlock()
	s.Poke()
}
func (s *State) setArchiving(on bool, fmt string) {
	s.mu.Lock()
	s.archiving, s.archiveFmt = on, fmt
	s.mu.Unlock()
	s.Poke()
}

func (s *State) addPDLink(name, url string) {
	s.mu.Lock()
	s.pdLinks = append(s.pdLinks, archiveLink{name, url})
	s.mu.Unlock()
}

func (s *State) setHosts(h map[string]status.HostState) {
	cp := make(map[string]status.HostState, len(h))
	for k, v := range h {
		cp[k] = v
	}
	s.mu.Lock()
	s.hosts = cp
	s.mu.Unlock()
	s.Poke()
}

func (s *State) addMirror(r MirrorRecord) { s.mu.Lock(); s.mirrors = append(s.mirrors, r); s.mu.Unlock() }

// Snapshot copies the state into a renderable status.Snapshot.
func (s *State) Snapshot(job *store.Job, showStats bool) status.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := dl.FirstURL(job.URL)
	snap := status.Snapshot{
		JobID: job.ID, Status: string(job.Status), Target: target, SplitEnabled: job.SplitLargeFile,
		Engine: s.engine, DownloadDone: s.downloadFinished(), Determinate: s.determinate, Pct: s.pct,
		Downloaded: s.downloaded + s.deletedBytes, Expected: s.expected, Speed: s.speed,
		CurrentFile: s.currentFile, FileCount: s.fileCount, TorrentName: s.torrentName,
		Seeders: s.seeders, Peers: s.peers, IsTorrent: s.isTorrent, IsPatch: s.isPatch, PatchStage: s.patchStage,
		Converting: s.converting, ConvertFile: s.convertFile, Archiving: s.archiving, ArchiveFmt: s.archiveFmt,
		TotalFiles: job.TotalFiles, Sent: s.sent, Skipped: len(s.skipped),
		UploadFile: s.currentUpload, UploadPct: s.uploadPct, UploadSpeed: s.uploadSpeed, UploadActive: s.currentUpload != "",
		Args: s.args.Display(),
	}
	if s.isPatch {
		snap.PatchInput = s.args.OriginalFilename
		if snap.PatchInput == "" {
			snap.PatchInput = "app.apk"
		}
		snap.PatchOutput = patchedName(snap.PatchInput)
	}
	if len(s.hosts) > 0 {
		snap.Hosts = s.hosts
		snap.HostOrder = []string{"gofile", "fileditch", "pixeldrain"}
		snap.MirrorLabels = map[string]string{"gofile": "GoFile", "fileditch": "FileDitch", "pixeldrain": "Pixeldrain"}
	}
	if showStats {
		st := status.ReadSystemStats()
		snap.Stats = &st
	}
	return snap
}

func patchedName(orig string) string {
	if len(orig) > 4 && (orig[len(orig)-4:] == ".apk" || orig[len(orig)-4:] == ".APK") {
		return orig[:len(orig)-4] + "_patched.apk"
	}
	return orig + "_patched.apk"
}

// sortedSkips returns skips in insertion order (copy).
func (s *State) skips() []Skip {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Skip(nil), s.skipped...)
	return out
}

func (s *State) mirrorRecords() []MirrorRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MirrorRecord(nil), s.mirrors...)
}

func (s *State) pdLinkList() []archiveLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]archiveLink(nil), s.pdLinks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *State) setResult(r dl.Result) { s.mu.Lock(); s.downloadResult = r; s.mu.Unlock() }
func (s *State) result() dl.Result     { s.mu.Lock(); defer s.mu.Unlock(); return s.downloadResult }

// setFatal aborts the whole job with a user-visible error.
func (s *State) setFatal(msg string) {
	s.mu.Lock()
	if s.fatal == "" {
		s.fatal = msg
	}
	s.mu.Unlock()
	s.cancel()
}
func (s *State) fatalErr() string { s.mu.Lock(); defer s.mu.Unlock(); return s.fatal }
