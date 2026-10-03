// Package fsutil holds filesystem helpers: natural ordering, ignore rules,
// magic-byte extension repair and safe path handling.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/h2non/filetype"
)

// NaturalLess compares strings so that "file2" sorts before "file10".
func NaturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			si := i
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			sj := j
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			if (i - si) != (j - sj) {
				return (i - si) > (j - sj)
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// NaturalPathLess compares paths component by component using NaturalLess.
func NaturalPathLess(a, b string) bool {
	pa := strings.Split(filepath.ToSlash(a), "/")
	pb := strings.Split(filepath.ToSlash(b), "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		return NaturalLess(pa[i], pb[i])
	}
	return len(pa) < len(pb)
}

// SortNatural sorts paths in place using hierarchical natural ordering.
func SortNatural(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool { return NaturalPathLess(paths[i], paths[j]) })
}

var (
	ignoredNames = map[string]bool{".ds_store": true, "thumbs.db": true, "desktop.ini": true}
	ignoredDirs  = map[string]bool{"__macosx": true, ".git": true, ".svn": true, ".hg": true, "$recycle.bin": true}
	ignoredExts  = map[string]bool{".part": true, ".crdownload": true, ".torrent": true}
)

// ShouldIgnore reports whether path is OS metadata, VCS data, a partial
// download marker, or an empty file.
func ShouldIgnore(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if ignoredNames[base] || strings.HasPrefix(base, "._") || ignoredExts[strings.ToLower(filepath.Ext(base))] {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		p := strings.ToLower(part)
		if ignoredDirs[p] || strings.HasPrefix(p, "__macosx") || strings.HasSuffix(p, "_pd_temp") {
			return true
		}
	}
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() == 0 {
		return true
	}
	return false
}

// ListFiles returns all regular files beneath root (missing root => empty).
func ListFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// ListUploadable returns non-ignored files beneath root in natural order.
func ListUploadable(root string) []string {
	files, _ := ListFiles(root)
	out := files[:0]
	for _, f := range files {
		if !ShouldIgnore(f) {
			out = append(out, f)
		}
	}
	SortNatural(out)
	return out
}

// DirSize sums file sizes beneath root.
func DirSize(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// SafeJoin joins name beneath base and rejects any path escaping base
// (zip-slip / path traversal protection).
func SafeJoin(base, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	target := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe path %q escapes destination", name)
	}
	return target, nil
}

// SanitizeFilename strips path separators and control characters.
func SanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name
}

var genericExts = map[string]bool{".bin": true, ".tmp": true, ".part": true, ".download": true, "": true}

var extMap = map[string]string{
	"jpeg": ".jpg", "tif": ".tiff", "mpeg": ".mpg", "tts": ".ts",
}

// DetectExtension guesses a canonical extension from magic bytes.
func DetectExtension(path string) string {
	kind, err := filetype.MatchFile(path)
	if err != nil || kind == filetype.Unknown || kind.Extension == "" {
		return ""
	}
	ext := strings.ToLower(kind.Extension)
	if m, ok := extMap[ext]; ok {
		return m
	}
	return "." + ext
}

// EnsureExtension renames files with a generic/missing extension according to
// their detected type, avoiding collisions. It returns the resulting path.
func EnsureExtension(path string) string {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || !genericExts[strings.ToLower(filepath.Ext(path))] {
		return path
	}
	ext := DetectExtension(path)
	if ext == "" || strings.EqualFold(filepath.Ext(path), ext) {
		return path
	}
	dir, base := filepath.Split(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	target := filepath.Join(dir, stem+ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) || target == path {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, i, ext))
	}
	if err := os.Rename(path, target); err != nil {
		slog.Warn("extension repair failed", "file", base, "err", err)
		return path
	}
	slog.Info("renamed file with generic extension", "from", base, "to", filepath.Base(target))
	return target
}
