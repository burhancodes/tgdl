package fsutil

import (
	"path/filepath"
	"sort"
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

func TestNaturalSort_EqualPrefix(t *testing.T) {
	in := []string{"file", "file.txt", "file10", "file2", "file1", "file01"}
	SortNatural(in)
	want := []string{"file", "file.txt", "file01", "file1", "file2", "file10"}
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("got %v, want %v", in, want)
		}
	}
}

func TestNaturalSort_LeadingZeros(t *testing.T) {
	in := []string{"a002", "a02", "a2", "a001", "a1", "a010", "a10"}
	SortNatural(in)
	want := []string{"a001", "a1", "a002", "a02", "a2", "a010", "a10"}
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("got %v, want %v", in, want)
		}
	}
}

func generate20kPaths() []string {
	paths := make([]string, 20000)
	for i := 0; i < 20000; i++ {
		dir := i % 100
		fileNum := (i * 37) % 20000
		paths[i] = filepath.Join("folder", "sub"+string(rune('a'+(dir%26))), "file_"+string(rune('0'+(fileNum%10)))+".dat")
	}
	return paths
}

func BenchmarkSortNatural_20k(b *testing.B) {
	paths := generate20kPaths()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp := make([]string, len(paths))
		copy(cp, paths)
		SortNatural(cp)
	}
}

func BenchmarkSortNaturalLegacy_20k(b *testing.B) {
	paths := generate20kPaths()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp := make([]string, len(paths))
		copy(cp, paths)
		sort.SliceStable(cp, func(x, y int) bool { return NaturalPathLess(cp[x], cp[y]) })
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
