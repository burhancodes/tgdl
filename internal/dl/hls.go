package dl

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/burhanverse/tgdl/internal/netguard"
)

// IsM3U8URL reports whether the URL is an HLS playlist, by extension or by
// sniffing the first bytes of the response.
func (d *Direct) IsM3U8URL(ctx context.Context, raw string) bool {
	if raw == "" {
		return false
	}
	if urlExt(raw) == ".m3u8" {
		return true
	}
	if !d.cfg.AllowPrivateURLs && netguard.CheckURL(ctx, raw) != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := d.newRequest(ctx, raw)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-511")
	resp, err := d.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "mpegurl") {
		return true
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return bytes.Contains(head, []byte("#EXTM3U"))
}

type variant struct {
	Bandwidth int
	URL       string
}

var bwRE = regexp.MustCompile(`BANDWIDTH=(\d+)`)

func parseMaster(text string, base *url.URL) []variant {
	var out []variant
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			continue
		}
		bw := 0
		if m := bwRE.FindStringSubmatch(line); m != nil {
			bw, _ = strconv.Atoi(m[1])
		}
		for i++; i < len(lines); i++ {
			u := strings.TrimSpace(lines[i])
			if u != "" && !strings.HasPrefix(u, "#") {
				if ref, err := base.Parse(u); err == nil {
					out = append(out, variant{bw, ref.String()})
				}
				break
			}
		}
	}
	return out
}

type segment struct {
	URL      string
	Duration float64
	Key      *hlsKey
	Seq      uint64
}

type hlsKey struct {
	URL string
	IV  []byte // nil => derive from media sequence
}

var attrRE = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)

func attrs(line string) map[string]string {
	m := map[string]string{}
	for _, a := range attrRE.FindAllStringSubmatch(line, -1) {
		m[a[1]] = strings.Trim(a[2], `"`)
	}
	return m
}

type mediaPlaylist struct {
	Segments []segment
	InitURL  string
	Duration float64
	VOD      bool
}

