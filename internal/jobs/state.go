package jobs

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/upload"
)

type Skip struct{ Name, Reason string }

type MirrorRecord struct {
	File   string
	Links  map[string]string
	Errors map[string]string
}

type State struct {
	mu sync.Mutex

	job     *store.Job
	args    dl.Args
	Dest    string
	Extract string

	ctx    context.Context
	cancel context.CancelFunc
	proc   *exec.Cmd
	killer func()

	msg        tg.MessageRef
	lastText   string
	lastEditAt time.Time
	pinned     bool

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
	deletedBytes int64

	converting  bool
	convertFile string
	archiving   bool
	archiveFmt  string

	isRename     bool
	renameScheme string
	renameCurOld string
	renameCurNew string
	renameDone   int
	renameTotal  int
	renamedList  []string

	currentUpload string
	followUpSent  bool
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
	hostOrder     []string
	mirrorLabels  map[string]string
	mirrors       []MirrorRecord
	webMirrorDone bool
	mirrorWG      sync.WaitGroup
	maxBytes      int64

	downloadResult dl.Result
	cancelled      atomic.Bool
	fatal          string
	downloaderDone chan struct{}
	uploaderDone   chan struct{}
	dlOnce, ulOnce sync.Once
	trigger        chan struct{}

	sess      *sessions
	fileNames map[string]fsutil.FileName
}

func (s *State) SetMaxBytes(b int64) {
	s.mu.Lock()
	s.maxBytes = b
	s.mu.Unlock()
}

func (s *State) checkMaxBytesLocked(current int64) {
	if s.maxBytes > 0 && current > s.maxBytes {
		if s.fatal == "" {
			s.fatal = fmt.Sprintf("Job exceeded maximum allowed download size of %d bytes", s.maxBytes)
		}
		s.cancel()
	}
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
		trigger:   make(chan struct{}, 1),
		sess:      newSessions(),
		fileNames: make(map[string]fsutil.FileName),
	}
}

func (s *State) Job() *store.Job { s.mu.Lock(); defer s.mu.Unlock(); return s.job }

func (s *State) setJob(j *store.Job) {
	s.mu.Lock()
	s.job, s.args = j, dl.ParseArgs(j.Args)
	s.mu.Unlock()
}

func (s *State) Args() dl.Args { s.mu.Lock(); defer s.mu.Unlock(); return s.args }

func (s *State) Context() context.Context { return s.ctx }

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

func (s *State) Register(cmd *exec.Cmd) { s.mu.Lock(); s.proc = cmd; s.mu.Unlock() }

func (s *State) Unregister() { s.mu.Lock(); s.proc = nil; s.mu.Unlock() }

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

func (s *State) SetEngine(name string) { s.mu.Lock(); s.engine = name; s.mu.Unlock() }
func (s *State) SetTorrent(v bool)     { s.mu.Lock(); s.isTorrent = v; s.mu.Unlock() }

