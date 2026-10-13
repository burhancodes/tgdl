package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/media"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/tg"
)

// ErrTooLarge is returned for files above the upload limit.
var ErrTooLarge = errors.New("file exceeds the Telegram upload limit")

var (
	partSuffixRE = regexp.MustCompile(`(?i)((?:_part|\.part)\d+\.[^.]+$|\.[^.]+\.\d+$|\.\d+$)`)
	groupKeyRE   = regexp.MustCompile(`(?i)^(.+?)(?:(?:_part|\.part)\d+\.[^.]+$|\.0*\d+$)`)
)

// tgUploader sends files to Telegram with pacing, retries and format fallbacks.
type tgUploader struct {
	tg            tg.Client
	lim           *pacing.TelegramLimiter
	limit         int64
	albumMaxBytes int64

	mu         sync.Mutex
	multiplier float64
}

func newTGUploader(c tg.Client, lim *pacing.TelegramLimiter, limit int64, albumMaxBytes ...int64) *tgUploader {
	ab := int64(64 * 1024 * 1024)
	if len(albumMaxBytes) > 0 && albumMaxBytes[0] > 0 {
		ab = albumMaxBytes[0]
	}
	return &tgUploader{tg: c, lim: lim, limit: limit, albumMaxBytes: ab, multiplier: 1}
}

func (u *tgUploader) bump(delta float64) {
	u.mu.Lock()
	u.multiplier = min(5, max(1, u.multiplier+delta))
	u.mu.Unlock()
}

func (u *tgUploader) mult() float64 { u.mu.Lock(); defer u.mu.Unlock(); return u.multiplier }

func caption(path string) string {
	name := filepath.Base(path)
	suffix := filepath.Ext(name)
	stem := strings.TrimSuffix(name, suffix)
	if m := partSuffixRE.FindString(name); m != "" {
		suffix, stem = m, name[:len(name)-len(m)]
	}
	display := name
	if len([]rune(name)) > 60 {
		remain := max(10, 60-len([]rune(suffix)))
		display = string([]rune(stem)[:min(remain, len([]rune(stem)))]) + suffix
	}
	return "<code>" + status.Esc(display) + "</code>"
}

// UploadResult reports whether the doc-mode fallback was engaged.
type UploadResult struct{ AsDoc bool }

// Upload sends one file. progress may be nil. It returns the (possibly
// updated) as-document preference so callers can stick to it for the batch.
func (u *tgUploader) Upload(ctx context.Context, chatID int64, path string, asDoc bool, progress tg.ProgressFunc) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return asDoc, err
	}
	if st.Size() == 0 {
		return asDoc, errors.New("file is empty (0 bytes)")
	}
	if st.Size() > u.limit {
		return asDoc, fmt.Errorf("%w: %s is %.2fGB", ErrTooLarge, filepath.Base(path), float64(st.Size())/1e9)
	}
	ext := media.Ext(path)
	var converted string
	if media.ConvertibleImage[ext] {
		png := strings.TrimSuffix(path, filepath.Ext(path)) + ".png"
		if media.ConvertImageToPNG(ctx, path, png) {
			converted, path, ext = png, png, ".png"
		}
	}
	defer func() {
		if converted != "" {
			_ = os.Remove(converted)
		}
	}()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := pacing.Sleep(ctx, time.Duration(2<<attempt)*time.Second); err != nil {
				return asDoc, err
			}
		}
		var err error
		asDoc, err = u.uploadOnce(ctx, chatID, path, asDoc, progress)
		if err == nil {
			return asDoc, nil
		}
		if ctx.Err() != nil {
			return asDoc, ctx.Err()
		}
		if errors.Is(err, tg.ErrAmbiguousUpload) {
			slog.Warn("upload failed after body was fully sent; not retrying to avoid duplicates", "file", filepath.Base(path), "err", err)
			return asDoc, err
		}
		lastErr = err
		if _, bad := tg.IsBadRequest(err); bad {
			break // uploadOnce already retried as document
		}
		slog.Warn("upload attempt failed", "file", filepath.Base(path), "attempt", attempt+1, "err", err)
	}
	return asDoc, lastErr
}

