package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/burhanverse/tgdl/internal/fsutil"
)

var archiveExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.zip .7z .rar .tar .gz .bz2 .xz .tgz .tbz2 .txz .z .lz .lzma .lzo .zst .cab .iso .ar .cpio .rpm .deb`) {
		archiveExts[e] = true
	}
}

// IsArchiveExt reports whether path has a recognised archive extension.
func IsArchiveExt(path string) bool { return archiveExts[strings.ToLower(filepath.Ext(path))] }

// ErrPasswordRequired signals a missing or incorrect archive password.
var ErrPasswordRequired = errors.New("archive password required or incorrect")

var passwordHints = []string{"password", "incorrect", "encrypted", "bad password", "cannot decrypt", "crc failed",
	"checksum error", "wrong password", "corrupt input data", "password is required", "header encrypted"}
var missingVolumeHints = []string{"cannot find volume", "volume missing", "volume not found", "unexpected end of archive",
	"no files to extract", "cannot open volume"}

func looksLikePassword(s string) bool {
	low := strings.ToLower(s)
	for _, h := range missingVolumeHints {
		if strings.Contains(low, h) {
			return false
		}
	}
	for _, h := range passwordHints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

func which(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func runTool(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return buf.String(), ee.ExitCode(), nil
	}
	return buf.String(), 0, err
}

// Extract extracts archivePath into dest. It returns ErrPasswordRequired when
// the archive is encrypted and the password is missing or wrong. The
// extraction order mirrors robustness: 7z (handles almost everything) →
// unrar → unzip → pure-Go zip/tar fallbacks.
func Extract(ctx context.Context, archivePath, dest, password string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(archivePath); err != nil {
		return err
	}
	dir := filepath.Dir(archivePath)
	if ren := NormalizeSplitNames(dir); len(ren) > 0 {
		if np, ok := ren[archivePath]; ok {
			archivePath = np
		}
	}
	if si := SplitArchiveInfo(filepath.Base(archivePath)); si != nil && si.Part > 1 {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if s := SplitArchiveInfo(e.Name()); s != nil && s.Prefix == si.Prefix && s.Part == 1 {
					archivePath = filepath.Join(dir, e.Name())
					slog.Info("redirecting extraction to first volume", "file", e.Name())
					break
				}
			}
		}
	}
	base := strings.ToLower(filepath.Base(archivePath))
	ext := strings.ToLower(filepath.Ext(archivePath))
	slog.Info("extracting archive", "file", filepath.Base(archivePath), "password", password != "")

	pw := "-p-"
	if password != "" {
		pw = "-p" + password
	}
	sawPassword := false

	if sz := which("7z", "7zz", "7za"); sz != "" {
		out, code, err := runTool(ctx, sz, "x", "-y", "-aoa", "-o"+dest, pw, archivePath)
		if err == nil && code == 0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("7z extraction failed", "code", code, "output", tailStr(out, 400))
		if looksLikePassword(out) || code == 2 && strings.Contains(strings.ToLower(out), "wrong password") {
			sawPassword = true
		}
	}
	if ext == ".rar" || strings.HasSuffix(base, ".rar") || SplitArchiveInfo(base) != nil && strings.Contains(base, ".r") {
		if ur := which("unrar"); ur != "" {
			args := []string{"x", "-y", pw, "-kb", "-or", "--", archivePath, dest + string(os.PathSeparator)}
			out, code, err := runTool(ctx, ur, args...)
			if err == nil && code == 0 {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if code == 3 || code == 6 || looksLikePassword(out) {
				sawPassword = true
			}
		}
	}
	if ext == ".zip" {
		if uz := which("unzip"); uz != "" {
			args := []string{"-o"}
			if password != "" {
				args = append(args, "-P", password)
			}
			args = append(args, archivePath, "-d", dest)
			out, code, err := runTool(ctx, uz, args...)
			if err == nil && code == 0 {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if code == 50 || code == 82 || looksLikePassword(out) {
				sawPassword = true
			}
		}
		if password == "" {
			if err := extractZip(archivePath, dest); err == nil {
				return nil
			} else if errors.Is(err, ErrPasswordRequired) {
				sawPassword = true
			} else {
				slog.Warn("native zip extraction failed", "err", err)
			}
		}
	}
	if password == "" {
		switch {
		case ext == ".tar" || strings.HasSuffix(base, ".tar.gz") || ext == ".tgz" || strings.HasSuffix(base, ".tar.bz2") || ext == ".tbz2":
			if err := extractTar(archivePath, dest); err == nil {
				return nil
			} else {
				slog.Warn("native tar extraction failed", "err", err)
			}
		}
	}
	if sawPassword || password != "" {
		if password != "" {
			slog.Warn("extraction failed with supplied password", "file", filepath.Base(archivePath), "len", len(password))
		}
		return fmt.Errorf("%w: %s", ErrPasswordRequired, filepath.Base(archivePath))
	}
	return fmt.Errorf("could not extract %s (is 7z installed?)", filepath.Base(archivePath))
}

func tailStr(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func extractZip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			return ErrPasswordRequired
		}
		target, err := fsutil.SafeJoin(dest, f.Name)
		if err != nil {
			slog.Warn("zip-slip attempt skipped", "member", f.Name, "archive", filepath.Base(path))
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyZipMember(f, target); err != nil {
			return err
		}
	}
	return nil
}

func copyZipMember(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func extractTar(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	case strings.HasSuffix(lower, ".bz2") || strings.HasSuffix(lower, ".tbz2"):
		r = bzip2.NewReader(f)
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := fsutil.SafeJoin(dest, h.Name)
		if err != nil {
			slog.Warn("tar path traversal skipped", "member", h.Name)
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}
