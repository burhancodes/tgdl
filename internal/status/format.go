// Package status renders human-readable job cards and formatting helpers.
package status

import (
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

var sizeUnits = []string{"B", "KB", "MB", "GB", "TB", "PB"}

// Size formats a byte count like "1.50GB".
func Size(n float64) string {
	if n <= 0 || math.IsNaN(n) {
		return "0B"
	}
	i := 0
	for n >= 1024 && i < len(sizeUnits)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f%s", n, sizeUnits[i])
}

// Duration formats seconds like "1h2m3s".
func Duration(sec float64) string {
	s := int(sec)
	if s <= 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		n string
		d int
	}{{"d", 86400}, {"h", 3600}, {"m", 60}, {"s", 1}} {
		if s >= u.d {
			fmt.Fprintf(&b, "%d%s", s/u.d, u.n)
			s %= u.d
		}
	}
	return b.String()
}

// Bar renders a 12-slot progress bar.
func Bar(pct float64) string {
	p := math.Min(math.Max(pct, 0), 100)
	full := int(p / (100.0 / 12))
	if full > 12 {
		full = 12
	}
	return "[" + strings.Repeat("●", full) + strings.Repeat("○", 12-full) + "]"
}

// Marquee renders an indeterminate bar that animates with wall-clock time.
func Marquee() string {
	const width = 10
	pos := int(time.Now().UnixMilli()/400) % (width*2 - 2)
	if pos >= width {
		pos = width*2 - 2 - pos
	}
	b := []rune(strings.Repeat("░", width))
	b[pos] = '█'
	return string(b)
}

// Esc escapes text for Telegram HTML.
func Esc(s string) string { return html.EscapeString(s) }

// Code wraps escaped text in <code>.
func Code(s string) string { return "<code>" + Esc(s) + "</code>" }

// Short truncates s to n runes with an ellipsis.
func Short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// URLDisplay shortens a URL for display: host + trimmed path.
func URLDisplay(u string, n int) string {
	u = strings.TrimSpace(u)
	for _, p := range []string{"https://", "http://"} {
		u = strings.TrimPrefix(u, p)
	}
	return Short(u, n)
}
