package jobs

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/tg"
)

type mockTGClient struct {
	tg.Client
	uploadFunc      func(ctx context.Context, chatID int64, u tg.Upload) error
	uploadGroupFunc func(ctx context.Context, chatID int64, items []tg.GroupItem) error
}

func (m *mockTGClient) Upload(ctx context.Context, chatID int64, u tg.Upload) error {
	if m.uploadFunc != nil {
		return m.uploadFunc(ctx, chatID, u)
	}
	return nil
}

func (m *mockTGClient) UploadGroup(ctx context.Context, chatID int64, items []tg.GroupItem) error {
	if m.uploadGroupFunc != nil {
		return m.uploadGroupFunc(ctx, chatID, items)
	}
	return nil
}

func (m *mockTGClient) Send(ctx context.Context, chatID int64, text string, o tg.SendOpts) (tg.MessageRef, error) {
	return tg.MessageRef{ChatID: chatID, ID: 100}, nil
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
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
			attempts.Add(1)
			return tg.ErrAmbiguousUpload
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
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
			attempts.Add(1)
			return regularErr
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
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
			if attempts.Add(1) == 1 {
				return &tg.FloodError{Seconds: 0}
			}
			return nil
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
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
			attempts.Add(1)
			uploadedKind = u.Kind
			return nil
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
		uploadFunc: func(ctx context.Context, chatID int64, u tg.Upload) error {
			kinds = append(kinds, u.Kind)
			if attempts.Add(1) == 1 {
				return &tg.BadRequestError{
					Message: "Bad Request: file of size 20509595 bytes is too big for a photo; the maximum size is 10485760 bytes",
				}
			}
			return nil
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
