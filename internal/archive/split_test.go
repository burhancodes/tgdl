package archive

import "testing"

func TestSplitArchiveInfo(t *testing.T) {
	cases := []struct {
		name  string
		kind  Kind
		part  int
		first string
	}{
		{"movie.zip.003", NumericSuffix, 3, "movie.zip.001"},
		{"movie.part02.rar", PartInfix, 2, "movie.part01.rar"},
		{"movie.part2-abc.rar", PartInfix, 2, "movie.part1.rar"},
		{"data.007", NumericSuffixOnly, 7, "data.001"},
		{"old.r00", RarR, 1, "old.rar"},
		{"old.z01", ZipZ, 2, "old.zip"},
	}
	for _, c := range cases {
		si := SplitArchiveInfo(c.name)
		if si == nil {
			t.Errorf("%s: expected split info", c.name)
			continue
		}
		if si.Kind != c.kind || si.Part != c.part || si.First != c.first {
			t.Errorf("%s: got kind=%v part=%d first=%q, want kind=%v part=%d first=%q",
				c.name, si.Kind, si.Part, si.First, c.kind, c.part, c.first)
		}
	}
	if SplitArchiveInfo("plain.zip") != nil {
		t.Error("plain.zip is not a split archive")
	}
}

func TestPasswordDetection(t *testing.T) {
	if !looksLikePassword("ERROR: Wrong password : file.txt") {
		t.Error("expected password error")
	}
	if looksLikePassword("Cannot find volume archive.part2.rar") {
		t.Error("missing volume must not be treated as a password problem")
	}
}
