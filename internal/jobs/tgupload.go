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

var ErrTooLarge = errors.New("file exceeds the Telegram upload limit")

var (
	partSuffixRE = regexp.MustCompile(`(?i)((?:_part|\.part)\d+\.[^.]+$|\.[^.]+\.\d+$|\.\d+$)`)
	groupKeyRE   = regexp.MustCompile(`(?i)^(.+?)(?:(?:_part|\.part)\d+\.[^.]+$|\.0*\d+$)`)
)

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

func (u *tgUploader) setAlbumMaxBytes(b int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if b > 0 {
		u.albumMaxBytes = b
	}
}

func (u *tgUploader) getAlbumMaxBytes() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.albumMaxBytes
}

func buildCaption(name string) (string, bool) {
	runes := []rune(name)
	if len(runes) <= 1024 {
		return "<code>" + status.Esc(name) + "</code>", false
	}
	return "<code>" + status.Esc(string(runes[:1020])) + "…" + "</code>", true
}

func caption(path string) string {
	c, _ := buildCaption(filepath.Base(path))
	return c
}

func (u *tgUploader) sendOverflowReplies(ctx context.Context, chatID int64, replyToID int, name string) error {
	runes := []rune(name)
	for i := 0; i < len(runes); i += 4000 {
		end := i + 4000
		if end > len(runes) {
			end = len(runes)
		}
		part := string(runes[i:end])
		html := "<code>" + status.Esc(part) + "</code>"
		if err := u.lim.Acquire(ctx, chatID); err != nil {
			return err
		}
		_, err := u.tg.Send(ctx, chatID, html, tg.SendOpts{ReplyTo: replyToID})
		u.lim.RecordCallDone(chatID)
		if err != nil {
			return err
		}
	}
	return nil
}

type UploadResult struct{ AsDoc bool }

func (u *tgUploader) Upload(ctx context.Context, chatID int64, path string, asDoc bool, progress tg.ProgressFunc, optFileName ...fsutil.FileName) (bool, error) {
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

	var fn fsutil.FileName
	if len(optFileName) > 0 && optFileName[0].Display != "" {
		fn = optFileName[0]
	} else {
		fn = fsutil.NewFileName(filepath.Base(path))
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := pacing.Sleep(ctx, time.Duration(2<<attempt)*time.Second); err != nil {
				return asDoc, err
			}
		}
		var err error
		asDoc, err = u.uploadOnce(ctx, chatID, path, asDoc, progress, fn)
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
			break
		}
		slog.Warn("upload attempt failed", "file", filepath.Base(path), "attempt", attempt+1, "err", err)
	}
	return asDoc, lastErr
}

func (u *tgUploader) uploadOnce(ctx context.Context, chatID int64, path string, asDoc bool, progress tg.ProgressFunc, fn fsutil.FileName) (bool, error) {
	ext := media.Ext(path)
	force := asDoc && ext != ".mkv"
	isVideo, isAudio := media.VideoExt[ext], media.AudioExt[ext]
	isImage := media.ImageExt[ext] && !media.ConvertibleImage[ext]
	if ext == ".gif" {
		force = true
	}
	if isImage && !force && media.PhotoInvalidForTelegram(ctx, path) {
		slog.Info("image unsuited for photo API; sending as document", "file", filepath.Base(path))
		force = true
	}

	capText, overflow := buildCaption(fn.Display)

	for flood := 0; ; flood++ {
		if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
			return asDoc, err
		}
		up := tg.Upload{Path: path, FileName: fn.Display, Caption: capText, Progress: progress}
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
				title = strings.TrimSuffix(fn.Display, filepath.Ext(fn.Display))
			}
			up.Kind, up.Duration, up.Performer, up.Title = tg.KindAudio, ai.Duration, ai.Artist, title
		default:
			up.Kind = tg.KindPhoto
		}
		if up.Thumb != "" {
			cleanup = append(cleanup, up.Thumb)
		}

		ref, err := u.tg.Upload(ctx, chatID, up)
		u.lim.RecordUploadDone(chatID)
		for _, c := range cleanup {
			_ = os.Remove(c)
		}
		if err == nil {
			if overflow && ref.Valid() {
				_ = u.sendOverflowReplies(ctx, chatID, ref.ID, fn.Display)
			}
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
	if _, err := u.tg.UploadGroup(ctx, chatID, items); err != nil {
		slog.Warn("screenshot album failed", "err", err)
	}
	u.lim.RecordUploadDone(chatID)
}

