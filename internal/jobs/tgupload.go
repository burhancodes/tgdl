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
	tg    tg.Client
	lim   *pacing.TelegramLimiter
	limit int64

	mu         sync.Mutex
	multiplier float64
}

func newTGUploader(c tg.Client, lim *pacing.TelegramLimiter, limit int64) *tgUploader {
	return &tgUploader{tg: c, lim: lim, limit: limit, multiplier: 1}
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
			}
		case isVideo:
			vi := media.ProbeVideo(ctx, path)
			up.Kind, up.Duration, up.Width, up.Height = tg.KindVideo, vi.Duration, vi.Width, vi.Height
			if vi.Decodable {
				up.Thumb = media.VideoThumbnail(ctx, path, vi.Duration)
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
		for _, c := range cleanup {
			_ = os.Remove(c)
		}
		if err == nil {
			if up.Kind == tg.KindVideo && up.Duration >= 120 && up.Thumb != "" {
				u.sendScreenshots(ctx, chatID, path, up.Duration)
			}
			u.bump(-0.05)
			return asDoc || force && isImage, nil
		}
		if secs, ok := tg.IsFlood(err); ok && flood < 5 {
			slog.Warn("telegram flood wait on upload", "seconds", secs)
			u.lim.NotifyFloodWait(secs, chatID)
			u.bump(0.5)
			if err := pacing.Sleep(ctx, time.Duration(secs)*time.Second*2); err != nil {
				return asDoc, err
			}
			continue
		}
		if msg, ok := tg.IsBadRequest(err); ok && !force {
			slog.Warn("bad request during upload; retrying as document", "file", filepath.Base(path), "err", msg)
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
}

// SplitGroupKey returns the base name when path is one part of a split set.
func SplitGroupKey(name string) (string, bool) {
	m := groupKeyRE.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// UploadGroup sends split parts as albums of up to 10, falling back to
// single-file uploads for a batch that fails.
func (u *tgUploader) UploadGroup(ctx context.Context, chatID int64, files []string, asDoc bool, progress tg.ProgressFunc) (bool, error) {
	sort.SliceStable(files, func(i, j int) bool { return fsutil.NaturalPathLess(files[i], files[j]) })
	for i := 0; i < len(files); i += 10 {
		batch := files[i:min(i+10, len(files))]
		if len(batch) == 1 {
			var err error
			if asDoc, err = u.Upload(ctx, chatID, batch[0], asDoc, progress); err != nil {
				return asDoc, err
			}
			continue
		}
		items := make([]tg.GroupItem, len(batch))
		for k, f := range batch {
			items[k] = tg.GroupItem{Path: f, Caption: caption(f), Video: media.VideoExt[media.Ext(f)]}
		}
		if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
			return asDoc, err
		}
		err := u.tg.UploadGroup(ctx, chatID, items)
		if secs, ok := tg.IsFlood(err); ok {
			u.lim.NotifyFloodWait(secs, chatID)
			if e := pacing.Sleep(ctx, time.Duration(secs+2)*time.Second); e != nil {
				return asDoc, e
			}
			err = u.tg.UploadGroup(ctx, chatID, items)
		}
		if err != nil {
			slog.Warn("album upload failed; falling back to single uploads", "err", err)
			for _, f := range batch {
				var e error
				if asDoc, e = u.Upload(ctx, chatID, f, asDoc, progress); e != nil {
					return asDoc, e
				}
			}
		}
	}
	return asDoc, nil
}
