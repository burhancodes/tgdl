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

func TestTelegramLimiter_InFlood(t *testing.T) {
	lim := NewTelegramLimiter()
	if lim.InFlood(123) {
		t.Error("expected not in flood initially")
	}

	lim.NotifyFloodWait(2, 123)
	if !lim.InFlood(123) {
		t.Error("expected chat 123 in flood wait")
	}
	if lim.InFlood(456) {
		t.Error("chat 456 should not be in flood wait")
	}

	lim.NotifyFloodWait(2, 0) // global
	if !lim.InFlood(456) {
		t.Error("chat 456 should be in flood wait due to global penalty")
	}
}

func TestTelegramLimiter_RecordDone(t *testing.T) {
	lim := NewTelegramLimiter()
	lim.chatGap = 10 * time.Millisecond
	lim.uploadGap = 50 * time.Millisecond

	ctx := t.Context()
	if err := lim.AcquireUpload(ctx, 123); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	lim.RecordUploadDone(123)

	if err := lim.AcquireUpload(ctx, 123); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected upload gap >= 40ms, got %v", elapsed)
	}
}

func TestTelegramLimiter_SlidingWindow(t *testing.T) {
	lim := NewTelegramLimiter()
	lim.chatGap = 5 * time.Millisecond
	lim.maxCalls = 3
	lim.windowDur = 80 * time.Millisecond

	ctx := t.Context()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := lim.Acquire(ctx, 999); err != nil {
			t.Fatal(err)
		}
	}

	// The 4th call must wait for the sliding window to free up a slot (from start of 1st call)
	if err := lim.Acquire(ctx, 999); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 75*time.Millisecond {
		t.Errorf("expected total window time >= 75ms, got %v", elapsed)
	}
}
