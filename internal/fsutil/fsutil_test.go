package fsutil

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestFileName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantExt string
	}{
		{"ASCII", "simple_ascii_file.mkv", ".mkv"},
		{"accents", "café_élégance_crème_brûlée.mp4", ".mp4"},
		{"Japanese", "日本語の長いファイル名テスト_東京.mkv", ".mkv"},
		{"emoji", "動画テスト_🎉🚀🔥_great_movie.mkv", ".mkv"},
		{"name exactly 255 bytes", strings.Repeat("a", 251) + ".txt", ".txt"},
		{"name 256 bytes", strings.Repeat("a", 252) + ".txt", ".txt"},
		{"name 4000 bytes", strings.Repeat("日", 1330) + ".mkv", ".mkv"},
		{"no extension", strings.Repeat("a", 300), ""},
		{"extension only", ".gitignore", ".gitignore"},
		{"all dots", ".....", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := NewFileName(tc.input)
			if fn.Display != tc.input && tc.input != "....." {
				t.Errorf("Display changed: got %q, want %q", fn.Display, tc.input)
			}
			if len([]byte(fn.Disk)) > 255 {
				t.Errorf("Disk exceeds 255 bytes: %d bytes (%s)", len([]byte(fn.Disk)), fn.Disk)
			}
			if !utf8.ValidString(fn.Disk) {
				t.Errorf("Disk is not valid UTF-8: %s", fn.Disk)
			}
			if tc.wantExt != "" && !strings.HasSuffix(fn.Disk, tc.wantExt) {
				t.Errorf("Disk lost extension: got %s, want ext %s", fn.Disk, tc.wantExt)
			}
		})
	}

	fnA := NewFileName(strings.Repeat("a", 300) + "_alpha.mkv")
	fnB := NewFileName(strings.Repeat("a", 300) + "_bravo.mkv")
	if fnA.Disk == fnB.Disk {
		t.Fatalf("collision detected for distinct long names: %s == %s", fnA.Disk, fnB.Disk)
	}
}

func TestTruncateMiddle(t *testing.T) {
	s := "The_Cold_Sato_san_is_Only_Sweet_to_Me_S01E01_WEBRip_1080p_AV1.mkv"
	got := TruncateMiddle(s, 30)
	if len([]rune(got)) > 30 {
		t.Errorf("TruncateMiddle length %d > 30: %q", len([]rune(got)), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("TruncateMiddle missing ellipsis: %q", got)
	}
	if !strings.HasSuffix(got, ".mkv") {
		t.Errorf("TruncateMiddle lost extension: %q", got)
	}
}

func TestNaturalSort_DifferentialAgainstNaturalLess(t *testing.T) {
	prefixes := []string{"file", "img", "doc", "track", "vol", "ep", "part", "sample"}
	extensions := []string{".jpg", ".png", ".txt", ".mp4", ".mkv", ".7z", ".001", ""}
	delims := []string{"_", "-", ".", " ", ""}

	r := rand.New(rand.NewSource(42))
	const total = 5000
	names := make([]string, total)
	for i := 0; i < total; i++ {
		pref := prefixes[r.Intn(len(prefixes))]
		delim := delims[r.Intn(len(delims))]
		num := r.Intn(1000)
		numFmt := fmt.Sprintf("%d", num)
		if r.Intn(2) == 0 {
			numFmt = fmt.Sprintf("%03d", num)
		} else if r.Intn(3) == 0 {
			numFmt = fmt.Sprintf("%05d", num)
		}
		sub := ""
		if r.Intn(3) == 0 {
			sub = fmt.Sprintf("/sub%d/", r.Intn(10))
		}
		ext := extensions[r.Intn(len(extensions))]
		names[i] = sub + pref + delim + numFmt + ext
	}

	listA := append([]string(nil), names...)
	listB := append([]string(nil), names...)

	SortNatural(listA)
	sort.SliceStable(listB, func(i, j int) bool {
		return NaturalPathLess(listB[i], listB[j])
	})

	for i := 0; i < total; i++ {
		if listA[i] != listB[i] {
			t.Fatalf("mismatch at index %d: SortNatural got %q, NaturalPathLess got %q", i, listA[i], listB[i])
		}
	}
}
