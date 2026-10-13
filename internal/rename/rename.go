package rename

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/burhanverse/tgdl/internal/fsutil"
)

var Presets = map[string]string{
	"sequence":    "{n:3} - {name}.{ext}",
	"clean":       "{name|slug}.{ext}",
	"date-prefix": "{date:2006-01-02}_{name}.{ext}",
	"tv":          "{parent} - S{season:2}E{episode:2}.{ext}",
}

func LookupPreset(name string) (string, bool) {
	val, ok := Presets[strings.ToLower(strings.TrimSpace(name))]
	return val, ok
}

type ParseError struct {
	Offset int
	Msg    string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at position %d: %s", e.Offset, e.Msg)
}

type modifier struct {
	name string
	args []string
}

type tokenPart struct {
	raw       string
	isToken   bool
	name      string
	format    string
	modifiers []modifier
}

type Scheme struct {
	raw   string
	parts []tokenPart
}

type Input struct {
	OriginalName string
	Size         int64
	ModTime      time.Time
	Parent       string
	Title        string
	Artist       string
	Track        string
	Year         string
	Season       int
	Episode      int
}

type Options struct {
	Start         int       `json:"start,omitempty"`
	Step          int       `json:"step,omitempty"`
	SortBy        string    `json:"sort_by,omitempty"`
	KeepExtension bool      `json:"keep_extension,omitempty"`
	CaseMode      string    `json:"case_mode,omitempty"`
	OnConflict    string    `json:"on_conflict,omitempty"`
	Now           time.Time `json:"now,omitempty"`
}

type Entry struct {
	OldName string
	NewName string
	Skipped bool
}

type Plan struct {
	Entries  []Entry
	Warnings []string
}

func Parse(scheme string) (*Scheme, error) {
	s := &Scheme{raw: scheme}
	i := 0
	n := len(scheme)

	for i < n {
		if scheme[i] == '{' {
			if i+1 < n && scheme[i+1] == '{' {
				s.parts = append(s.parts, tokenPart{raw: "{"})
				i += 2
				continue
			}
			start := i
			i++
			tokenEnd := -1
			for i < n {
				if scheme[i] == '}' {
					if i+1 < n && scheme[i+1] == '}' {
						i += 2
						continue
					}
					tokenEnd = i
					i++
					break
				}
				i++
			}
			if tokenEnd == -1 {
				return nil, &ParseError{Offset: start, Msg: "unclosed token brace"}
			}
			content := scheme[start+1 : tokenEnd]
			part, err := parseTokenContent(content, start+1)
			if err != nil {
				return nil, err
			}
			s.parts = append(s.parts, part)
			continue
		}
		if scheme[i] == '}' {
			if i+1 < n && scheme[i+1] == '}' {
				s.parts = append(s.parts, tokenPart{raw: "}"})
				i += 2
				continue
			}
			return nil, &ParseError{Offset: i, Msg: "unexpected closing brace"}
		}

		start := i
		for i < n && scheme[i] != '{' && scheme[i] != '}' {
			i++
		}
		s.parts = append(s.parts, tokenPart{raw: scheme[start:i]})
	}

	return s, nil
}

func parseTokenContent(content string, baseOffset int) (tokenPart, error) {
	if strings.TrimSpace(content) == "" {
		return tokenPart{}, &ParseError{Offset: baseOffset, Msg: "empty token"}
	}

	sections := strings.Split(content, "|")
	mainPart := strings.TrimSpace(sections[0])

	name := mainPart
	format := ""
	if colonIdx := strings.Index(mainPart, ":"); colonIdx != -1 {
		name = strings.TrimSpace(mainPart[:colonIdx])
		format = strings.TrimSpace(mainPart[colonIdx+1:])
	}

	tp := tokenPart{
		isToken: true,
		name:    strings.ToLower(name),
		format:  format,
	}

	for idx, modRaw := range sections[1:] {
		modRaw = strings.TrimSpace(modRaw)
		if modRaw == "" {
			continue
		}
		modParts := strings.Split(modRaw, ":")
		modName := strings.ToLower(strings.TrimSpace(modParts[0]))
		modArgs := modParts[1:]
		if modName == "regex" && len(modArgs) > 0 && len(modArgs[0]) > 200 {
			modOffset := baseOffset + len(sections[0]) + 1
			for k := 0; k < idx; k++ {
				modOffset += len(sections[1+k]) + 1
			}
			return tokenPart{}, &ParseError{Offset: modOffset, Msg: "regex pattern exceeds 200 characters"}
		}
		tp.modifiers = append(tp.modifiers, modifier{
			name: modName,
			args: modArgs,
		})
	}

	return tp, nil
}

