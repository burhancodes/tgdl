// Package pacing implements rate limiting, throttling and backoff helpers.
package pacing

import (
	"context"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var rateLimitRE = regexp.MustCompile(`(?i)\b(429|403|too many requests|rate.?limit|temporarily blocked|forbidden|quota exceeded|retry after|cloudflare|turnstile)\b`)

// LooksRateLimited reports whether tool output suggests upstream throttling.
func LooksRateLimited(text string) bool { return text != "" && rateLimitRE.MatchString(text) }

// Backoff is exponential backoff with full jitter.
type Backoff struct {
	Base        time.Duration
	Multiplier  float64
	MaxAttempts int
	MaxDelay    time.Duration
	attempt     int
}

func NewBackoff(base time.Duration, mult float64, maxAttempts int) *Backoff {
	return &Backoff{Base: base, Multiplier: mult, MaxAttempts: maxAttempts, MaxDelay: 60 * time.Second}
}

func (b *Backoff) Reset()          { b.attempt = 0 }
func (b *Backoff) Exhausted() bool { return b.attempt >= b.MaxAttempts }

// Next returns a jittered delay in [0, min(MaxDelay, Base*Multiplier^attempt)].
func (b *Backoff) Next() time.Duration {
	d := float64(b.Base) * math.Pow(b.Multiplier, float64(b.attempt))
	if d > float64(b.MaxDelay) {
		d = float64(b.MaxDelay)
	}
	b.attempt++
	return time.Duration(rand.Float64() * d)
}

// Sleep waits for d or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RandomDelay sleeps for a uniformly random duration in [min, max].
func RandomDelay(ctx context.Context, min, max time.Duration, multiplier float64) error {
	if max < min {
		max = min
	}
	d := min + time.Duration(rand.Float64()*float64(max-min))
	return Sleep(ctx, time.Duration(float64(d)*multiplier))
}

// ---- Telegram rate limiter ---------------------------------------------

const (
	staleAfter         = 24 * time.Hour
	perUploadGap       = 4 * time.Second
	defaultChatGap     = 3 * time.Second
	defaultGlobalOPS   = 30.0
	chatWindowDuration = 60 * time.Second
	maxCallsPerWindow  = 18 // Telegram allows 20 msgs/min per chat; keep safety buffer of 2
)

type chatState struct {
	mu          sync.Mutex // serialises pacing within a chat
	lastCall    time.Time
	lastUpload  time.Time
	floodUntil  time.Time
	lastTouched time.Time
	history     []time.Time // timestamps of recent calls within chatWindowDuration
}

// TelegramLimiter enforces global and per-chat pacing plus FloodWait penalties.
type TelegramLimiter struct {
	mu          sync.Mutex
	chats       map[int64]*chatState
	globalLast  time.Time
	globalFlood time.Time
	globalOPS   float64
	chatGap     time.Duration
	uploadGap   time.Duration
	maxCalls    int
	windowDur   time.Duration
}

func NewTelegramLimiter() *TelegramLimiter {
	return &TelegramLimiter{
		chats:     map[int64]*chatState{},
		globalOPS: defaultGlobalOPS,
		chatGap:   defaultChatGap,
		uploadGap: perUploadGap,
		maxCalls:  maxCallsPerWindow,
		windowDur: chatWindowDuration,
	}
}

func (l *TelegramLimiter) chat(id int64) *chatState {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.chats[id]
	if !ok {
		c = &chatState{lastTouched: time.Now()}
		l.chats[id] = c
	}
	return c
}

// Acquire blocks until a call to chatID is allowed.
func (l *TelegramLimiter) Acquire(ctx context.Context, chatID int64) error {
	l.mu.Lock()
	gf := l.globalFlood
	l.mu.Unlock()
	if w := time.Until(gf); w > 0 {
		if err := Sleep(ctx, w); err != nil {
			return err
		}
	}

	// Global pacing: reserve the next slot under the lock, sleep outside it.
	l.mu.Lock()
	slot := l.globalLast.Add(time.Duration(float64(time.Second) / l.globalOPS))
	if slot.Before(time.Now()) {
		slot = time.Now()
	}
	l.globalLast = slot
	l.mu.Unlock()
	if err := Sleep(ctx, time.Until(slot)); err != nil {
		return err
	}

	if chatID == 0 {
		return nil
	}

	c := l.chat(chatID)
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		now := time.Now()
		// 1. Check chat flood wait
		if w := c.floodUntil.Sub(now); w > 0 {
			c.mu.Unlock()
			err := Sleep(ctx, w)
			c.mu.Lock()
			if err != nil {
				return err
			}
			continue
		}

		// 2. Check per-chat gap
		if w := l.chatGap - now.Sub(c.lastCall); w > 0 {
			c.mu.Unlock()
			err := Sleep(ctx, w)
			c.mu.Lock()
			if err != nil {
				return err
			}
			continue
		}

		// 3. Sliding window check (max calls per 60s)
		cutoff := now.Add(-l.windowDur)
		i := 0
		for i < len(c.history) && c.history[i].Before(cutoff) {
			i++
		}
		if i > 0 {
			c.history = c.history[i:]
		}

		if l.maxCalls > 0 && len(c.history) >= l.maxCalls {
			oldest := c.history[0]
			waitDur := oldest.Add(l.windowDur).Sub(now)
			if waitDur > 0 {
				c.mu.Unlock()
				err := Sleep(ctx, waitDur)
				c.mu.Lock()
				if err != nil {
					return err
				}
				continue
			}
		}

		break
	}

	now := time.Now()
	c.lastCall = now
	c.history = append(c.history, now)
	c.lastTouched = now
	return nil
}

