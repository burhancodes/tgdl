package jobs

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/tg"
)

type mockTGClient struct {
	tg.Client
	uploadFunc      func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error)
	uploadGroupFunc func(ctx context.Context, chatID int64, items []tg.GroupItem) ([]tg.MessageRef, error)
	sendFunc        func(ctx context.Context, chatID int64, text string, o tg.SendOpts) (tg.MessageRef, error)
	mu              sync.Mutex
	sentMessages    []string
	sentReplies     []int
}

func (m *mockTGClient) Upload(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
	if m.uploadFunc != nil {
		return m.uploadFunc(ctx, chatID, u)
	}
	return tg.MessageRef{ChatID: chatID, ID: 100}, nil
}

func (m *mockTGClient) UploadGroup(ctx context.Context, chatID int64, items []tg.GroupItem) ([]tg.MessageRef, error) {
	if m.uploadGroupFunc != nil {
		return m.uploadGroupFunc(ctx, chatID, items)
	}
	refs := make([]tg.MessageRef, len(items))
	for i := range items {
		refs[i] = tg.MessageRef{ChatID: chatID, ID: 100 + i}
	}
	return refs, nil
}

func (m *mockTGClient) Send(ctx context.Context, chatID int64, text string, o tg.SendOpts) (tg.MessageRef, error) {
	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, text)
	m.sentReplies = append(m.sentReplies, o.ReplyTo)
	m.mu.Unlock()
	if m.sendFunc != nil {
		return m.sendFunc(ctx, chatID, text, o)
	}
	return tg.MessageRef{ChatID: chatID, ID: 200}, nil
}

func (m *mockTGClient) Edit(ctx context.Context, ref tg.MessageRef, text string, o tg.SendOpts) error {
	return nil
}

func (m *mockTGClient) Delete(ctx context.Context, ref tg.MessageRef) error {
	return nil
}

func TestTGUploader_NoRetryOnAmbiguousUpload(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	mock := &mockTGClient{
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
			attempts.Add(1)
			return tg.MessageRef{}, tg.ErrAmbiguousUpload
		},
	}

	lim := pacing.NewTelegramLimiter()
	uploader := newTGUploader(mock, lim, 100*1024*1024)

	_, err := uploader.Upload(context.Background(), 123, filePath, false, nil)
	if !errors.Is(err, tg.ErrAmbiguousUpload) {
		t.Fatalf("expected ErrAmbiguousUpload, got: %v", err)
	}

	if attempts.Load() != 1 {
		t.Errorf("expected exactly 1 attempt on ErrAmbiguousUpload, got %d", attempts.Load())
	}
}

func TestTGUploader_RetryOnRegularErrorBeforeBody(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	regularErr := errors.New("connection reset by peer before body fully sent")
	mock := &mockTGClient{
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
			attempts.Add(1)
			return tg.MessageRef{}, regularErr
		},
	}

	lim := pacing.NewTelegramLimiter()
	uploader := newTGUploader(mock, lim, 100*1024*1024)

	_, err := uploader.Upload(context.Background(), 123, filePath, false, nil)
	if !errors.Is(err, regularErr) {
		t.Fatalf("expected regularErr, got: %v", err)
	}

	if attempts.Load() != 3 {
		t.Errorf("expected 3 retry attempts on regular error, got %d", attempts.Load())
	}
}

func TestTGUploader_FloodErrorHandled(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	mock := &mockTGClient{
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
			if attempts.Add(1) == 1 {
				return tg.MessageRef{}, &tg.FloodError{Seconds: 0}
			}
			return tg.MessageRef{ChatID: chatID, ID: 100}, nil
		},
	}

	lim := pacing.NewTelegramLimiter()
	uploader := newTGUploader(mock, lim, 100*1024*1024)

	_, err := uploader.Upload(context.Background(), 123, filePath, false, nil)
	if err != nil {
		t.Fatalf("expected success after flood wait, got: %v", err)
	}

	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestTGUploader_LargeImageDirectToDocument(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "large.png")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(11 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	var uploadedKind tg.Kind
	var attempts atomic.Int32
	mock := &mockTGClient{
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
			attempts.Add(1)
			uploadedKind = u.Kind
			return tg.MessageRef{ChatID: chatID, ID: 100}, nil
		},
	}

	lim := pacing.NewTelegramLimiter()
	uploader := newTGUploader(mock, lim, 100*1024*1024)

	asDoc, err := uploader.Upload(context.Background(), 123, filePath, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", attempts.Load())
	}
	if uploadedKind != tg.KindDocument {
		t.Fatalf("expected KindDocument for >10MB image, got %v", uploadedKind)
	}
	if !asDoc {
		t.Errorf("expected asDoc to be true for >10MB image")
	}
}

