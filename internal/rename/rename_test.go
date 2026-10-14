package rename

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestName(t *testing.T) {
	orig := "Trix_The_Cold_Sato_san_is_Only_Sweet_to_Me_S01E01_WEBRip_1080p_AV1.mkv"

	got, notes, err := Name("The Cold Sato-san S01E01", orig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Display != "The Cold Sato-san S01E01.mkv" {
		t.Errorf("got %q, want %q", got.Display, "The Cold Sato-san S01E01.mkv")
	}
	if got.Disk != "The Cold Sato-san S01E01.mkv" {
		t.Errorf("got disk %q, want %q", got.Disk, "The Cold Sato-san S01E01.mkv")
	}
	if len(notes) != 0 {
		t.Errorf("expected no notes, got %+v", notes)
	}

	gotSameExt, _, err := Name("The Cold Sato-san S01E01.mkv", orig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSameExt.Display != "The Cold Sato-san S01E01.mkv" {
		t.Errorf("doubled extension: got %q", gotSameExt.Display)
	}

	gotDiffExt, _, err := Name("The Cold Sato-san S01E01.mp4", orig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDiffExt.Display != "The Cold Sato-san S01E01.mp4" {
		t.Errorf("expected custom extension preserved: got %q", gotDiffExt.Display)
	}

	gotLiteral, _, err := Name("{n:3}|upper S01", orig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotLiteral.Display != "{n:3}|upper S01.mkv" {
		t.Errorf("expected literal text: got %q", gotLiteral.Display)
	}

	gotSlash, slashNotes, err := Name("Show/Season 1\\Episode 1", orig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSlash.Display != "Show_Season 1_Episode 1.mkv" {
		t.Errorf("got %q, want Show_Season 1_Episode 1.mkv", gotSlash.Display)
	}
	foundSlash := false
	foundBackslash := false
	for _, n := range slashNotes {
		if strings.Contains(n, "changed '/' to '_'") {
			foundSlash = true
		}
		if strings.Contains(n, "changed '\\' to '_'") {
			foundBackslash = true
		}
	}
	if !foundSlash || !foundBackslash {
		t.Errorf("expected slash change notes, got %+v", slashNotes)
	}

	_, _, errEmpty := Name("", orig)
	if errEmpty == nil {
		t.Errorf("expected error on empty typed name")
	}

	_, _, errSpaces := Name("   ...   ", orig)
	if errSpaces == nil {
		t.Errorf("expected error on whitespace/dots typed name")
	}

	unicodeStem := strings.Repeat("日本語テスト文字", 30)
	gotLong, _, err := Name(unicodeStem, orig)
	if err != nil {
		t.Fatalf("unexpected error on long unicode name: %v", err)
	}
	if gotLong.Display != unicodeStem+".mkv" {
		t.Errorf("Display should not be truncated: got %q", gotLong.Display)
	}
	if len([]byte(gotLong.Disk)) > 255 {
		t.Errorf("Disk length exceeds 255 bytes: %d bytes", len([]byte(gotLong.Disk)))
	}
	if !utf8.ValidString(gotLong.Disk) {
		t.Errorf("Disk result is not valid UTF-8")
	}
	if !strings.HasSuffix(gotLong.Disk, ".mkv") {
		t.Errorf("Disk extension was lost: %q", gotLong.Disk)
	}
}

func TestPlanBatch(t *testing.T) {
	originals := []string{
		"file1.mkv",
		"file2.mkv",
		"file3.mkv",
	}
	names := []string{
		"Episode",
		"Episode",
		"Episode",
	}

	entries, err := PlanBatch(names, originals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].NewName.Display != "Episode.mkv" {
		t.Errorf("entry 0: got %q, want Episode.mkv", entries[0].NewName.Display)
	}
	if entries[1].NewName.Display != "Episode (2).mkv" {
		t.Errorf("entry 1: got %q, want Episode (2).mkv", entries[1].NewName.Display)
	}
	if entries[2].NewName.Display != "Episode (3).mkv" {
		t.Errorf("entry 2: got %q, want Episode (3).mkv", entries[2].NewName.Display)
	}
	if len(entries[1].Notes) == 0 || !strings.Contains(entries[1].Notes[0], "(2)") {
		t.Errorf("entry 1 notes missing duplicate note: %+v", entries[1].Notes)
	}

	mismatchNames := []string{"Ep 1", "Ep 2"}
	entriesMismatch, mismatchErr := PlanBatch(mismatchNames, originals)
	if mismatchErr == nil {
		t.Errorf("expected line count mismatch error, got nil")
	}
	if len(entriesMismatch) != 2 {
		t.Errorf("expected 2 planned entries for matched count, got %d", len(entriesMismatch))
	}
}
