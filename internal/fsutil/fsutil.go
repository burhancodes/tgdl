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
	"time"
	"unicode"

	"github.com/h2non/filetype"
)

type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}

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

type digitRun struct {
	start int
	end   int
	trim  int
}

type sortKeyPart struct {
	raw  string
	runs []digitRun
}

type sortKey struct {
	parts []sortKeyPart
}

func makeSortKeyPart(s string) sortKeyPart {
	raw := strings.ToLower(s)
	var runs []digitRun
	n := len(raw)
	for i := 0; i < n; {
		if isDigit(raw[i]) {
			start := i
			for i < n && isDigit(raw[i]) {
				i++
			}
			end := i
			trim := start
			for trim < end && raw[trim] == '0' {
				trim++
			}
			runs = append(runs, digitRun{start: start, end: end, trim: trim})
		} else {
			i++
		}
	}
	return sortKeyPart{raw: raw, runs: runs}
}

func makeSortKey(path string) sortKey {
	slashPath := filepath.ToSlash(path)
	components := strings.Split(slashPath, "/")
	parts := make([]sortKeyPart, len(components))
	for i, c := range components {
		parts[i] = makeSortKeyPart(c)
	}
	return sortKey{parts: parts}
}

func compareKeyPart(a, b *sortKeyPart) int {
	rawA, rawB := a.raw, b.raw
	lenA, lenB := len(rawA), len(rawB)
	runsA, runsB := a.runs, b.runs
	rIdxA, rIdxB := 0, 0
	numRunsA, numRunsB := len(runsA), len(runsB)

	i, j := 0, 0
	for i < lenA && j < lenB {
		if rIdxA < numRunsA && i == runsA[rIdxA].start && rIdxB < numRunsB && j == runsB[rIdxB].start {
			runA := runsA[rIdxA]
			runB := runsB[rIdxB]
			sigA := runA.end - runA.trim
			sigB := runB.end - runB.trim
			if sigA != sigB {
				if sigA < sigB {
					return -1
				}
				return 1
			}
			for k := 0; k < sigA; k++ {
				ca := rawA[runA.trim+k]
				cb := rawB[runB.trim+k]
				if ca != cb {
					if ca < cb {
						return -1
					}
					return 1
				}
			}
			totA := runA.end - runA.start
			totB := runB.end - runB.start
			if totA != totB {
				if totA > totB {
					return -1
				}
				return 1
			}
			i = runA.end
			j = runB.end
			rIdxA++
			rIdxB++
			continue
		}

		if rawA[i] != rawB[j] {
			if rawA[i] < rawB[j] {
				return -1
			}
			return 1
		}
		i++
		j++
		if rIdxA < numRunsA && i > runsA[rIdxA].end {
			rIdxA++
		}
		if rIdxB < numRunsB && j > runsB[rIdxB].end {
			rIdxB++
		}
	}

	remA := lenA - i
	remB := lenB - j
	if remA != remB {
		if remA < remB {
			return -1
		}
		return 1
	}
	return 0
}

func (k *sortKey) less(o *sortKey) bool {
	minParts := len(k.parts)
	if len(o.parts) < minParts {
		minParts = len(o.parts)
	}
	for p := 0; p < minParts; p++ {
		cmp := compareKeyPart(&k.parts[p], &o.parts[p])
		if cmp != 0 {
			return cmp < 0
		}
	}
	return len(k.parts) < len(o.parts)
}

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

type pathWithKey struct {
	key  sortKey
	path string
}

// SortNatural sorts paths in place using hierarchical natural ordering with precomputed keys.
func SortNatural(paths []string) {
	if len(paths) <= 1 {
		return
	}
	items := make([]pathWithKey, len(paths))
	for i, p := range paths {
		items[i] = pathWithKey{key: makeSortKey(p), path: p}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].key.less(&items[j].key)
	})
	for i, it := range items {
		paths[i] = it.path
	}
}

type fileInfoWithKey struct {
	key  sortKey
	info FileInfo
}

// SortNaturalFileInfo sorts FileInfo slice in place using hierarchical natural ordering with precomputed keys.
func SortNaturalFileInfo(infos []FileInfo) {
	if len(infos) <= 1 {
		return
	}
	items := make([]fileInfoWithKey, len(infos))
	for i, info := range infos {
		items[i] = fileInfoWithKey{key: makeSortKey(info.Path), info: info}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].key.less(&items[j].key)
	})
	for i, it := range items {
		infos[i] = it.info
	}
}

var (
	ignoredNames = map[string]bool{".ds_store": true, "thumbs.db": true, "desktop.ini": true}
	ignoredDirs  = map[string]bool{"__macosx": true, ".git": true, ".svn": true, ".hg": true, "$recycle.bin": true}
	ignoredExts  = map[string]bool{".part": true, ".crdownload": true, ".torrent": true}
)

// ShouldIgnoreName checks if a base file name is ignored without stat.
func ShouldIgnoreName(base string) bool {
	base = strings.ToLower(base)
	return ignoredNames[base] || strings.HasPrefix(base, "._") || ignoredExts[strings.ToLower(filepath.Ext(base))]
}

// ShouldIgnorePath checks if any directory component in path is ignored without stat.
func ShouldIgnorePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		p := strings.ToLower(part)
		if ignoredDirs[p] || strings.HasPrefix(p, "__macosx") || strings.HasSuffix(p, "_pd_temp") {
			return true
		}
	}
	return false
}

// ShouldIgnore reports whether path is OS metadata, VCS data, a partial
// download marker, or an empty file.
func ShouldIgnore(path string) bool {
	base := filepath.Base(path)
	if ShouldIgnoreName(base) || ShouldIgnorePath(path) {
		return true
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

// ListUploadableWithInfo returns non-ignored files beneath root in natural order with their size and mtime.
func ListUploadableWithInfo(root string) []FileInfo {
	var out []FileInfo
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			name := strings.ToLower(d.Name())
			if ignoredDirs[name] || strings.HasPrefix(name, "__macosx") || strings.HasSuffix(name, "_pd_temp") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if ShouldIgnoreName(name) || ShouldIgnorePath(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		out = append(out, FileInfo{
			Path:    p,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	SortNaturalFileInfo(out)
	return out
}

// ListUploadable returns non-ignored files beneath root in natural order.
func ListUploadable(root string) []string {
	infos := ListUploadableWithInfo(root)
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.Path
	}
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

func MakeWorldReadable(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return os.Chmod(path, 0o755)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		_ = os.Chmod(dir, 0o755)
	}
	return nil
}
