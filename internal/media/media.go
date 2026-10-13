// Package media wraps ffmpeg/ffprobe for probing, thumbnails, conversion and
// splitting. Every operation shells out with a context so it is cancellable.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/burhanverse/tgdl/internal/archive"
)

var (
	VideoExt = set(".mp4", ".mov", ".webm", ".mkv", ".avi", ".flv", ".wmv", ".3gp", ".mpeg", ".mpg", ".m4v", ".ts", ".tts", ".f4v")
	// ConversionExt are video containers that Telegram players handle poorly;
	// they are remuxed to MKV before upload. (.mov, .m4v, .webm play natively in Telegram).
	ConversionExt      = set(".ts", ".flv", ".avi", ".wmv", ".mpeg", ".mpg", ".vob", ".3gp", ".f4v", ".tts", ".asf")
	AudioExt           = set(".mp3", ".flac", ".m4a", ".aac", ".opus", ".ogg", ".wav", ".wma", ".alac", ".aiff")
	AudioConversionExt = set(".wav", ".flac", ".ogg", ".opus", ".aiff", ".aac")
	ImageExt           = set(".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".tiff", ".heic", ".heif", ".ico")
	ConvertibleImage   = set(".webp", ".bmp", ".tiff", ".heic", ".heif", ".ico")
)

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Ext returns the lower-cased extension of path.
func Ext(path string) string { return strings.ToLower(filepath.Ext(path)) }

// Available reports whether ffmpeg and ffprobe are installed.
func Available() bool {
	_, e1 := exec.LookPath("ffmpeg")
	_, e2 := exec.LookPath("ffprobe")
	return e1 == nil && e2 == nil
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	bin, wArgs := archive.WrapHeavy(name, args...)
	cmd := exec.CommandContext(ctx, bin, wArgs...)
	tw := archive.NewTailWriter(64 * 1024)
	cmd.Stdout, cmd.Stderr = tw, tw
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return tw.Bytes(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(tailStr(tw.String(), 400)))
	}
	return tw.Bytes(), nil
}

