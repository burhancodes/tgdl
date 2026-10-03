package fsutil

import (
	"path/filepath"
	"testing"
)

func TestNaturalSort(t *testing.T) {
	in := []string{"file10.jpg", "file2.jpg", "File1.jpg", "file02.jpg"}
	SortNatural(in)
	want := []string{"File1.jpg", "file02.jpg", "file2.jpg", "file10.jpg"}
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("got %v, want %v", in, want)
		}
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	for _, bad := range []string{"../evil", "a/../../evil", "/etc/passwd"} {
		if _, err := SafeJoin(base, bad); err == nil {
			t.Errorf("SafeJoin(%q) should fail", bad)
		}
	}
	got, err := SafeJoin(base, "sub/file.txt")
	if err != nil || got != filepath.Join(base, "sub", "file.txt") {
		t.Errorf("SafeJoin ok-case = %q, %v", got, err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := SanitizeFilename("a/b\\c.txt"); got != "a_b_c.txt" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeFilename("  ..  "); got != "file" {
		t.Errorf("got %q", got)
	}
}