func (s *State) Bytes(p dl.Progress) {
	s.mu.Lock()
	s.downloaded = p.Current
	s.checkMaxBytesLocked(p.Current)
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

func (s *State) SpeedBytes(downloaded int64, speed float64, file string) {
	s.mu.Lock()
	s.downloaded, s.speed = downloaded, speed
	s.checkMaxBytesLocked(downloaded)
	if file != "" {
		s.currentFile = file
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) Count(n int, file, _ string) {
	s.mu.Lock()
	s.fileCount = n
	if file != "" {
		s.currentFile = file
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) Aria(pct float64, completed, speed int64, seeders, conns int, name string) {
	s.mu.Lock()
	s.determinate, s.pct, s.downloaded, s.speed = true, pct, completed, float64(speed)
	s.checkMaxBytesLocked(completed)
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

func (s *State) observeDisk(size int64, currentFile string) {
	s.mu.Lock()
	s.downloaded = size
	s.checkMaxBytesLocked(size)
	s.smoothSpeedLocked(size)
	if currentFile != "" {
		s.currentFile = currentFile
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) addDeleted(n int64) { s.mu.Lock(); s.deletedBytes += n; s.mu.Unlock() }
func (s *State) deleted() int64     { s.mu.Lock(); defer s.mu.Unlock(); return s.deletedBytes }

func (s *State) markUploaded(rel string) { s.mu.Lock(); s.uploaded[rel] = struct{}{}; s.mu.Unlock() }
func (s *State) isUploaded(rel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.uploaded[rel]
	return ok
}
func (s *State) markFailed(rel string) { s.mu.Lock(); s.failed[rel] = struct{}{}; s.mu.Unlock() }
func (s *State) beginUpload(rel, name string) {
	s.mu.Lock()
	s.uploading[rel], s.currentUpload = struct{}{}, name
	s.followUpSent = false
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

func (s *State) setHostsWithOrder(h map[string]status.HostState, order []string, labels map[string]string) {
	cp := make(map[string]status.HostState, len(h))
	for k, v := range h {
		cp[k] = v
	}
	s.mu.Lock()
	s.hosts = cp
	s.hostOrder = append([]string(nil), order...)
	s.mirrorLabels = make(map[string]string, len(labels))
	for k, v := range labels {
		s.mirrorLabels[k] = v
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) addMirror(r MirrorRecord) {
	s.mu.Lock()
	s.mirrors = append(s.mirrors, r)
	s.mu.Unlock()
}

func (s *State) Snapshot(job *store.Job, showStats bool) status.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := dl.FirstURL(job.URL)
	snap := status.Snapshot{
		JobID: job.ID, Status: string(job.Status), Target: target, SplitEnabled: job.SplitLargeFile,
		Engine: s.engine, DownloadDone: s.downloadFinished(), Determinate: s.determinate, Pct: s.pct,
		Downloaded: s.downloaded + s.deletedBytes, Expected: s.expected, Speed: s.speed,
		CurrentFile: s.currentFile, FileCount: s.fileCount, TorrentName: s.torrentName,
		Seeders: s.seeders, Peers: s.peers, IsTorrent: s.isTorrent,
		Converting: s.converting, ConvertFile: s.convertFile, Archiving: s.archiving, ArchiveFmt: s.archiveFmt,
		IsRename: s.isRename, RenameScheme: s.renameScheme,
		RenameCurOld: s.renameCurOld, RenameCurNew: s.renameCurNew,
		RenameDone: s.renameDone, RenameTotal: s.renameTotal,
		TotalFiles: job.TotalFiles, Sent: s.sent, Skipped: len(s.skipped),
		UploadFile: s.currentUpload, UploadPct: s.uploadPct, UploadSpeed: s.uploadSpeed, UploadActive: s.currentUpload != "",
		Args: s.args.Display(),
	}
	if len(s.hosts) > 0 {
		snap.Hosts = s.hosts
		if len(s.hostOrder) > 0 {
			snap.HostOrder = append([]string(nil), s.hostOrder...)
		} else {
			for k := range s.hosts {
				snap.HostOrder = append(snap.HostOrder, k)
			}
		}
		if len(s.mirrorLabels) > 0 {
			snap.MirrorLabels = make(map[string]string, len(s.mirrorLabels))
			for k, v := range s.mirrorLabels {
				snap.MirrorLabels[k] = v
			}
		} else {
			snap.MirrorLabels = make(map[string]string, len(s.hosts))
			for k := range s.hosts {
				snap.MirrorLabels[k] = upload.HostID(k).Name()
			}
		}
	}
	if showStats {
		st := status.ReadSystemStats()
		snap.Stats = &st
	}
	return snap
}

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

func (s *State) setFatal(msg string) {
	s.mu.Lock()
	if s.fatal == "" {
		s.fatal = msg
	}
	s.mu.Unlock()
	s.cancel()
}
func (s *State) fatalErr() string { s.mu.Lock(); defer s.mu.Unlock(); return s.fatal }

func (s *State) setRenameInfo(scheme string, total int) {
	s.mu.Lock()
	s.isRename = true
	s.renameScheme = scheme
	s.renameTotal = total
	s.mu.Unlock()
	s.Poke()
}

func (s *State) setRenameProgress(curOld, curNew string, done int) {
	s.mu.Lock()
	s.renameCurOld = curOld
	s.renameCurNew = curNew
	s.renameDone = done
	if curNew != "" {
		s.renamedList = append(s.renamedList, curNew)
	}
	s.mu.Unlock()
	s.Poke()
}

func (s *State) isRenameState() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isRename
}

func (s *State) renamedFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.renamedList...)
}

func (s *State) SetFileName(key string, fn fsutil.FileName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileNames == nil {
		s.fileNames = make(map[string]fsutil.FileName)
	}
	s.fileNames[key] = fn
	s.fileNames[fn.Disk] = fn
	s.fileNames[fn.Display] = fn
}

func (s *State) FileName(key string) fsutil.FileName {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileNames != nil {
		if fn, ok := s.fileNames[key]; ok {
			return fn
		}
		base := filepath.Base(key)
		if fn, ok := s.fileNames[base]; ok {
			return fn
		}
	}
	base := filepath.Base(key)
	return fsutil.FileName{Display: base, Disk: base}
}

func (s *State) FileNames() map[string]fsutil.FileName {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]fsutil.FileName, len(s.fileNames))
	for k, v := range s.fileNames {
		out[k] = v
	}
	return out
}