func (u *tgUploader) uploadOnce(ctx context.Context, chatID int64, path string, asDoc bool, progress tg.ProgressFunc) (bool, error) {
	ext := media.Ext(path)
	force := asDoc && ext != ".mkv"
	isVideo, isAudio := media.VideoExt[ext], media.AudioExt[ext]
	isImage := media.ImageExt[ext] && !media.ConvertibleImage[ext]
	if ext == ".gif" {
		force = true // sendPhoto would flatten the animation
	}
	if isImage && !force && media.PhotoInvalidForTelegram(ctx, path) {
		slog.Info("image unsuited for photo API; sending as document", "file", filepath.Base(path))
		force = true
	}

	for flood := 0; ; flood++ {
		if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
			return asDoc, err
		}
		up := tg.Upload{Path: path, Caption: caption(path), Progress: progress}
		var cleanup []string
		switch {
		case force || (!isVideo && !isAudio && !isImage):
			up.Kind = tg.KindDocument
			if isVideo {
				up.Thumb = media.VideoThumbnail(ctx, path, 0)
			} else if media.ImageExt[ext] {
				up.Thumb = media.ImageThumbnail(ctx, path)
			} else if ext == ".pdf" {
				up.Thumb = media.PDFThumbnail(ctx, path)
			}
		case isVideo:
			vi := media.ProbeVideo(ctx, path)
			up.Kind, up.Duration, up.Width, up.Height = tg.KindVideo, vi.Duration, vi.Width, vi.Height
			if vi.Decodable {
				up.Thumb = media.VideoThumbnail(ctx, path, vi.Duration)
			} else {
				up.Thumb = media.VideoThumbnail(ctx, path, 0)
			}
		case isAudio:
			ai := media.ProbeAudio(ctx, path)
			title := ai.Title
			if title == "" {
				title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			up.Kind, up.Duration, up.Performer, up.Title = tg.KindAudio, ai.Duration, ai.Artist, title
		default:
			up.Kind = tg.KindPhoto
		}
		if up.Thumb != "" {
			cleanup = append(cleanup, up.Thumb)
		}

		err := u.tg.Upload(ctx, chatID, up)
		u.lim.RecordUploadDone(chatID)
		for _, c := range cleanup {
			_ = os.Remove(c)
		}
		if err == nil {
			if up.Kind == tg.KindVideo && up.Duration >= 120 && up.Thumb != "" {
				u.sendScreenshots(ctx, chatID, path, up.Duration)
			}
			u.bump(-0.05)
			return asDoc || (force && isImage), nil
		}
		if secs, ok := tg.IsFlood(err); ok && flood < 5 {
			slog.Warn("telegram flood wait on upload", "seconds", secs)
			u.lim.NotifyFloodWait(secs, chatID)
			u.bump(0.5)
			waitDur := time.Duration(secs)*time.Second + 2*time.Second
			if secs == 0 {
				waitDur = 0
			}
			if err := pacing.Sleep(ctx, waitDur); err != nil {
				return asDoc, err
			}
			continue
		}
		if msg, ok := tg.IsBadRequest(err); ok && !force {
			slog.Warn("bad request during upload; retrying as document", "file", filepath.Base(path), "err", msg)
			low := strings.ToLower(msg)
			if strings.Contains(low, "photo") || strings.Contains(low, "image") || strings.Contains(low, "media_invalid") || strings.Contains(low, "too big") || strings.Contains(low, "size") {
				asDoc = true
			}
			for _, k := range []string{"PHOTO_SAVE_FILE_INVALID", "PHOTO_INVALID_DIMENSIONS", "MEDIA_INVALID", "IMAGE_PROCESS_FAILED"} {
				if strings.Contains(msg, k) {
					asDoc = true
				}
			}
			force = true
			continue
		}
		return asDoc, err
	}
}

func (u *tgUploader) sendScreenshots(ctx context.Context, chatID int64, path string, duration int) {
	shots := media.Screenshots(ctx, path, duration)
	defer func() {
		for _, s := range shots {
			_ = os.Remove(s)
		}
	}()
	if len(shots) < 2 {
		return
	}
	items := make([]tg.GroupItem, len(shots))
	for i, s := range shots {
		items[i] = tg.GroupItem{Path: s, Photo: true}
	}
	items[0].Caption = "Screenshots for <code>" + status.Esc(filepath.Base(path)) + "</code>"
	if err := u.lim.Acquire(ctx, chatID); err != nil {
		return
	}
	if err := u.tg.UploadGroup(ctx, chatID, items); err != nil {
		slog.Warn("screenshot album failed", "err", err)
	}
	u.lim.RecordUploadDone(chatID)
}

