package rename

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTokensAndModifiers(t *testing.T) {
	refTime := time.Date(2026, 10, 14, 15, 4, 5, 0, time.UTC)
	in := Input{
		OriginalName: "My.Awesome.Show.S02E05.1080p.mkv",
		Size:         15728640,
		ModTime:      refTime,
		Parent:       "Season 2",
		Title:        "The Awakening",
		Artist:       "Director X",
		Track:        "5",
		Year:         "2026",
	}

	cases := []struct {
		name     string
		scheme   string
		opts     Options
		input    Input
		wantName string
	}{
		{
			name:     "token name and ext",
			scheme:   "{name}.{ext}",
			input:    in,
			wantName: "My.Awesome.Show.S02E05.1080p.mkv",
		},
		{
			name:     "token counter padded",
			scheme:   "{n:3} - {name}.{ext}",
			opts:     Options{Start: 5},
			input:    in,
			wantName: "005 - My.Awesome.Show.S02E05.1080p.mkv",
		},
		{
			name:     "token date default and format",
			scheme:   "{date:20060102}_{name}.{ext}",
			opts:     Options{Now: refTime},
			input:    in,
			wantName: "20261014_My.Awesome.Show.S02E05.1080p.mkv",
		},
		{
			name:     "token mtime",
			scheme:   "{mtime:2006-01-02}_{name}.{ext}",
			opts:     Options{Now: refTime},
			input:    in,
			wantName: "2026-10-14_My.Awesome.Show.S02E05.1080p.mkv",
		},
		{
			name:     "token size formatted and bytes",
			scheme:   "{name}_{size}_{size:bytes}.{ext}",
			input:    in,
			wantName: "My.Awesome.Show.S02E05.1080p_15.0 MB_15728640.mkv",
		},
		{
			name:     "token parent",
			scheme:   "{parent}/{name}.{ext}",
			input:    in,
			wantName: "Season 2 My.Awesome.Show.S02E05.1080p.mkv",
		},
		{
			name:     "token season episode from filename",
			scheme:   "{parent}_S{season:2}E{episode:2}_{title}.{ext}",
			input:    in,
			wantName: "Season 2_S02E05_The Awakening.mkv",
		},
		{
			name:     "token artist track year",
			scheme:   "{track} - {artist} - {title} ({year}).{ext}",
			input:    in,
			wantName: "5 - Director X - The Awakening (2026).mkv",
		},
		{
			name:     "modifier lower",
			scheme:   "{name|lower}.{ext}",
			input:    in,
			wantName: "my.awesome.show.s02e05.1080p.mkv",
		},
		{
			name:     "modifier upper",
			scheme:   "{name|upper}.{ext}",
			input:    in,
			wantName: "MY.AWESOME.SHOW.S02E05.1080P.mkv",
		},
		{
			name:     "modifier title",
			scheme:   "{title|title}.{ext}",
			input:    Input{OriginalName: "song.mp3", Title: "the silent night"},
			wantName: "The Silent Night.mp3",
		},
		{
			name:     "modifier slug",
			scheme:   "{name|slug}.{ext}",
			input:    Input{OriginalName: "Hello World! Episode #1.mp4"},
			wantName: "hello-world-episode-1.mp4",
		},
		{
			name:     "modifier trim",
			scheme:   "{title|trim}.{ext}",
			input:    Input{OriginalName: "test.txt", Title: "   spaced out   "},
			wantName: "spaced out.txt",
		},
		{
			name:     "modifier replace",
			scheme:   "{name|replace:.:_}.{ext}",
			input:    Input{OriginalName: "a.b.c.txt"},
			wantName: "a_b_c.txt",
		},
		{
			name:     "modifier regex",
			scheme:   "{name|regex:\\[.*?\\]:}.{ext}",
			input:    Input{OriginalName: "[Group] Video [720p].mkv"},
			wantName: "Video.mkv",
		},
		{
			name:     "modifier cut",
			scheme:   "{name|cut:0:5}.{ext}",
			input:    Input{OriginalName: "longfilenamehere.txt"},
			wantName: "longf.txt",
		},
		{
			name:     "modifier remove",
			scheme:   "{name|remove:[_.-]}.{ext}",
			input:    Input{OriginalName: "a_b-c.d.txt"},
			wantName: "abcd.txt",
		},
		{
			name:     "literal escaping braces",
			scheme:   "{{prefix}}_{name}_{{suffix}}.{ext}",
			input:    Input{OriginalName: "file.doc"},
			wantName: "{prefix}_file_{suffix}.doc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse(tc.scheme)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tc.scheme, err)
			}
			opts := tc.opts
			opts.Now = refTime
			plan, err := s.Plan([]Input{tc.input}, opts)
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}
			if len(plan.Entries) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(plan.Entries))
			}
			got := plan.Entries[0].NewName
			if got != tc.wantName {
				t.Errorf("got %q, want %q", got, tc.wantName)
			}
		})
	}
}

