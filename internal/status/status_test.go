package status

import "testing"

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
