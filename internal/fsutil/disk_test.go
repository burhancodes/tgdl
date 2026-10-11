package fsutil

import (
	"errors"
	"testing"
)

func TestFreeDiskSpace(t *testing.T) {
	free, err := FreeDiskSpace(".")
	if err != nil {
		t.Fatalf("FreeDiskSpace failed: %v", err)
	}
	if free == 0 {
		t.Errorf("expected free space > 0, got %d", free)
	}

	// Test with injected mock function
	orig := StatfsFreeFunc
	defer func() { StatfsFreeFunc = orig }()

	StatfsFreeFunc = func(path string) (uint64, error) {
		return 123456789, nil
	}
	got, err := FreeDiskSpace("/any/path")
	if err != nil || got != 123456789 {
		t.Fatalf("expected 123456789, got %d (err: %v)", got, err)
	}

	StatfsFreeFunc = func(path string) (uint64, error) {
		return 0, errors.New("disk error")
	}
	_, err = FreeDiskSpace("/any/path")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