var seRegex = regexp.MustCompile(`(?i)(?:s|season\s*)(\d+)[._\s-]*(?:e|ep|episode\s*)(\d+)|(\d+)x(\d+)`)

func extractSeasonEpisode(name string) (int, int) {
	matches := seRegex.FindStringSubmatch(name)
	if len(matches) == 0 {
		return 0, 0
	}
	if matches[1] != "" && matches[2] != "" {
		s, _ := strconv.Atoi(matches[1])
		e, _ := strconv.Atoi(matches[2])
		return s, e
	}
	if len(matches) > 4 && matches[3] != "" && matches[4] != "" {
		s, _ := strconv.Atoi(matches[3])
		e, _ := strconv.Atoi(matches[4])
		return s, e
	}
	return 0, 0
}

func (s *Scheme) HasExtToken() bool {
	for _, p := range s.parts {
		if p.isToken && p.name == "ext" {
			return true
		}
	}
	return false
}

func (s *Scheme) Plan(files []Input, opts Options) (Plan, error) {
	var plan Plan
	if len(files) == 0 {
		return plan, nil
	}

	if opts.Step == 0 {
		opts.Step = 1
	}
	if opts.Start == 0 {
		opts.Start = 1
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	type sortItem struct {
		in    Input
		index int
	}

	items := make([]sortItem, len(files))
	for i, f := range files {
		items[i] = sortItem{in: f, index: i}
	}

	switch strings.ToLower(opts.SortBy) {
	case "name":
		sort.SliceStable(items, func(i, j int) bool {
			return fsutil.NaturalLess(items[i].in.OriginalName, items[j].in.OriginalName)
		})
	case "time", "mtime":
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].in.ModTime.Equal(items[j].in.ModTime) {
				return items[i].index < items[j].index
			}
			return items[i].in.ModTime.Before(items[j].in.ModTime)
		})
	case "size":
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].in.Size == items[j].in.Size {
				return items[i].index < items[j].index
			}
			return items[i].in.Size < items[j].in.Size
		})
	}

	usedNames := make(map[string]int)
	hasExt := s.HasExtToken()

	for idx, item := range items {
		in := item.in
		countVal := opts.Start + (idx * opts.Step)
		candidate := s.eval(in, countVal, now)

		if opts.KeepExtension && !hasExt {
			ext := filepath.Ext(in.OriginalName)
			if ext != "" && !strings.HasSuffix(candidate, ext) {
				candidate += ext
			}
		}

		sanitized, warn := Sanitize(candidate, in.OriginalName)
		if warn != "" {
			plan.Warnings = append(plan.Warnings, warn)
		}

		switch strings.ToLower(opts.CaseMode) {
		case "lower":
			sanitized = strings.ToLower(sanitized)
		case "upper":
			sanitized = strings.ToUpper(sanitized)
		case "title":
			sanitized = applyTitle(sanitized)
		}
		if len([]byte(sanitized)) > 255 {
			sanitized, _ = Sanitize(sanitized, in.OriginalName)
		}

		lookupKey := strings.ToLower(sanitized)
		if existingCount, exists := usedNames[lookupKey]; exists {
			switch strings.ToLower(opts.OnConflict) {
			case "error":
				return Plan{}, fmt.Errorf("naming conflict for %q", sanitized)
			case "skip":
				plan.Entries = append(plan.Entries, Entry{
					OldName: in.OriginalName,
					NewName: sanitized,
					Skipped: true,
				})
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("skipped %q due to duplicate name %q", in.OriginalName, sanitized))
				continue
			default:
				resolvedName := resolveConflictSuffix(sanitized, usedNames, existingCount+1)
				sanitized = resolvedName
				usedNames[strings.ToLower(sanitized)] = 1
			}
		} else {
			usedNames[lookupKey] = 1
		}

		plan.Entries = append(plan.Entries, Entry{
			OldName: in.OriginalName,
			NewName: sanitized,
		})
	}

	return plan, nil
}

