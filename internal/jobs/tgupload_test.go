package jobs

import (
	"context"
	"errors"
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