func SplitGroupKey(name string) (string, bool) {
	m := groupKeyRE.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

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

func (u *tgUploader) UploadGroup(ctx context.Context, chatID int64, files []string, asDoc bool, progress tg.ProgressFunc, onUploaded ...func(path string)) (bool, error) {
	return u.UploadGroupWithNames(ctx, chatID, files, nil, asDoc, progress, onUploaded...)
}

func (u *tgUploader) UploadGroupWithNames(ctx context.Context, chatID int64, files []string, names map[string]fsutil.FileName, asDoc bool, progress tg.ProgressFunc, onUploaded ...func(path string)) (bool, error) {
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

	plan := PlanAlbumBatches(sizes, u.getAlbumMaxBytes())
	for _, batchIndices := range plan {
		if len(batchIndices) == 1 {
			f := files[batchIndices[0]]
			var fn fsutil.FileName
			if names != nil {
				if n, ok := names[f]; ok && n.Display != "" {
					fn = n
				}
			}
			var err error
			if asDoc, err = u.Upload(ctx, chatID, f, asDoc, progress, fn); err != nil {
				return asDoc, err
			}
			if notifySuccess != nil {
				notifySuccess(f)
			}
			continue
		}

		batchFiles := make([]string, len(batchIndices))
		items := make([]tg.GroupItem, len(batchIndices))
		overflows := make([]bool, len(batchIndices))
		itemNames := make([]fsutil.FileName, len(batchIndices))
		for k, idx := range batchIndices {
			f := files[idx]
			batchFiles[k] = f
			fn := fsutil.NewFileName(filepath.Base(f))
			if names != nil {
				if n, ok := names[f]; ok && n.Display != "" {
					fn = n
				}
			}
			itemNames[k] = fn
			capText, ovf := buildCaption(fn.Display)
			overflows[k] = ovf
			items[k] = tg.GroupItem{
				Path:     f,
				FileName: fn.Display,
				Caption:  capText,
				Video:    media.VideoExt[media.Ext(f)],
			}
		}

		if err := u.lim.AcquireUpload(ctx, chatID); err != nil {
			return asDoc, err
		}
		refs, err := u.tg.UploadGroup(ctx, chatID, items)
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
			refs, err = u.tg.UploadGroup(ctx, chatID, items)
			u.lim.RecordUploadDone(chatID)
		}
		if err != nil {
			if errors.Is(err, tg.ErrAmbiguousUpload) {
				slog.Warn("album upload failed after body was fully sent; not falling back to single uploads", "err", err)
				return asDoc, err
			}
			slog.Warn("album upload failed; falling back to single uploads", "err", err)
			for k, f := range batchFiles {
				var e error
				if asDoc, e = u.Upload(ctx, chatID, f, asDoc, progress, itemNames[k]); e != nil {
					return asDoc, e
				}
				if notifySuccess != nil {
					notifySuccess(f)
				}
			}
		} else {
			for k, ovf := range overflows {
				if ovf {
					targetID := 0
					if k < len(refs) && refs[k].Valid() {
						targetID = refs[k].ID
					} else if len(refs) > 0 && refs[0].Valid() {
						targetID = refs[0].ID
					}
					if targetID != 0 {
						_ = u.sendOverflowReplies(ctx, chatID, targetID, itemNames[k].Display)
					}
				}
			}
			if notifySuccess != nil {
				for _, f := range batchFiles {
					notifySuccess(f)
				}
			}
		}
	}
	return asDoc, nil
}