func resolveConflictSuffix(name string, used map[string]int, startCounter int) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	counter := startCounter
	for {
		suffix := fmt.Sprintf(" (%d)", counter)
		candidate := stem + suffix + ext
		if len([]byte(candidate)) > 255 {
			candidate, _ = Sanitize(candidate, "file")
		}
		if _, exists := used[strings.ToLower(candidate)]; !exists {
			return candidate
		}
		counter++
	}
}

func (s *Scheme) eval(in Input, n int, now time.Time) string {
	var b strings.Builder
	origName := in.OriginalName
	origExtWithDot := filepath.Ext(origName)
	origStem := strings.TrimSuffix(origName, origExtWithDot)
	origExt := strings.TrimPrefix(origExtWithDot, ".")

	season := in.Season
	episode := in.Episode
	if season == 0 && episode == 0 {
		season, episode = extractSeasonEpisode(origName)
	}

	for _, p := range s.parts {
		if !p.isToken {
			b.WriteString(p.raw)
			continue
		}

		val := ""
		switch p.name {
		case "name":
			val = origStem
		case "ext":
			val = origExt
		case "n":
			if p.format != "" {
				if pad, err := strconv.Atoi(p.format); err == nil && pad > 0 {
					val = fmt.Sprintf("%0*d", pad, n)
				} else {
					val = strconv.Itoa(n)
				}
			} else {
				val = strconv.Itoa(n)
			}
		case "date":
			layout := "2006-01-02"
			if p.format != "" {
				layout = p.format
			}
			val = now.Format(layout)
		case "mtime":
			layout := "2006-01-02"
			if p.format != "" {
				layout = p.format
			}
			t := in.ModTime
			if t.IsZero() {
				t = now
			}
			val = t.Format(layout)
		case "size":
			if strings.EqualFold(p.format, "bytes") {
				val = strconv.FormatInt(in.Size, 10)
			} else {
				val = formatSize(in.Size)
			}
		case "parent":
			val = in.Parent
		case "rand":
			length := 6
			if p.format != "" {
				if parsed, err := strconv.Atoi(p.format); err == nil && parsed > 0 {
					length = parsed
				}
			}
			val = randomString(length)
		case "title":
			val = in.Title
		case "artist":
			val = in.Artist
		case "track":
			val = in.Track
		case "year":
			val = in.Year
		case "season":
			if season > 0 {
				if p.format != "" {
					if pad, err := strconv.Atoi(p.format); err == nil && pad > 0 {
						val = fmt.Sprintf("%0*d", pad, season)
					} else {
						val = strconv.Itoa(season)
					}
				} else {
					val = strconv.Itoa(season)
				}
			}
		case "episode":
			if episode > 0 {
				if p.format != "" {
					if pad, err := strconv.Atoi(p.format); err == nil && pad > 0 {
						val = fmt.Sprintf("%0*d", pad, episode)
					} else {
						val = strconv.Itoa(episode)
					}
				} else {
					val = strconv.Itoa(episode)
				}
			}
		}

		for _, mod := range p.modifiers {
			val = applyModifier(val, mod)
		}

		b.WriteString(val)
	}

	return b.String()
}

func applyModifier(val string, mod modifier) string {
	switch mod.name {
	case "lower":
		return strings.ToLower(val)
	case "upper":
		return strings.ToUpper(val)
	case "title":
		return applyTitle(val)
	case "slug":
		return applySlug(val)
	case "trim":
		return strings.TrimSpace(val)
	case "replace":
		if len(mod.args) >= 2 {
			return strings.ReplaceAll(val, mod.args[0], mod.args[1])
		}
		if len(mod.args) == 1 {
			return strings.ReplaceAll(val, mod.args[0], "")
		}
	case "regex":
		if len(mod.args) >= 1 {
			pat := mod.args[0]
			repl := ""
			if len(mod.args) >= 2 {
				repl = mod.args[1]
			}
			if len(pat) <= 200 {
				if re, err := regexp.Compile(pat); err == nil {
					capped := val
					if len(capped) > 4096 {
						capped = capped[:4096]
					}
					return re.ReplaceAllString(capped, repl)
				}
			}
		}
	case "cut":
		runes := []rune(val)
		if len(mod.args) >= 1 {
			start, err1 := strconv.Atoi(mod.args[0])
			if err1 == nil {
				if start < 0 {
					start = 0
				}
				if start > len(runes) {
					start = len(runes)
				}
				end := len(runes)
				if len(mod.args) >= 2 {
					if endParsed, err2 := strconv.Atoi(mod.args[1]); err2 == nil {
						if endParsed > len(runes) {
							endParsed = len(runes)
						}
						if endParsed >= start {
							end = endParsed
						}
					}
				}
				return string(runes[start:end])
			}
		}
	case "remove":
		if len(mod.args) >= 1 {
			chars := mod.args[0]
			if strings.HasPrefix(chars, "[") && strings.HasSuffix(chars, "]") && len(chars) >= 2 {
				chars = chars[1 : len(chars)-1]
			}
			return strings.Map(func(r rune) rune {
				if strings.ContainsRune(chars, r) {
					return -1
				}
				return r
			}, val)
		}
	}
	return val
}

