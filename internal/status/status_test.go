package status

import (
	"strings"
	"testing"
)

func TestSizeAndDuration(t *testing.T) {
	if got := Size(1536); got != "1.50KB" {
		t.Errorf("Size = %q", got)
	}
	if got := Size(0); got != "0B" {
		t.Errorf("Size(0) = %q", got)
	}
	if got := Duration(3725); got != "1h2m5s" {
		t.Errorf("Duration = %q", got)
	}
}

func TestBarBounds(t *testing.T) {
	if Bar(-5) != "[○○○○○○○○○○○○]" {
		t.Errorf("Bar(-5) = %q", Bar(-5))
	}
	if Bar(500) != "[●●●●●●●●●●●●]" {
		t.Errorf("Bar(500) = %q", Bar(500))
	}
}

func TestRenameCard(t *testing.T) {
	snap := Snapshot{
		JobID:        "test1234",
		Status:       "downloading",
		IsRename:     true,
		RenameScheme: "{n:3} - {name}.{ext}",
		RenameDone:   3,
		RenameTotal:  20,
		RenameCurOld: "original.mkv",
		RenameCurNew: "003 - new.mkv",
	}
	card := Job(snap)
	if !strings.Contains(card, "<b>Rename</b>") || !strings.Contains(card, "3 / 20") || !strings.Contains(card, "original.mkv") {
		t.Errorf("card output missing rename info:\n%s", card)
	}
}

func TestFit(t *testing.T) {
	snap := Snapshot{
		JobID:       "123",
		Status:      "uploading",
		CurrentFile: "normal_file.mkv",
	}
	normalCard := Job(snap)
	msgs := Fit(normalCard, 4096)
	if len(msgs) != 1 || msgs[0] != normalCard {
		t.Fatalf("normal card should be unchanged, got %d messages", len(msgs))
	}

	longName := strings.Repeat("A", 5000) + ".mkv"
	snapLong := Snapshot{
		JobID:       "123",
		Status:      "uploading",
		CurrentFile: longName,
	}
	cardLong := Job(snapLong)
	msgsLong := Fit(cardLong, 4096)
	if len(msgsLong) < 2 {
		t.Fatalf("expected follow-up message for 5000-char name, got %d", len(msgsLong))
	}
	if len([]rune(msgsLong[0])) > 4096 {
		t.Errorf("card message exceeds 4096: %d", len([]rune(msgsLong[0])))
	}
	if !strings.Contains(msgsLong[1], longName) {
		t.Errorf("follow-up message missing full 5000-char name")
	}
}