// SplitGroupKey returns the base name when path is one part of a split set.
func SplitGroupKey(name string) (string, bool) {
	m := groupKeyRE.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// PlanAlbumBatches splits a list of file sizes into batches for upload.
// An album batch requires 2 to 10 items whose combined size does NOT exceed maxTotalBytes,
// with every file individually <= maxTotalBytes.
// Any file that cannot form a valid album batch (e.g. size > maxTotalBytes,
// or only 1 file remaining) is returned as a single-item batch (len == 1).
func PlanAlbumBatches(sizes []int64, maxTotalBytes int64) [][]int {
	var batches [][]int
	n := len(sizes)
	i := 0
	for i < n {
		if sizes[i] > maxTotalBytes {
			batches = append(batches, []int{i})
			i++
			continue
		}

		var currentSize int64
		j := i
		for j < n && (j-i) < 10 {
			if sizes[j] > maxTotalBytes {
				break
			}
			if currentSize+sizes[j] > maxTotalBytes {
				break
			}
			currentSize += sizes[j]
			j++
		}

		count := j - i
		if count >= 2 {
			batch := make([]int, count)
			for k := 0; k < count; k++ {
				batch[k] = i + k
			}
			batches = append(batches, batch)
			i = j
		} else {
			batches = append(batches, []int{i})
			i++
		}
	}
	return batches
}

// UploadGroup sends files as albums when the combined size is small (<= albumMaxBytes)
// and each file is individually small, falling back to single-file uploads otherwise.
// onUploaded is called for each file that succeeds.
func (u *tgUploader) UploadGroup(ctx context.Context, chatID int64, files []string, asDoc bool, progress tg.ProgressFunc, onUploaded ...func(path string)) (bool, error) {
	var notifySuccess func(string)
	if len(onUploaded) > 0 && onUploaded[0] != nil {
		notifySuccess = onUploaded[0]
	}

	sort.SliceStable(files, func(i, j int) bool { return fsutil.NaturalPathLess(files[i], files[j]) })

	sizes := make([]int64, len(files))
	for i, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return asDoc, err
		}
		sizes[i] = st.Size()
	}

	plan := PlanAlbumBatches(sizes, u.albumMaxBytes)
	for _, batchIndices := range plan {
		if len(batchIndices) == 1 {
			f := files[batchIndices[0]]
			var err error
			if asDoc, err = u.Upload(ctx, chatID, f, asDoc, progress); err != nil {
				return asDoc, err
			}
			if notifySuccess != nil {
				notifySuccess(f)
			}
			continue
		}

		batchFiles := make([]string, len(batchIndices))
		items := make([]tg.GroupItem, len(batchIndices))
		for k, idx := range batchIndices {
			f := files[idx]
			batchFiles[k] = f
			items[k] = tg.GroupItem{Path: f, Caption: caption(f), Video: media.VideoExt[media.Ext(f)]}
		}

		if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
			return asDoc, err
		}
		err := u.tg.UploadGroup(ctx, chatID, items)
		u.lim.RecordUploadDone(chatID)
		if secs, ok := tg.IsFlood(err); ok {
			u.lim.NotifyFloodWait(secs, chatID)
			waitDur := time.Duration(secs)*time.Second + 2*time.Second
			if secs == 0 {
				waitDur = 0
			}
			if e := pacing.Sleep(ctx, waitDur); e != nil {
				return asDoc, e
			}
			if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
				return asDoc, err
			}
			err = u.tg.UploadGroup(ctx, chatID, items)
			u.lim.RecordUploadDone(chatID)
		}
		if err != nil {
			if errors.Is(err, tg.ErrAmbiguousUpload) {
				slog.Warn("album upload failed after body was fully sent; not falling back to single uploads", "err", err)
				return asDoc, err
			}
			slog.Warn("album upload failed; falling back to single uploads", "err", err)
			for _, f := range batchFiles {
				var e error
				if asDoc, e = u.Upload(ctx, chatID, f, asDoc, progress); e != nil {
					return asDoc, e
				}
				if notifySuccess != nil {
					notifySuccess(f)
				}
			}
		} else {
			if notifySuccess != nil {
				for _, f := range batchFiles {
					notifySuccess(f)
				}
			}
		}
	}
	return asDoc, nil
}