func TestKeepExtension(t *testing.T) {
	s, err := Parse("{n:2} - {name}")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Plan([]Input{{OriginalName: "track.flac"}}, Options{KeepExtension: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Entries[0].NewName != "01 - track.flac" {
		t.Errorf("got %q, want %q", plan.Entries[0].NewName, "01 - track.flac")
	}
}

func TestParseErrorOffset(t *testing.T) {
	_, err := Parse("test_{name")
	if err == nil {
		t.Fatal("expected error for unclosed brace")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if pe.Offset != 5 {
		t.Errorf("expected offset 5, got %d", pe.Offset)
	}

	_, err = Parse("test_name}")
	if err == nil {
		t.Fatal("expected error for unexpected closing brace")
	}
	if !errors.As(err, &pe) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if pe.Offset != 9 {
		t.Errorf("expected offset 9, got %d", pe.Offset)
	}

	longPattern := strings.Repeat("a", 205)
	_, err = Parse("{name|regex:" + longPattern + ":b}")
	if err == nil {
		t.Fatal("expected error for pattern > 200 chars")
	}
}

func TestConflictResolution(t *testing.T) {
	files := []Input{
		{OriginalName: "photo_1.jpg"},
		{OriginalName: "photo_2.jpg"},
		{OriginalName: "photo_3.jpg"},
	}

	s, _ := Parse("image.{ext}")

	planSuffix, err := s.Plan(files, Options{OnConflict: "suffix"})
	if err != nil {
		t.Fatal(err)
	}
	expectedSuffix := []string{"image.jpg", "image (2).jpg", "image (3).jpg"}
	for i, e := range planSuffix.Entries {
		if e.NewName != expectedSuffix[i] {
			t.Errorf("suffix entry %d: got %q, want %q", i, e.NewName, expectedSuffix[i])
		}
	}

	planSkip, err := s.Plan(files, Options{OnConflict: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	if len(planSkip.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(planSkip.Entries))
	}
	if planSkip.Entries[0].Skipped || !planSkip.Entries[1].Skipped || !planSkip.Entries[2].Skipped {
		t.Errorf("unexpected skip states: %+v", planSkip.Entries)
	}

	_, err = s.Plan(files, Options{OnConflict: "error"})
	if err == nil {
		t.Fatal("expected error on conflict mode error")
	}
}

func TestSorting(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	files := []Input{
		{OriginalName: "file10.txt", Size: 300, ModTime: t2},
		{OriginalName: "file2.txt", Size: 100, ModTime: t3},
		{OriginalName: "file1.txt", Size: 200, ModTime: t1},
	}

	s, _ := Parse("{n} - {name}.{ext}")

	planName, _ := s.Plan(files, Options{SortBy: "name"})
	if planName.Entries[0].OldName != "file1.txt" || planName.Entries[1].OldName != "file2.txt" || planName.Entries[2].OldName != "file10.txt" {
		t.Errorf("natural name sort mismatch: %+v", planName.Entries)
	}

	planSize, _ := s.Plan(files, Options{SortBy: "size"})
	if planSize.Entries[0].OldName != "file2.txt" || planSize.Entries[1].OldName != "file1.txt" || planSize.Entries[2].OldName != "file10.txt" {
		t.Errorf("size sort mismatch: %+v", planSize.Entries)
	}

	planTime, _ := s.Plan(files, Options{SortBy: "time"})
	if planTime.Entries[0].OldName != "file1.txt" || planTime.Entries[1].OldName != "file10.txt" || planTime.Entries[2].OldName != "file2.txt" {
		t.Errorf("time sort mismatch: %+v", planTime.Entries)
	}

	planSent, _ := s.Plan(files, Options{SortBy: "as-sent"})
	if planSent.Entries[0].OldName != "file10.txt" || planSent.Entries[1].OldName != "file2.txt" || planSent.Entries[2].OldName != "file1.txt" {
		t.Errorf("as-sent sort mismatch: %+v", planSent.Entries)
	}
}

func TestSanitizeAndLimits(t *testing.T) {
	bad := "folder/sub\\name\x00\x1f\n\ttest.txt"
	clean, _ := Sanitize(bad, "orig.txt")
	if strings.ContainsAny(clean, "/\\\x00\x1f\n\t") {
		t.Errorf("sanitize failed to strip control/separators: %q", clean)
	}

	fallback, warn := Sanitize("", "default.txt")
	if fallback != "default.txt" || warn == "" {
		t.Errorf("expected fallback on empty, got %q, warn %q", fallback, warn)
	}

	fallbackDot, _ := Sanitize(".", "default.txt")
	if fallbackDot != "default.txt" {
		t.Errorf("expected fallback on dot, got %q", fallbackDot)
	}

	longStem := strings.Repeat("世界", 150) + ".mkv"
	sanitizedLong, warnLong := Sanitize(longStem, "fallback.mkv")
	if len([]byte(sanitizedLong)) > 255 {
		t.Errorf("name exceeded 255 bytes: %d bytes", len([]byte(sanitizedLong)))
	}
	if !strings.HasSuffix(sanitizedLong, ".mkv") {
		t.Errorf("extension was truncated: %q", sanitizedLong)
	}
	if warnLong == "" {
		t.Error("expected warning for truncated name")
	}
}

func TestPresets(t *testing.T) {
	for name, scheme := range Presets {
		s, err := Parse(scheme)
		if err != nil {
			t.Fatalf("preset %q failed to parse: %v", name, err)
		}
		in := Input{OriginalName: "Episode 1.mp4", Parent: "Series"}
		plan, err := s.Plan([]Input{in}, Options{})
		if err != nil {
			t.Fatalf("preset %q failed to plan: %v", name, err)
		}
		if len(plan.Entries) != 1 || plan.Entries[0].NewName == "" {
			t.Errorf("preset %q produced empty plan", name)
		}
	}

	v, ok := LookupPreset("CLEAN")
	if !ok || v != Presets["clean"] {
		t.Errorf("lookup clean preset failed: got %q, %v", v, ok)
	}
}

func FuzzParsePlan(f *testing.F) {
	f.Add("{name}.{ext}", "test.txt", "parent")
	f.Add("{n:3} - {name|lower}.{ext}", "Sample Video.mkv", "Folder")
	f.Add("{{escaped}}_{date:2006}_{rand:4}.{ext}", "audio.mp3", "")

	f.Fuzz(func(t *testing.T, schemeStr, filename, parent string) {
		s, err := Parse(schemeStr)
		if err != nil {
			return
		}
		in := Input{
			OriginalName: filename,
			Parent:       parent,
			Size:         1024,
			ModTime:      time.Now(),
		}
		plan, planErr := s.Plan([]Input{in}, Options{})
		if planErr != nil {
			return
		}
		for _, e := range plan.Entries {
			if len([]byte(e.NewName)) > 255 {
				t.Errorf("planned name exceeds 255 bytes: %q (%d bytes)", e.NewName, len([]byte(e.NewName)))
			}
			if strings.ContainsAny(e.NewName, "/\\\x00") {
				t.Errorf("planned name contains invalid characters: %q", e.NewName)
			}
			if e.NewName == "." || e.NewName == ".." {
				t.Errorf("planned name is dot: %q", e.NewName)
			}
		}
	})
}