func applyTitle(val string) string {
	words := strings.Fields(val)
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			for j := 1; j < len(runes); j++ {
				runes[j] = unicode.ToLower(runes[j])
			}
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func applySlug(val string) string {
	var sb strings.Builder
	for _, r := range val {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
		} else if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "-") {
			sb.WriteRune('-')
		}
	}
	return strings.Trim(sb.String(), "-")
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}

func randomString(length int) string {
	if length <= 0 {
		return ""
	}
	b := make([]byte, (length+1)/2)
	_, _ = rand.Read(b)
	s := hex.EncodeToString(b)
	if len(s) > length {
		return s[:length]
	}
	return s
}

func Sanitize(name, fallback string) (string, string) {
	cleanRaw := func(s string) string {
		s = strings.ReplaceAll(s, "/", " ")
		s = strings.ReplaceAll(s, "\\", " ")
		var b strings.Builder
		for _, r := range s {
			if unicode.IsControl(r) || r < 32 || r == 127 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}

	safeFallback := strings.Trim(cleanRaw(fallback), ". ")
	if safeFallback == "" {
		safeFallback = "file"
	}

	cleaned := cleanRaw(name)
	ext := filepath.Ext(cleaned)
	stem := strings.TrimSpace(strings.TrimSuffix(cleaned, ext))
	cleaned = stem + ext

	if cleaned == "" || strings.Trim(cleaned, ". ") == "" {
		return safeFallback, fmt.Sprintf("invalid name %q fell back to original %q", name, safeFallback)
	}

	if len([]byte(cleaned)) <= 255 {
		return cleaned, ""
	}

	ext = filepath.Ext(cleaned)
	extBytes := len([]byte(ext))
	if extBytes >= 255 {
		runes := []rune(ext)
		for len([]byte(string(runes))) > 255 {
			runes = runes[:len(runes)-1]
		}
		res := string(runes)
		if strings.Trim(res, ". ") == "" {
			return safeFallback, fmt.Sprintf("truncated name fell back to %q", safeFallback)
		}
		return res, "name truncated to 255 bytes"
	}

	stem = strings.TrimSuffix(cleaned, ext)
	maxStemBytes := 255 - extBytes
	for len([]byte(stem)) > maxStemBytes && len(stem) > 0 {
		_, sz := utf8.DecodeLastRuneInString(stem)
		if sz == 0 {
			stem = stem[:len(stem)-1]
		} else {
			stem = stem[:len(stem)-sz]
		}
	}

	result := strings.TrimSpace(stem) + ext
	for len([]byte(result)) > 255 {
		if len(stem) > 0 {
			_, sz := utf8.DecodeLastRuneInString(stem)
			if sz == 0 {
				stem = stem[:len(stem)-1]
			} else {
				stem = stem[:len(stem)-sz]
			}
			result = strings.TrimSpace(stem) + ext
		} else {
			_, sz := utf8.DecodeLastRuneInString(result)
			if sz == 0 {
				result = result[:len(result)-1]
			} else {
				result = result[:len(result)-sz]
			}
		}
	}
	if result == "" || strings.Trim(result, ". ") == "" {
		return safeFallback, fmt.Sprintf("truncated name fell back to %q", safeFallback)
	}
	return result, "name truncated to 255 bytes"
}
