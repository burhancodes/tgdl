package status

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
	"time"
)

var sizeUnits = []string{"B", "KB", "MB", "GB", "TB", "PB"}

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

func Bar(pct float64) string {
	p := math.Min(math.Max(pct, 0), 100)
	full := int(p / (100.0 / 12))
	if full > 12 {
		full = 12
	}
	return "[" + strings.Repeat("●", full) + strings.Repeat("○", 12-full) + "]"
}

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

func Esc(s string) string { return html.EscapeString(s) }

func Code(s string) string { return "<code>" + Esc(s) + "</code>" }

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

func URLDisplay(u string, n int) string {
	u = strings.TrimSpace(u)
	for _, p := range []string{"https://", "http://"} {
		u = strings.TrimPrefix(u, p)
	}
	return Short(u, n)
}

var codeTagRE = regexp.MustCompile(`(?s)<code>(.*?)</code>`)

func Fit(text string, budget int) []string {
	if budget <= 0 {
		budget = 4096
	}

	var followUps []string
	matches := codeTagRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) > 0 {
		var b strings.Builder
		last := 0
		for _, m := range matches {
			rawInside := text[m[2]:m[3]]
			unesc := html.UnescapeString(rawInside)
			if len([]rune(unesc)) >= 3500 {
				b.WriteString(text[last:m[0]])
				b.WriteString("<code>[Name sent in follow-up message]</code>")
				last = m[1]
				followUps = append(followUps, Code(unesc))
			}
		}
		if len(followUps) > 0 {
			b.WriteString(text[last:])
			text = b.String()
		}
	}

	if len([]rune(text)) <= budget {
		return append([]string{text}, followUps...)
	}

	trimmed := shortenLeastImportant(text, budget)
	return append([]string{trimmed}, followUps...)
}

func shortenLeastImportant(text string, budget int) string {
	if len([]rune(text)) <= budget {
		return text
	}

	lines := strings.Split(text, "\n")
	var errorIdx = -1
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), "error") || strings.Contains(strings.ToLower(l), "failed") {
			errorIdx = i
			break
		}
	}

	if errorIdx >= 0 {
		excess := len([]rune(text)) - budget + 10
		r := []rune(lines[errorIdx])
		if len(r) > excess+20 {
			lines[errorIdx] = string(r[:len(r)-excess]) + "…"
			text = strings.Join(lines, "\n")
			if len([]rune(text)) <= budget {
				return text
			}
		}
	}

	for len([]rune(text)) > budget && len(lines) > 3 {
		removed := false
		for i := 1; i < len(lines)-1; i++ {
			if strings.HasPrefix(lines[i], "• ") && !strings.Contains(lines[i], "<code>") {
				lines = append(lines[:i], lines[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			for i := 1; i < len(lines)-1; i++ {
				if strings.HasPrefix(lines[i], "• ") {
					lines = append(lines[:i], lines[i+1:]...)
					removed = true
					break
				}
			}
		}
		if !removed {
			break
		}
		text = strings.Join(lines, "\n")
	}

	r := []rune(text)
	if len(r) > budget {
		if budget > 1 {
			return string(r[:budget-1]) + "…"
		}
		return string(r[:budget])
	}
	return text
}
