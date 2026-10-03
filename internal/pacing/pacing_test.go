package pacing

import (
	"testing"
	"time"
)

func TestParseSpeedLimit(t *testing.T) {
	cases := map[string]int64{
		"":          0,
		"none":      0,
		"0":         0,
		"unlimited": 0,
		"garbage":   0,
		"1024":      1024,
		"500K":      500 << 10,
		"20M":       20 << 20,
		"1.5M":      int64(1.5 * float64(1<<20)),
		"2g":        2 << 30,
	}
	for in, want := range cases {
		if got := ParseSpeedLimit(in); got != want {
			t.Errorf("ParseSpeedLimit(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLooksRateLimited(t *testing.T) {
	if !LooksRateLimited("HTTP 429 Too Many Requests") {
		t.Error("expected 429 to be detected")
	}
	if LooksRateLimited("download finished") {
		t.Error("unexpected rate-limit detection")
	}
}

func TestBackoffExhaustion(t *testing.T) {
	b := NewBackoff(time.Second, 2, 3)
	for i := 0; i < 3; i++ {
		if b.Exhausted() {
			t.Fatalf("exhausted too early at attempt %d", i)
		}
		if d := b.Next(); d < 0 || d > b.MaxDelay {
			t.Fatalf("delay %v out of range", d)
		}
	}
	if !b.Exhausted() {
		t.Error("expected backoff to be exhausted")
	}
	b.Reset()
	if b.Exhausted() {
		t.Error("Reset should clear attempts")
	}
}