func tailStr(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ---- probing -------------------------------------------------------------

// VideoInfo describes a probed video.
type VideoInfo struct {
	Duration  int
	Width     int
	Height    int
	Decodable bool
}

type ffprobeOut struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		PixFmt    string `json:"pix_fmt"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

func probe(ctx context.Context, path string) (*ffprobeOut, error) {
	out, err := run(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	var p ffprobeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func parseDur(s string) int {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) {
		return 0
	}
	return int(f)
}

// ProbeVideo returns duration/size. On failure Decodable is false and the
// other fields are zero, so callers can still upload as a plain document.
func ProbeVideo(ctx context.Context, path string) VideoInfo {
	p, err := probe(ctx, path)
	if err != nil {
		slog.Debug("video probe failed", "file", filepath.Base(path), "err", err)
		return VideoInfo{}
	}
	vi := VideoInfo{Duration: parseDur(p.Format.Duration)}
	for _, s := range p.Streams {
		if s.CodecType == "video" && s.Width > 0 {
			vi.Width, vi.Height, vi.Decodable = s.Width, s.Height, true
			break
		}
	}
	return vi
}

// AudioInfo holds audio tags.
type AudioInfo struct {
	Duration int
	Artist   string
	Title    string
}

func ProbeAudio(ctx context.Context, path string) AudioInfo {
	p, err := probe(ctx, path)
	if err != nil {
		return AudioInfo{}
	}
	tag := func(keys ...string) string {
		for _, k := range keys {
			for tk, v := range p.Format.Tags {
				if strings.EqualFold(tk, k) && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
		return ""
	}
	return AudioInfo{Duration: parseDur(p.Format.Duration), Artist: tag("artist", "album_artist"), Title: tag("title")}
}

// ---- images --------------------------------------------------------------

// TelegramPhotoMaxSize is the strict maximum file size Telegram allows for photos (10 MB).
const TelegramPhotoMaxSize int64 = 10 * 1024 * 1024

// PhotoInvalidForTelegram reports images that Telegram's photo API rejects
// (size > 10MB, bad dimensions/aspect ratio, palette/CMYK colour) so they go as documents.
func PhotoInvalidForTelegram(ctx context.Context, path string) bool {
	if st, err := os.Stat(path); err == nil && st.Size() > TelegramPhotoMaxSize {
		return true
	}
	p, err := probe(ctx, path)
	if err != nil || len(p.Streams) == 0 {
		return true
	}
	s := p.Streams[0]
	if s.Width <= 0 || s.Height <= 0 {
		return true
	}
	switch s.PixFmt {
	case "pal8", "monob", "monow":
		return true
	}
	if strings.Contains(s.PixFmt, "cmyk") {
		return true
	}
	w, h := s.Width, s.Height
	if w+h > 10000 || max(w, h) > 9900 {
		return true
	}
	r := float64(w) / float64(h)
	return r > 15 || r < 1.0/15
}

// ConvertImageToPNG converts via ffmpeg. It returns true on success.
func ConvertImageToPNG(ctx context.Context, in, out string) bool {
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", in, "-frames:v", "1", out); err != nil {
		slog.Warn("image->png failed", "file", filepath.Base(in), "err", err)
		_ = os.Remove(out)
		return false
	}
	st, err := os.Stat(out)
	return err == nil && st.Size() > 0
}

var TempDir = ""

// SetTempDir sets the temporary directory used for thumbnails and screenshots.
func SetTempDir(dir string) { TempDir = dir }

func tempJPG(stem string) (string, error) {
	dir := TempDir
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.CreateTemp(dir, stem+"_*.jpg")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Chmod(name, 0o644)
	return name, nil
}

// ImageThumbnail makes a 320px JPEG thumbnail; "" on failure.
func ImageThumbnail(ctx context.Context, path string) string {
	out, err := tempJPG("thumb")
	if err != nil {
		return ""
	}
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", path, "-frames:v", "1",
		"-vf", "scale='min(320,iw)':'min(320,ih)':force_original_aspect_ratio=decrease", "-q:v", "4", out); err != nil {
		_ = os.Remove(out)
		return ""
	}
	return out
}

// VideoThumbnail grabs a frame near the start; "" on failure.
func VideoThumbnail(ctx context.Context, path string, duration int) string {
	out, err := tempJPG("vthumb")
	if err != nil {
		return ""
	}
	ts := "1"
	if duration > 0 && duration < 3 {
		ts = "0"
	}
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-ss", ts, "-i", path, "-frames:v", "1",
		"-vf", "scale='min(320,iw)':'min(320,ih)':force_original_aspect_ratio=decrease", "-q:v", "4", out); err != nil {
		_ = os.Remove(out)
		return ""
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		_ = os.Remove(out)
		return ""
	}
	return out
}

// Screenshots grabs up to nine frames at random timestamps.
func Screenshots(ctx context.Context, path string, duration int) []string {
	if duration <= 0 {
		return nil
	}
	ts := make([]float64, 9)
	for i := range ts {
		ts[i] = (0.05 + rand.Float64()*0.90) * float64(duration)
	}
	sort.Float64s(ts)
	var shots []string
	fails := 0
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, t := range ts {
		if ctx.Err() != nil {
			break
		}
		out, err := tempJPG(stem + "_shot")
		if err != nil {
			continue
		}
		_, err = run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-ss", strconv.FormatFloat(t, 'f', 2, 64), "-i", path,
			"-frames:v", "1", "-q:v", "3", out)
		if st, serr := os.Stat(out); err != nil || serr != nil || st.Size() == 0 {
			_ = os.Remove(out)
			if fails++; fails >= 3 {
				slog.Warn("aborting screenshots after repeated failures", "file", filepath.Base(path))
				break
			}
			continue
		}
		fails = 0
		shots = append(shots, out)
	}
	return shots
}

// ---- conversion ----------------------------------------------------------

// ConvertVideoToMKV remuxes (stream copy) into Matroska.
func ConvertVideoToMKV(ctx context.Context, in, out string) bool {
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", in, "-map", "0", "-c", "copy", "-f", "matroska", out); err != nil {
		slog.Warn("remux to mkv failed", "file", filepath.Base(in), "err", err)
		_ = os.Remove(out)
		return false
	}
	st, err := os.Stat(out)
	if err == nil && st.Size() > 0 {
		_ = os.Chmod(out, 0o644)
		return true
	}
	return false
}

// ConvertAudioToMP3 transcodes to 320k MP3, preserving tags.
func ConvertAudioToMP3(ctx context.Context, in, out string) bool {
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", in, "-vn", "-map_metadata", "0",
		"-codec:a", "libmp3lame", "-b:a", "320k", out); err != nil {
		slog.Warn("audio->mp3 failed", "file", filepath.Base(in), "err", err)
		_ = os.Remove(out)
		return false
	}
	st, err := os.Stat(out)
	if err == nil && st.Size() > 0 {
		_ = os.Chmod(out, 0o644)
		return true
	}
	return false
}

// ---- splitting -----------------------------------------------------------

// SplitBinary splits into "<name>.001, .002 ..." parts of ~98% of maxBytes.
// On any failure all created parts are removed and nil is returned.
func SplitBinary(path string, maxBytes int64) ([]string, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	chunk := int64(float64(maxBytes) * 0.98)
	var parts []string
	cleanup := func() {
		for _, p := range parts {
			_ = os.Remove(p)
		}
	}
	for n := 1; ; n++ {
		part := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.%03d", filepath.Base(path), n))
		out, err := os.Create(part)
		if err != nil {
			cleanup()
			return nil, err
		}
		_ = os.Chmod(part, 0o644)
		written, cerr := io.CopyN(out, in, chunk)
		if err := out.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr != nil && !errors.Is(cerr, io.EOF) {
			_ = os.Remove(part)
			cleanup()
			return nil, cerr
		}
		if written == 0 {
			_ = os.Remove(part)
			break
		}
		parts = append(parts, part)
		if errors.Is(cerr, io.EOF) {
			break
		}
	}
	return parts, nil
}

// SplitVideo cuts a video into playable segments below maxBytes using ffmpeg's
// segment muxer with stream copy. Segment length is derived from the average
// bitrate with a safety margin; the caller falls back to SplitBinary if any
// part is still oversized.
func SplitVideo(ctx context.Context, path string, maxBytes int64) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	vi := ProbeVideo(ctx, path)
	if vi.Duration <= 0 {
		return nil, errors.New("unknown video duration")
	}
	seg := float64(vi.Duration) * float64(maxBytes) * 0.90 / float64(st.Size())
	if seg < 5 {
		seg = 5
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	pattern := stem + "_part%03d" + ext
	if _, err := run(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", path, "-map", "0", "-c", "copy",
		"-f", "segment", "-segment_time", strconv.FormatFloat(seg, 'f', 1, 64), "-reset_timestamps", "1", pattern); err != nil {
		cleanGlob(stem + "_part*" + ext)
		return nil, err
	}
	parts, _ := filepath.Glob(stem + "_part*" + ext)
	sort.Strings(parts)
	for _, p := range parts {
		if s, err := os.Stat(p); err != nil || s.Size() > maxBytes || s.Size() == 0 {
			cleanGlob(stem + "_part*" + ext)
			return nil, fmt.Errorf("segment %s exceeds limit", filepath.Base(p))
		}
	}
	if len(parts) < 2 {
		cleanGlob(stem + "_part*" + ext)
		return nil, errors.New("video was not split into multiple parts")
	}
	return parts, nil
}

func cleanGlob(pattern string) {
	m, _ := filepath.Glob(pattern)
	for _, f := range m {
		_ = os.Remove(f)
	}
}

// HandleLarge splits oversized files. It returns the files to upload; the
// original is removed whenever it was split or dropped. Files within the limit
// are returned unchanged.
func HandleLarge(ctx context.Context, path string, split bool, limit int64) []string {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if st.Size() <= limit {
		return []string{path}
	}
	slog.Info("file exceeds upload limit", "file", filepath.Base(path), "bytes", st.Size())
	if !split {
		_ = os.Remove(path)
		return nil
	}
	var parts []string
	if VideoExt[Ext(path)] {
		if p, err := SplitVideo(ctx, path, limit); err == nil {
			parts = p
		} else {
			slog.Warn("video split failed; using binary split", "err", err)
		}
	}
	if parts == nil {
		p, err := SplitBinary(path, limit)
		if err != nil {
			slog.Error("binary split failed", "file", filepath.Base(path), "err", err)
		}
		parts = p
	}
	_ = os.Remove(path)
	return parts
}
