package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTailWriter_BoundedSize(t *testing.T) {
	tw := NewTailWriter(10)
	_, err := tw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if tw.String() != "hello" {
		t.Errorf("got %q, want %q", tw.String(), "hello")
	}

	_, err = tw.Write([]byte(" world!"))
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	// Total written: "hello world!" (12 bytes)
	// Last 10 bytes: "llo world!"
	want := "llo world!"
	if tw.String() != want {
		t.Errorf("got %q, want %q", tw.String(), want)
	}

	// Overwrite with a single large chunk > limit
	_, err = tw.Write([]byte("1234567890abcdef"))
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	// Last 10 bytes of "1234567890abcdef" are "7890abcdef"
	want2 := "7890abcdef"
	if tw.String() != want2 {
		t.Errorf("got %q, want %q", tw.String(), want2)
	}
}

func TestTailWriter_ConcurrentWrites(t *testing.T) {
	tw := NewTailWriter(100)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = tw.Write([]byte(fmt.Sprintf("thread-%d-line-%d\n", id, j)))
			}
		}(i)
	}
	wg.Wait()
	if len(tw.Bytes()) > 100 {
		t.Errorf("buffer exceeded limit: len=%d", len(tw.Bytes()))
	}
}

func TestGlobSorted_VolumeNaming(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"archive_parts.7z.003",
		"archive_parts.7z.001",
		"archive_parts.7z.010",
		"archive_parts.7z.002",
		"other_file.txt",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	got := globSorted(dir, "archive_parts.7z")
	want := []string{
		filepath.Join(dir, "archive_parts.7z.001"),
		filepath.Join(dir, "archive_parts.7z.002"),
		filepath.Join(dir, "archive_parts.7z.003"),
		filepath.Join(dir, "archive_parts.7z.010"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %s, want %s", i, got[i], want[i])
		}
	}
}