// SetGaps overrides the per-chat call gap and upload gap (useful for testing).
func (l *TelegramLimiter) SetGaps(chatGap, uploadGap time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chatGap = chatGap
	if uploadGap <= 0 {
		l.uploadGap = -1
	} else {
		l.uploadGap = uploadGap
	}
}

// AcquireUpload applies the stricter upload spacing on top of Acquire.
func (l *TelegramLimiter) AcquireUpload(ctx context.Context, chatID int64) error {
	if err := l.Acquire(ctx, chatID); err != nil {
		return err
	}
	if chatID == 0 {
		return nil
	}
	c := l.chat(chatID)
	c.mu.Lock()
	defer c.mu.Unlock()

	gap := l.uploadGap
	if gap < 0 {
		gap = 0
	} else if gap == 0 {
		gap = perUploadGap
	}
	for {
		now := time.Now()
		if w := gap - now.Sub(c.lastUpload); w > 0 {
			c.mu.Unlock()
			err := Sleep(ctx, w)
			c.mu.Lock()
			if err != nil {
				return err
			}
			continue
		}
		break
	}
	now := time.Now()
	c.lastUpload = now
	c.lastTouched = now
	return nil
}

// InFlood reports whether the chat (or global bot API) is currently in a flood wait penalty.
func (l *TelegramLimiter) InFlood(chatID int64) bool {
	l.mu.Lock()
	gf := l.globalFlood
	l.mu.Unlock()
	if time.Now().Before(gf) {
		return true
	}
	if chatID == 0 {
		return false
	}
	l.mu.Lock()
	c, ok := l.chats[chatID]
	l.mu.Unlock()
	if !ok || c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.floodUntil)
}

// RecordCallDone updates the completion timestamp of a Telegram API call.
func (l *TelegramLimiter) RecordCallDone(chatID int64) {
	if chatID == 0 {
		return
	}
	c := l.chat(chatID)
	c.mu.Lock()
	now := time.Now()
	if now.After(c.lastCall) {
		c.lastCall = now
	}
	c.lastTouched = now
	c.mu.Unlock()
}

// RecordUploadDone updates the completion timestamp of an upload operation.
func (l *TelegramLimiter) RecordUploadDone(chatID int64) {
	if chatID == 0 {
		return
	}
	c := l.chat(chatID)
	c.mu.Lock()
	now := time.Now()
	if now.After(c.lastUpload) {
		c.lastUpload = now
	}
	if now.After(c.lastCall) {
		c.lastCall = now
	}
	c.lastTouched = now
	c.mu.Unlock()
}

// NotifyFloodWait registers a penalty. chatID == 0 applies globally.
func (l *TelegramLimiter) NotifyFloodWait(seconds int, chatID int64) {
	margin := 2 * time.Second
	if seconds == 0 {
		margin = 1 * time.Second
	}
	until := time.Now().Add(time.Duration(seconds)*time.Second + margin)
	if chatID == 0 {
		l.mu.Lock()
		if until.After(l.globalFlood) {
			l.globalFlood = until
		}
		l.mu.Unlock()
		return
	}
	c := l.chat(chatID)
	c.mu.Lock()
	if until.After(c.floodUntil) {
		c.floodUntil = until
	}
	c.mu.Unlock()
}

// Sweep evicts state for chats idle beyond the staleness threshold.
func (l *TelegramLimiter) Sweep(active map[int64]bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for id, c := range l.chats {
		if active[id] || time.Since(c.lastTouched) < staleAfter || !c.mu.TryLock() {
			continue
		}
		delete(l.chats, id)
		c.mu.Unlock()
		n++
	}
	return n
}

// RunSweeper periodically evicts stale chats until ctx is cancelled.
func (l *TelegramLimiter) RunSweeper(ctx context.Context, active func() map[int64]bool) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Sweep(active())
		}
	}
}

// ---- Download throttling ------------------------------------------------

var speedRE = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)\s*$`)

var speedUnits = map[string]float64{
	"": 1, "b": 1,
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
}

// ParseSpeedLimit parses "20M", "500K", "1.5M", "1024" into bytes/sec.
// It returns 0 for unlimited or invalid input.
func ParseSpeedLimit(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "none", "0", "unlimited", "off", "false":
		return 0
	}
	m := speedRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	mult, ok := speedUnits[m[2]]
	if err != nil || !ok {
		return 0
	}
	if n := int64(v * mult); n > 0 {
		return n
	}
	return 0
}

// Throttler is a token-bucket limiter for chunked streams. Not safe for
// concurrent use; create one per stream.
type Throttler struct {
	rate   float64
	tokens float64
	last   time.Time
}

// NewThrottler returns a limiter for rate bytes/sec (0 = unlimited).
func NewThrottler(rate int64) *Throttler {
	return &Throttler{rate: float64(rate), tokens: float64(rate), last: time.Now()}
}

// Consume accounts for n bytes and sleeps as needed.
func (t *Throttler) Consume(ctx context.Context, n int) error {
	if t == nil || t.rate <= 0 || n <= 0 {
		return nil
	}
	now := time.Now()
	t.tokens = math.Min(t.rate, t.tokens+now.Sub(t.last).Seconds()*t.rate)
	t.last = now
	t.tokens -= float64(n)
	if t.tokens < 0 {
		wait := time.Duration(-t.tokens / t.rate * float64(time.Second))
		if err := Sleep(ctx, wait); err != nil {
			return err
		}
		t.last = time.Now()
		t.tokens = 0
	}
	return nil
}
