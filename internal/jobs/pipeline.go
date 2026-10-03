package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/burhanverse/tgdl/internal/archive"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/media"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/tg"
)

const (
	promptTimeout   = 15 * time.Second
	passwordTimeout = 5 * time.Minute
	stableFor       = 3 * time.Second
)

type seen struct {
	size int64
	at   time.Time
}

// uploadStage is the consumer half of the pipeline. Depending on the source it
// either streams files as they finish downloading or waits for the whole set.
func (m *Manager) uploadStage(st *State) {
	defer st.finishUpload()
	job := st.Job()
	args := st.Args()

	if done, err := m.d.Store.UploadedFilenames(context.Background(), job.ID); err == nil {
		for f := range done {
			st.markUploaded(f)
		}
	}

	first := dl.FirstURL(job.URL)
	waitAll := isTorrentURL(first) || args.ArchiveFormat != "" || args.Unzip || args.Engine == "aria2" ||
		strings.HasPrefix(first, "patch:") || strings.HasPrefix(first, "unzip:")
	if waitAll {
		select {
		case <-st.downloaderDone:
		case <-st.ctx.Done():
			return
		}
	}

	track := map[string]seen{}
	for {
		if st.ctx.Err() != nil {
			return
		}
		done := st.downloadFinished()
		files := readyFiles(st, track, done)
		if len(files) == 0 {
			if done {
				return
			}
			if err := pacing.Sleep(st.ctx, 1500*time.Millisecond); err != nil {
				return
			}
			continue
		}
		if groups := groupSplitParts(st, files); len(groups) > 0 {
			for _, g := range groups {
				m.uploadSplitGroup(st, g)
			}
			continue
		}
		for _, f := range files {
			if st.ctx.Err() != nil {
				return
			}
			m.processFile(st, st.Dest, f, false)
		}
	}
}

