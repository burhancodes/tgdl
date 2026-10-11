package archive

import "sync"

// TailWriter is a bounded, thread-safe writer that retains only the last limit bytes written.
type TailWriter struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

// NewTailWriter creates a new TailWriter keeping at most limit bytes (default 64 KiB if <= 0).
func NewTailWriter(limit int) *TailWriter {
	if limit <= 0 {
		limit = 64 * 1024
	}
	return &TailWriter{limit: limit}
}

func (w *TailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if n >= w.limit {
		w.buf = append(w.buf[:0], p[n-w.limit:]...)
		return n, nil
	}
	over := (len(w.buf) + n) - w.limit
	if over > 0 {
		copy(w.buf, w.buf[over:])
		w.buf = w.buf[:len(w.buf)-over]
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

// Bytes returns a copy of the accumulated tail buffer.
func (w *TailWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, len(w.buf))
	copy(cp, w.buf)
	return cp
}

// String returns the accumulated tail buffer as a string.
func (w *TailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
