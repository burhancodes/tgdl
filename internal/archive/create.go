package archive

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/burhanverse/tgdl/internal/fsutil"
)

// Create archives dir into out ("zip" or "7z"). It prefers 7z and falls back
// to a native zip writer for the zip format.
func Create(ctx context.Context, dir, out, format string, opts ...FolderOpts) error {
	threads := 2
	zipLevel := 1
	if len(opts) > 0 {
		if opts[0].Threads > 0 {
			threads = opts[0].Threads
		}
		if opts[0].ZipLevel > 0 {
			zipLevel = opts[0].ZipLevel
		}
	}
	format = strings.TrimPrefix(strings.ToLower(format), "-")
	if format != "zip" && format != "7z" {
		format = "zip"
	}
	if !strings.HasSuffix(strings.ToLower(out), "."+format) {
		out += "." + format
	}
	if files, _ := fsutil.ListFiles(dir); len(files) == 0 {
		return fmt.Errorf("no files found in %s to archive", dir)
	}
	if sz := which("7z", "7zz", "7za"); sz != "" {
		cmd := "-tzip"
		compArgs := []string{fmt.Sprintf("-mx=%d", zipLevel)}
		if format == "7z" {
			cmd = "-t7z"
			compArgs = []string{"-mx=3", "-md=16m", "-ms=off"}
		}
		abs, _ := filepath.Abs(out)
		args := []string{"a", cmd, fmt.Sprintf("-mmt=%d", threads), "-bso0", "-bsp0", "-bb0", "-y"}
		args = append(args, compArgs...)
		args = append(args, abs, ".")
		c := execIn(ctx, dir, sz, args...)
		if err := c.Run(); err == nil {
			if st, err := os.Stat(out); err == nil && st.Size() > 0 {
				return nil
			}
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			slog.Warn("7z create failed", "err", err)
		}
	}
	if format == "zip" {
		return createZip(dir, out)
	}
	return fmt.Errorf("7z is required to create .7z archives")
}

func createZip(dir, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(out)
	}
	return err
}

// Uploader mirrors a file to Pixeldrain and returns its URL.
type Uploader func(ctx context.Context, path string) (string, error)

// Link is a (filename, url) mirror record.
type Link struct{ Name, URL string }

// FolderOpts tunes ArchiveFolder.
type FolderOpts struct {
	Format     string
	MaxPartMB  int
	MirrorPD   Uploader // nil disables mirroring
	OnMirror   func(Link)
	TempDir    string
	MirrorDone *sync.WaitGroup
	Threads    int
	ZipLevel   int
}

const pixeldrainMax = 10 << 30

// ArchiveFolder compresses folder into one archive (or ≤MaxPartMB volumes),
// optionally mirroring to a file host, then deletes the folder. It returns the
// files to upload. On failure the folder is left untouched.
func ArchiveFolder(ctx context.Context, folder string, o FolderOpts) ([]string, error) {
	if abs, err := filepath.Abs(folder); err == nil {
		folder = abs
	}
	st, err := os.Stat(folder)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", folder)
	}
	format := strings.TrimPrefix(strings.ToLower(o.Format), "-")
	if format != "zip" && format != "7z" {
		format = "zip"
	}
	if o.MaxPartMB <= 0 {
		o.MaxPartMB = 1900
	}
	threads := o.Threads
	if threads <= 0 {
		threads = 2
	}
	zipLevel := o.ZipLevel
	if zipLevel <= 0 {
		zipLevel = 1
	}

	parent, name := filepath.Dir(folder), filepath.Base(folder)
	limit := int64(o.MaxPartMB) << 20

	// Single-pass volume creation: when expected archive size exceeds MaxPartMB,
	// create volumes directly from folder.
	folderSize := fsutil.DirSize(folder)
	if folderSize > limit {
		if sz := which("7z", "7zz", "7za"); sz != "" {
			flag := "-tzip"
			compArgs := []string{fmt.Sprintf("-mx=%d", zipLevel)}
			if format == "7z" {
				flag = "-t7z"
				compArgs = []string{"-mx=3", "-md=16m", "-ms=off"}
			}
			prefix := filepath.Join(parent, name+"_parts."+format)
			args := []string{"a", flag, fmt.Sprintf("-v%dm", o.MaxPartMB), fmt.Sprintf("-mmt=%d", threads), "-bso0", "-bsp0", "-bb0", "-y"}
			args = append(args, compArgs...)
			args = append(args, prefix, ".")
			runErr := execIn(ctx, folder, sz, args...).Run()
			parts := globSorted(parent, filepath.Base(prefix))
			if runErr == nil && len(parts) > 0 {
				_ = os.RemoveAll(folder)
				if o.MirrorPD != nil {
					mirrorInBackground(ctx, parts, parent, name, o)
				}
				return parts, nil
			}
			// Clean up partial volumes before falling back
			for _, p := range parts {
				_ = os.Remove(p)
			}
		}
	}

	archive := filepath.Join(parent, name+"."+format)
	if err := Create(ctx, folder, archive, format, o); err != nil {
		return nil, fmt.Errorf("archive %s: %w", name, err)
	}
	ast, _ := os.Stat(archive)
	split := ast.Size() > limit

	var parts []string
	if split {
		parts = splitVolumes(ctx, archive, parent, name, format, o.MaxPartMB, threads)
	}
	if len(parts) == 0 {
		parts = []string{archive}
	} else if _, err := os.Stat(archive); err == nil {
		// 7z volumes were created from the archive; drop the source.
		_ = os.Remove(archive)
	}

	if o.MirrorPD != nil && (split || ast.Size() <= pixeldrainMax) {
		mirrorInBackground(ctx, parts, parent, name, o)
	}
	_ = os.RemoveAll(folder)
	return parts, nil
}