func parseMedia(text string, base *url.URL) (*mediaPlaylist, error) {
	mp := &mediaPlaylist{VOD: strings.Contains(text, "#EXT-X-ENDLIST")}
	var seq uint64
	var key *hlsKey
	var dur float64
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseUint(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			a := attrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			switch a["METHOD"] {
			case "NONE":
				key = nil
			case "AES-128":
				ref, err := base.Parse(a["URI"])
				if err != nil {
					return nil, err
				}
				k := &hlsKey{URL: ref.String()}
				if iv := strings.TrimPrefix(strings.ToLower(a["IV"]), "0x"); iv != "" {
					b, err := hex.DecodeString(fmt.Sprintf("%032s", iv))
					if err != nil {
						return nil, fmt.Errorf("bad HLS IV: %w", err)
					}
					k.IV = b
				}
				key = k
			default:
				return nil, fmt.Errorf("unsupported HLS encryption method %q", a["METHOD"])
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			a := attrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if ref, err := base.Parse(a["URI"]); err == nil {
				mp.InitURL = ref.String()
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(v, ","); i >= 0 {
				v = v[:i]
			}
			dur, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case line != "" && !strings.HasPrefix(line, "#"):
			ref, err := base.Parse(line)
			if err != nil {
				return nil, err
			}
			mp.Segments = append(mp.Segments, segment{URL: ref.String(), Duration: dur, Key: key, Seq: seq})
			mp.Duration += dur
			seq++
			dur = 0
		}
	}
	if len(mp.Segments) == 0 {
		return nil, errors.New("playlist contains no segments")
	}
	return mp, nil
}

func (d *Direct) getText(ctx context.Context, u string) (string, string, error) {
	if !d.cfg.AllowPrivateURLs {
		if err := netguard.CheckURL(ctx, u); err != nil {
			return "", "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := d.newRequest(ctx, u)
	if err != nil {
		return "", "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d fetching playlist", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(b), resp.Request.URL.String(), err
}

func (d *Direct) getBytes(ctx context.Context, u string) ([]byte, error) {
	req, err := d.newRequest(ctx, u)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// downloadHLS fetches a VOD playlist's segments through the SSRF-guarded
// client (so segment URLs are validated too), decrypts AES-128 segments, and
// remuxes to MP4 (falling back to MKV) with ffmpeg stream copy.
func (d *Direct) downloadHLS(ctx context.Context, playlistURL, dest string) (string, error) {
	text, finalURL, err := d.getText(ctx, playlistURL)
	if err != nil {
		return "", err
	}
	base, _ := url.Parse(finalURL)
	if strings.Contains(text, "#EXT-X-STREAM-INF") {
		vs := parseMaster(text, base)
		if len(vs) == 0 {
			return "", errors.New("master playlist found, but failed to parse variant streams")
		}
		sort.SliceStable(vs, func(i, j int) bool { return vs[i].Bandwidth < vs[j].Bandwidth })
		best := vs[len(vs)-1]
		slog.Info("selected HLS variant", "bandwidth", best.Bandwidth, "url", best.URL)
		text, finalURL, err = d.getText(ctx, best.URL)
		if err != nil {
			return "", err
		}
		base, _ = url.Parse(finalURL)
	}
	mp, err := parseMedia(text, base)
	if err != nil {
		return "", err
	}
	if !mp.VOD {
		return "", errors.New("live HLS streams are not supported: only VOD playlists can be downloaded")
	}

	name := filepath.Base(dest)
	tsPath := dest + ".ts.part"
	defer os.Remove(tsPath)
	out, err := os.Create(tsPath)
	if err != nil {
		return "", err
	}
	closeOut := func() error { return out.Close() }

	if mp.InitURL != "" {
		b, err := d.getBytes(ctx, mp.InitURL)
		if err != nil {
			_ = closeOut()
			return "", fmt.Errorf("fetch init segment: %w", err)
		}
		_, _ = out.Write(b)
	}

	keys := map[string][]byte{}
	var elapsed float64
	for _, seg := range mp.Segments {
		if err := ctx.Err(); err != nil {
			_ = closeOut()
			return "", err
		}
		data, err := d.fetchSegment(ctx, seg, keys)
		if err != nil {
			_ = closeOut()
			return "", fmt.Errorf("segment %d: %w", seg.Seq, err)
		}
		if _, err := out.Write(data); err != nil {
			_ = closeOut()
			return "", err
		}
		elapsed += seg.Duration
		d.processed = int64(elapsed)
		d.total = int64(mp.Duration)
		d.report(name, playlistURL)
	}
	if err := closeOut(); err != nil {
		return "", err
	}

	res, err := remux(ctx, tsPath, dest, "mp4")
	if err != nil {
		slog.Warn("MP4 remux failed, falling back to MKV", "err", err)
		res, err = remux(ctx, tsPath, strings.TrimSuffix(dest, filepath.Ext(dest))+".mkv", "matroska")
	}
	if err != nil {
		return "", fmt.Errorf("HLS remux failed: %w", err)
	}
	return res, nil
}

func (d *Direct) fetchSegment(ctx context.Context, seg segment, keys map[string][]byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if !d.cfg.AllowPrivateURLs {
			if err := netguard.CheckURL(ctx, seg.URL); err != nil {
				return nil, err
			}
		}
		data, err := d.getBytes(ctx, seg.URL)
		if err != nil {
			lastErr = err
			continue
		}
		if seg.Key == nil {
			return data, nil
		}
		k, ok := keys[seg.Key.URL]
		if !ok {
			if k, err = d.getBytes(ctx, seg.Key.URL); err != nil {
				return nil, fmt.Errorf("fetch key: %w", err)
			}
			keys[seg.Key.URL] = k
		}
		iv := seg.Key.IV
		if iv == nil {
			iv = make([]byte, 16)
			binary.BigEndian.PutUint64(iv[8:], seg.Seq)
		}
		return decryptAES128(data, k, iv)
	}
	return nil, lastErr
}

func decryptAES128(data, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("encrypted segment is not block aligned")
	}
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, data)
	if n := len(data); n > 0 {
		pad := int(data[n-1])
		if pad > 0 && pad <= aes.BlockSize && pad <= n {
			data = data[:n-pad]
		}
	}
	return data, nil
}

func remux(ctx context.Context, in, dest, format string) (string, error) {
	part := dest + ".part"
	defer os.Remove(part)
	args := []string{"-y", "-loglevel", "error", "-i", in, "-map", "0:v?", "-map", "0:a?", "-c", "copy"}
	if format == "mp4" {
		args = append(args, "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart")
	}
	args = append(args, "-f", format, part)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	return dest, nil
}

var _ = http.StatusOK