func TestTGUploader_PhotoTooBigStickyDoc(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.png")
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	var attempts atomic.Int32
	var kinds []tg.Kind
	mock := &mockTGClient{
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) (tg.MessageRef, error) {
			kinds = append(kinds, u.Kind)
			if attempts.Add(1) == 1 {
				return tg.MessageRef{}, &tg.BadRequestError{
					Message: "Bad Request: file of size 20509595 bytes is too big for a photo; the maximum size is 10485760 bytes",
				}
			}
			return tg.MessageRef{ChatID: chatID, ID: 100}, nil
		},
	}

	lim := pacing.NewTelegramLimiter()
	uploader := newTGUploader(mock, lim, 100*1024*1024)

	asDoc, err := uploader.Upload(context.Background(), 123, filePath, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if len(kinds) != 2 || kinds[1] != tg.KindDocument {
		t.Fatalf("expected second attempt as document, got kinds: %v", kinds)
	}
	if !asDoc {
		t.Errorf("expected sticky asDoc to be true")
	}
}

func TestBuildCaption(t *testing.T) {
	shortName := "my_normal_file.mkv"
	c, ovf := buildCaption(shortName)
	if ovf {
		t.Errorf("expected no overflow for short name")
	}
	if c != "<code>my_normal_file.mkv</code>" {
		t.Errorf("unexpected caption: %q", c)
	}

	name1024 := strings.Repeat("a", 1020) + ".mkv"
	if len([]rune(name1024)) != 1024 {
		t.Fatalf("expected 1024 runes, got %d", len([]rune(name1024)))
	}
	c1024, ovf1024 := buildCaption(name1024)
	if ovf1024 {
		t.Errorf("expected no overflow for 1024 name")
	}
	if c1024 != "<code>"+name1024+"</code>" {
		t.Errorf("expected exact match for 1024 name")
	}

	name1025 := strings.Repeat("b", 1021) + ".mkv"
	c1025, ovf1025 := buildCaption(name1025)
	if !ovf1025 {
		t.Errorf("expected overflow for 1025 name")
	}
	expected1025 := "<code>" + strings.Repeat("b", 1020) + "…</code>"
	if c1025 != expected1025 {
		t.Errorf("got %q, want %q", c1025, expected1025)
	}

	specialName := strings.Repeat("<>&\"🔥", 250) + "end"
	cSpec, ovfSpec := buildCaption(specialName)
	if !ovfSpec {
		t.Errorf("expected overflow for special long name")
	}
	if !strings.HasPrefix(cSpec, "<code>") || !strings.HasSuffix(cSpec, "…</code>") {
		t.Errorf("malformed caption wrappers: %q", cSpec)
	}
	if strings.Contains(cSpec, "<&") || strings.Contains(cSpec, "<\"") {
		t.Errorf("broken tag or entity in caption: %q", cSpec)
	}

	name9000 := strings.Repeat("漢字", 4500)
	c9000, ovf9000 := buildCaption(name9000)
	if !ovf9000 {
		t.Errorf("expected overflow for 9000 name")
	}
	expectedRunes := []rune(name9000)[:1020]
	expected9000 := "<code>" + string(expectedRunes) + "…</code>"
	if c9000 != expected9000 {
		t.Errorf("caption 9000 cut mismatch")
	}
}

func TestTGUploader_OverflowReplies(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.bin")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	name1500 := strings.Repeat("x", 1496) + ".bin"
	mock1500 := &mockTGClient{}
	lim := pacing.NewTelegramLimiter()
	uploader1500 := newTGUploader(mock1500, lim, 100*1024*1024)

	fn1500 := fsutil.NewFileName(name1500)
	_, err := uploader1500.Upload(context.Background(), 12345, filePath, false, nil, fn1500)
	if err != nil {
		t.Fatalf("upload 1500 failed: %v", err)
	}
	if len(mock1500.sentMessages) != 1 {
		t.Fatalf("expected 1 overflow reply for 1500-char name, got %d", len(mock1500.sentMessages))
	}
	if mock1500.sentReplies[0] != 100 {
		t.Errorf("overflow reply should reply to uploaded msg ID 100, got %d", mock1500.sentReplies[0])
	}
	if mock1500.sentMessages[0] != "<code>"+name1500+"</code>" {
		t.Errorf("overflow reply content mismatch")
	}

	name9000 := strings.Repeat("y", 9000)
	mock9000 := &mockTGClient{}
	uploader9000 := newTGUploader(mock9000, lim, 100*1024*1024)
	fn9000 := fsutil.NewFileName(name9000)
	_, err = uploader9000.Upload(context.Background(), 12345, filePath, false, nil, fn9000)
	if err != nil {
		t.Fatalf("upload 9000 failed: %v", err)
	}
	if len(mock9000.sentMessages) != 3 {
		t.Fatalf("expected 3 chunks (4000+4000+1000) for 9000-char name, got %d", len(mock9000.sentMessages))
	}
	for i, r := range mock9000.sentReplies {
		if r != 100 {
			t.Errorf("chunk %d should reply to uploaded msg ID 100, got %d", i, r)
		}
	}
	if mock9000.sentMessages[0] != "<code>"+strings.Repeat("y", 4000)+"</code>" {
		t.Errorf("chunk 0 mismatch")
	}
	if mock9000.sentMessages[1] != "<code>"+strings.Repeat("y", 4000)+"</code>" {
		t.Errorf("chunk 1 mismatch")
	}
	if mock9000.sentMessages[2] != "<code>"+strings.Repeat("y", 1000)+"</code>" {
		t.Errorf("chunk 2 mismatch")
	}
}
