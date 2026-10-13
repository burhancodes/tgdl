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