func splitVolumes(ctx context.Context, archive, parent, name, format string, mb int, threads int) []string {
	if threads <= 0 {
		threads = 2
	}
	if sz := which("7z", "7zz", "7za"); sz != "" {
		flag := "-tzip"
		if format == "7z" {
			flag = "-t7z"
		}
		prefix := filepath.Join(parent, name+"_parts."+format)
		// Second pass uses store mode (-mx=0), never recompresses.
		args := []string{"a", flag, "-mx=0", fmt.Sprintf("-v%dm", mb), fmt.Sprintf("-mmt=%d", threads), "-bso0", "-bsp0", "-bb0", "-y", prefix, archive}
		if err := execIn(ctx, parent, sz, args...).Run(); err == nil {
			return globSorted(parent, filepath.Base(prefix))
		}
	}
	if sp := which("split"); sp != "" {
		prefix := archive + "."
		if err := execIn(ctx, parent, sp, "-b", fmt.Sprintf("%dm", mb), "-d", "-a", "3", archive, prefix).Run(); err == nil {
			_ = os.Remove(archive)
			return globSorted(parent, filepath.Base(prefix))
		}
	}
	return nil
}

func globSorted(dir, prefix string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && (e.Name() == prefix || strings.HasPrefix(e.Name(), prefix+".")) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return fsutil.NaturalPathLess(out[i], out[j]) })
	return out
}

// mirrorInBackground links the parts into a temporary directory outside st.Dest
// (falling back to copy if Link fails) and uploads them asynchronously without being cancelled by parent.
func mirrorInBackground(ctx context.Context, parts []string, parent, name string, o FolderOpts) {
	tmp := o.TempDir
	if tmp == "" {
		tmp = filepath.Join(parent, name+"_pd_temp")
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		slog.Error("mirror temp dir failed", "err", err)
		return
	}
	var copies []string
	for _, p := range parts {
		dst := filepath.Join(tmp, filepath.Base(p))
		if err := os.Link(p, dst); err != nil {
			if err := copyFile(p, dst); err != nil {
				slog.Error("mirror copy failed", "file", p, "err", err)
				continue
			}
		}
		copies = append(copies, dst)
	}
	if o.MirrorDone != nil {
		o.MirrorDone.Add(1)
	}
	go func(ctx context.Context) {
		if o.MirrorDone != nil {
			defer o.MirrorDone.Done()
		}
		defer os.RemoveAll(tmp)
		for _, c := range copies {
			url, err := o.MirrorPD(ctx, c)
			if err != nil {
				slog.Error("background mirror upload failed", "file", filepath.Base(c), "err", err)
				continue
			}
			if o.OnMirror != nil {
				o.OnMirror(Link{Name: filepath.Base(c), URL: url})
			}
		}
	}(context.WithoutCancel(ctx))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
