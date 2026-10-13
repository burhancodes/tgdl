package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

type rotator struct {
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func newRotator(path string, max int64, keep int) (*rotator, error) {
	r := &rotator{path: path, max: max, keep: keep}
	return r, r.open()
}

func (r *rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotator) Write(p []byte) (int, error) {
	if r.size+int64(len(p)) > r.max {
		_ = r.f.Close()
		for i := r.keep - 1; i >= 1; i-- {
			_ = os.Rename(r.path+"."+itoa(i), r.path+"."+itoa(i+1))
		}
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func Setup(cfg *config.Config) func() {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARNING":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}

	if cfg.LogLevelVar == nil {
		cfg.LogLevelVar = &slog.LevelVar{}
	}
	cfg.LogLevelVar.Set(level)

	var w io.Writer = os.Stderr
	closer := func() {}
	if r, err := newRotator(filepath.Join(cfg.LogDir, "bot.log"), 10_000_000, 5); err == nil {
		w = io.MultiWriter(os.Stderr, r)
		closer = func() { _ = r.f.Close() }
	} else {
		slog.Warn("file logging unavailable, using console only", "err", err)
	}

	opts := &slog.HandlerOptions{Level: cfg.LogLevelVar, AddSource: cfg.LogLevel == "DEBUG"}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(h))
	return closer
}

func TruncateLogFile(path string, maxSize int64) {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= maxSize {
		return
	}
	rotated := path + ".1"
	_ = os.Remove(rotated)
	_ = os.Rename(path, rotated)
}

func StartLogTruncator(ctx context.Context, logDir string, maxSize int64, filenames ...string) {
	check := func() {
		for _, fn := range filenames {
			TruncateLogFile(filepath.Join(logDir, fn), maxSize)
		}
	}
	check()
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				check()
			}
		}
	}()
}