// readyFiles lists files that are pending and (unless the download is over)
// have stopped growing.
func readyFiles(st *State, track map[string]seen, done bool) []string {
	var out []string
	for _, f := range fsutil.ListUploadable(st.Dest) {
		rel, _ := filepath.Rel(st.Dest, f)
		if !st.isPending(rel) {
			continue
		}
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if !done {
			prev, ok := track[f]
			now := time.Now()
			if !ok || prev.size != info.Size() {
				track[f] = seen{info.Size(), now}
				continue
			}
			if now.Sub(prev.at) < stableFor || now.Sub(info.ModTime()) < stableFor {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// groupSplitParts collects sets of ≥2 split parts that share a base name,
// produced by this job's own splitting, so they can go out as albums.
func groupSplitParts(st *State, files []string) [][]string {
	byKey := map[string][]string{}
	var order []string
	for _, f := range files {
		rel, _ := filepath.Rel(st.Dest, f)
		if !st.isSplitPart(rel, filepath.Base(f)) {
			continue
		}
		key, ok := SplitGroupKey(filepath.Base(f))
		if !ok {
			continue
		}
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], f)
	}
	var out [][]string
	for _, k := range order {
		if len(byKey[k]) > 1 {
			out = append(out, byKey[k])
		}
	}
	return out
}

func (m *Manager) uploadSplitGroup(st *State, files []string) {
	chat := st.Job().ChatID
	if err := m.ulSem.Acquire(st.ctx, 1); err != nil {
		return
	}
	defer m.ulSem.Release(1)
	for _, f := range files {
		rel, _ := filepath.Rel(st.Dest, f)
		st.beginUpload(rel, filepath.Base(f))
	}
	asDoc, err := m.up.UploadGroup(st.ctx, chat, files, st.asDocFlag(), st.UploadProgress)
	st.setAsDoc(asDoc)
	for _, f := range files {
		rel, _ := filepath.Rel(st.Dest, f)
		st.endUpload(rel)
		if err != nil {
			st.addSkip(filepath.Base(f), "error: "+err.Error())
			st.markFailed(rel)
			continue
		}
		m.afterUpload(st, f, rel)
	}
	m.pace(st)
}

func (s *State) asDocFlag() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.asDoc }
func (s *State) setAsDoc(v bool) {
	s.mu.Lock()
	s.asDoc = s.asDoc || v
	s.mu.Unlock()
}

// afterUpload records success, deletes the local copy and refreshes counters.
func (m *Manager) afterUpload(st *State, f, rel string) {
	job := st.Job()
	if info, err := os.Stat(f); err == nil {
		st.addDeleted(info.Size())
		_ = os.Remove(f)
	}
	_ = m.d.Store.MarkUploaded(context.Background(), job.ID, rel)
	st.markUploaded(rel)
	sent := st.incSent()
	skipped := len(st.skips())
	_ = m.d.Store.UpdateProgress(context.Background(), job.ID, store2(sent, skipped))
	m.logUpload(job.ID, filepath.Base(f))
	st.Poke()
}

func (m *Manager) logUpload(jobID, name string) {
	p := filepath.Join(m.d.Cfg.LogDir, "uploads.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s - Job #%s - Uploaded: %s\n", time.Now().Format("2006-01-02 15:04:05"), jobID, name)
}

// pace sleeps between uploads to stay under Telegram's radar.
func (m *Manager) pace(st *State) {
	st.mu.Lock()
	st.sessionCount++
	n := st.sessionCount
	st.mu.Unlock()
	mult := m.up.mult()
	c := m.d.Cfg
	if c.TGBatchSize > 0 && n%c.TGBatchSize == 0 {
		_ = pacing.Sleep(st.ctx, time.Duration(float64(c.TGBatchCooldown)*mult))
		return
	}
	_ = pacing.RandomDelay(st.ctx, c.TGUploadDelayMin, c.TGUploadDelayMax, mult)
}

// processFile drives one file through archive handling, conversion, splitting
// and upload/mirror. root is the directory rel paths are computed against.
func (m *Manager) processFile(st *State, root, f string, nested bool) {
	job := st.Job()
	args := st.Args()
	name := filepath.Base(f)
	rel, _ := filepath.Rel(root, f)
	key := rel
	if root != st.Dest {
		key = "extract/" + rel
	}
	if !st.isPending(key) {
		return
	}
	if _, err := os.Stat(f); err != nil {
		return
	}
	f = fsutil.EnsureExtension(f)
	name = filepath.Base(f)

	// Archives.
	if !nested {
		isSplitPart := st.isSplitPart(rel, name)
		isArchive := (archive.IsArchiveExt(f) && !isSplitPart) || archive.IsFirstSplitPartOfArchive(name)
		if isArchive {
			skipUpload := m.handleArchive(st, f, rel, key, args.Unzip)
			if st.ctx.Err() != nil {
				return
			}
			if skipUpload {
				return
			}
		} else if args.Unzip {
			if si := archive.SplitArchiveInfo(name); si != nil && si.Part > 1 {
				// Later volumes are consumed with part one; never upload them.
				return
			}
		}
	}

	// Video → MKV remux for players that dislike odd containers.
	if media.ConversionExt[media.Ext(f)] && !st.sess.convertedBefore(name) {
		st.sess.markConverted(name)
		f, key = m.convertVideo(st, f, root, key)
		name = filepath.Base(f)
	}

	// Audio → MP3 (asks first).
	if media.AudioConversionExt[media.Ext(f)] && !args.IsMirror {
		f, key = m.maybeConvertAudio(st, f, root, key)
		name = filepath.Base(f)
	}

	// Oversized files.
	limit := m.d.Cfg.UploadLimit()
	if !args.IsMirror || args.UploadTG {
		parts := media.HandleLarge(st.ctx, f, job.SplitLargeFile, limit)
		switch {
		case len(parts) == 0:
			st.addSkip(name, "exceeds upload limit")
			st.markFailed(key)
			return
		case len(parts) > 1 || parts[0] != f:
			for _, p := range parts {
				pr, _ := filepath.Rel(root, p)
				st.addSplitParts(pr, filepath.Base(p))
			}
			_ = m.d.Store.MarkUploaded(context.Background(), job.ID, key)
			st.markUploaded(key)
			return // parts are picked up on the next scan
		}
	}

	// Web mirror (GoFile / FileDitch / Pixeldrain).
	if args.IsMirror {
		st.beginUpload(key, name)
		rec := m.mirrorFile(st, f)
		st.endUpload(key)
		st.addMirror(rec)
		if !args.UploadTG {
			m.afterUpload(st, f, key)
			return
		}
	}

	// Telegram upload.
	if err := m.ulSem.Acquire(st.ctx, 1); err != nil {
		return
	}
	st.beginUpload(key, name)
	m.setStatus(st, "uploading")
	asDoc, err := m.up.Upload(st.ctx, job.ChatID, f, st.asDocFlag(), st.UploadProgress)
	m.ulSem.Release(1)
	st.endUpload(key)
	st.setAsDoc(asDoc)
	if err != nil {
		if st.ctx.Err() != nil {
			return
		}
		slog.Error("upload failed", "job", job.ID, "file", name, "err", err)
		reason := "error: " + err.Error()
		if errors.Is(err, ErrTooLarge) {
			reason = "too large"
		}
		st.addSkip(name, reason)
		st.markFailed(key)
		m.pace(st)
		return
	}
	m.afterUpload(st, f, key)
	m.pace(st)
}

// handleArchive returns true when the archive itself must not be uploaded.
func (m *Manager) handleArchive(st *State, f, rel, key string, unzipJob bool) (skipUpload bool) {
	job := st.Job()
	name := filepath.Base(f)
	chat := job.ChatID

	extractAndUpload := func() error {
		statusMsg := m.send(st, status.Extracting(job.ID, name), tg.SendOpts{Keyboard: cancelKeyboard(job.ID)})
		defer m.del(statusMsg)
		if err := m.extract(st, f, name); err != nil {
			return err
		}
		ok := m.send(st, statusOK(job.ID, name), tg.SendOpts{})
		m.delLater(ok, 5*time.Second)
		if files := fsutil.ListUploadable(st.Extract); len(files) > 0 {
			total := job.TotalFiles + len(files)
			_ = m.d.Store.UpdateProgress(context.Background(), job.ID, storeTotal(total))
			for _, ef := range files {
				if st.ctx.Err() != nil {
					return st.ctx.Err()
				}
				m.processFile(st, st.Extract, ef, true)
			}
		}
		return nil
	}

	if unzipJob {
		if !st.sess.markExtracted(rel) {
			return true
		}
		if err := extractAndUpload(); err != nil {
			if st.ctx.Err() == nil {
				fail := m.send(st, status.ExtractionFailed(job.ID, name), tg.SendOpts{})
				m.delLater(fail, 5*time.Second)
				st.setFatal(fmt.Sprintf("Failed to extract archive %s: %v", name, err))
			}
			return true
		}
		m.consumeArchive(st, f, rel)
		return true
	}

	// Regular job: ask what to do with the archive.
	id := st.sess.archiveID(rel)
	choice, decided := st.sess.archiveChoice(id)
	if !decided {
		p := st.sess.archivePrompt(id)
		kb := tg.Keyboard{
			{tg.Button{Text: "Archive Only", Data: fmt.Sprintf("archive_only:%s:%s", job.ID, id)},
				tg.Button{Text: "Extract & Upload Both", Data: fmt.Sprintf("archive_ext:%s:%s", job.ID, id)}},
			{tg.Button{Text: "Cancel", Data: "cancel_job:" + job.ID}},
		}
		msg := m.send(st, status.ArchivePrompt(job.ID, name), tg.SendOpts{Keyboard: kb})
		choice, decided = p.wait(st.ctx, promptTimeout)
		if !decided {
			choice = ChoiceArchiveOnly
			st.sess.setArchiveChoice(id, choice)
		}
		m.del(msg)
		_ = chat
	}
	if choice == ChoiceExtract && st.sess.markExtracted(rel) {
		if err := extractAndUpload(); err != nil && st.ctx.Err() == nil {
			slog.Warn("extraction failed; uploading the archive only", "file", name, "err", err)
			fail := m.send(st, status.ExtractionFailed(job.ID, name), tg.SendOpts{})
			m.delLater(fail, 5*time.Second)
		}
	}
	return false
}

func statusOK(jobID, name string) string { return status.ExtractionOK(jobID, name) }

// consumeArchive deletes an extracted archive and every sibling volume.
func (m *Manager) consumeArchive(st *State, f, rel string) {
	_ = os.Remove(f)
	st.markUploaded(rel)
	_ = m.d.Store.MarkUploaded(context.Background(), st.Job().ID, rel)
	if si := archive.SplitArchiveInfo(filepath.Base(f)); si != nil && si.Part == 1 {
		entries, _ := os.ReadDir(filepath.Dir(f))
		for _, e := range entries {
			if !e.IsDir() && si.Pattern.MatchString(e.Name()) {
				sib := filepath.Join(filepath.Dir(f), e.Name())
				_ = os.Remove(sib)
				sr, _ := filepath.Rel(st.Dest, sib)
				st.markUploaded(sr)
			}
		}
	}
}

// extract runs archive.Extract, prompting the user for a password as needed.
func (m *Manager) extract(st *State, path, name string) error {
	job := st.Job()
	for {
		err := archive.Extract(st.ctx, path, st.Extract, st.Args().Password)
		if err == nil || !errors.Is(err, archive.ErrPasswordRequired) {
			return err
		}
		ref := m.send(st, fmt.Sprintf("<b>Password Required</b>: %s is password-protected or the password was incorrect.\n\nReply to this message with the password.", status.Code(name)),
			tg.SendOpts{ForceReply: true, Placeholder: "Enter archive password"})
		if !ref.Valid() {
			return err
		}
		pp := st.sess.newPasswordWait(ref.ID, name)
		var pw string
		select {
		case pw = <-pp.ch:
		case <-time.After(passwordTimeout):
			st.sess.dropPasswordWait(ref.ID)
			m.del(ref)
			m.send(st, fmt.Sprintf("<b>Job #%s aborted</b>: timed out waiting for the password for %s.", status.Esc(job.ID), status.Code(name)), tg.SendOpts{})
			return errNoPassword
		case <-st.ctx.Done():
			st.sess.dropPasswordWait(ref.ID)
			m.del(ref)
			return st.ctx.Err()
		}
		st.sess.dropPasswordWait(ref.ID)
		m.del(ref)

		a := st.Args()
		a.Password = pw
		if b, merr := json.Marshal(a); merr == nil {
			_ = m.d.Store.SetArgs(context.Background(), job.ID, string(b))
		}
		st.mu.Lock()
		st.args = a
		st.mu.Unlock()
	}
}

func (m *Manager) convertVideo(st *State, f, root, key string) (string, string) {
	job := st.Job()
	name := filepath.Base(f)
	out := strings.TrimSuffix(f, filepath.Ext(f)) + ".mkv"
	if _, err := os.Stat(out); err == nil {
		out = strings.TrimSuffix(f, filepath.Ext(f)) + "_converted.mkv"
	}
	msg := m.send(st, status.Converting(job.ID, name, "MKV"), tg.SendOpts{Keyboard: cancelKeyboard(job.ID)})
	st.setConverting(true, name)
	ok := media.ConvertVideoToMKV(st.ctx, f, out)
	st.setConverting(false, "")
	m.del(msg)
	if !ok {
		slog.Error("video conversion failed; keeping original", "file", name)
		return f, key
	}
	_ = os.Remove(f)
	rel, _ := filepath.Rel(root, out)
	if root != st.Dest {
		rel = "extract/" + rel
	}
	return out, rel
}

func (m *Manager) maybeConvertAudio(st *State, f, root, key string) (string, string) {
	job := st.Job()
	name := filepath.Base(f)
	if st.sess.convertedBefore(name) {
		return f, key
	}
	id := st.sess.convID2(name)
	choice := st.sess.audioSticky()
	if choice == "" {
		p := st.sess.convPrompt(id)
		kb := tg.Keyboard{
			{tg.Button{Text: "Convert to MP3", Data: fmt.Sprintf("convert_mp3:%s:%s", job.ID, id)},
				tg.Button{Text: "Original File", Data: fmt.Sprintf("convert_orig:%s:%s", job.ID, id)}},
			{tg.Button{Text: "Cancel", Data: "cancel_job:" + job.ID}},
		}
		msg := m.send(st, status.AudioPrompt(job.ID, name), tg.SendOpts{Keyboard: kb})
		c, ok := p.wait(st.ctx, promptTimeout)
		m.del(msg)
		if !ok {
			c = ChoiceOriginal
			st.sess.setAudioChoice(id, c)
		}
		choice = c
	}
	if choice != ChoiceMP3 {
		return f, key
	}
	st.sess.markConverted(name)
	out := strings.TrimSuffix(f, filepath.Ext(f)) + "_converted.mp3"
	msg := m.send(st, status.Converting(job.ID, name, "MP3"), tg.SendOpts{Keyboard: cancelKeyboard(job.ID)})
	st.setConverting(true, name)
	ok := media.ConvertAudioToMP3(st.ctx, f, out)
	st.setConverting(false, "")
	m.del(msg)
	if !ok {
		fail := m.send(st, status.ConversionFailed(job.ID, name), tg.SendOpts{})
		m.delLater(fail, 5*time.Second)
		return f, key
	}
	_ = os.Remove(f)
	rel, _ := filepath.Rel(root, out)
	if root != st.Dest {
		rel = "extract/" + rel
	}
	return out, rel
}

// mirrorFile uploads one file to all web hosts sequentially, publishing live
// progress in the status card.
func (m *Manager) mirrorFile(st *State, path string) MirrorRecord {
	const tenGB = 10 << 30
	uid := st.Args().UserIDInt()
	info, _ := os.Stat(path)
	hosts := map[string]status.HostState{
		"gofile": {Status: "pending"}, "fileditch": {Status: "pending"}, "pixeldrain": {Status: "pending"},
	}
	if info != nil && info.Size() > tenGB {
		hosts["pixeldrain"] = status.HostState{Status: "skipped"}
	}
	rec := MirrorRecord{File: filepath.Base(path), Links: map[string]string{}}

	type hostFn func(ctx context.Context, cb func(cur, total int64)) (string, error)
	run := func(key string, fn hostFn) {
		if hosts[key].Status == "skipped" || st.ctx.Err() != nil {
			return
		}
		var lastAt time.Time
		var lastB int64
		var speed float64
		cb := func(cur, total int64) {
			now := time.Now()
			if lastAt.IsZero() {
				lastAt, lastB = now, cur
			} else if dt := now.Sub(lastAt).Seconds(); dt >= 1 {
				speed = max(0, float64(cur-lastB)/dt)
				lastAt, lastB = now, cur
			}
			pct := 0.0
			if total > 0 {
				pct = float64(cur) / float64(total) * 100
			}
			hosts[key] = status.HostState{Status: "uploading", Pct: pct, Speed: speed}
			st.setHosts(hosts)
		}
		hosts[key] = status.HostState{Status: "uploading"}
		st.setHosts(hosts)
		link, err := fn(st.ctx, cb)
		if err != nil {
			slog.Warn("mirror upload failed", "host", key, "file", rec.File, "err", err)
			hosts[key] = status.HostState{Status: "failed", Error: err.Error()}
		} else {
			hosts[key] = status.HostState{Status: "done", URL: link}
			rec.Links[key] = link
		}
		st.setHosts(hosts)
	}
	run("gofile", func(ctx context.Context, cb func(int64, int64)) (string, error) {
		return m.d.Hosts.Gofile(ctx, path, uid, cb)
	})
	run("fileditch", func(ctx context.Context, cb func(int64, int64)) (string, error) {
		return m.d.Hosts.Fileditch(ctx, path, false, cb)
	})
	run("pixeldrain", func(ctx context.Context, cb func(int64, int64)) (string, error) {
		return m.d.Hosts.Pixeldrain(ctx, path, uid, cb)
	})
	return rec
}

func store2(sent, skipped int) store.Update {
	return store.Update{SentFiles: &sent, SkippedFiles: &skipped}
}

func storeTotal(total int) store.Update { return store.Update{TotalFiles: &total} }
