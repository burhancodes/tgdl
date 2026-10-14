package rename

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Entry struct {
	OldName string   `json:"old_name"`
	NewName string   `json:"new_name"`
	Notes   []string `json:"notes,omitempty"`
}

func Name(typed, originalFilename string) (string, []string, error) {
	if strings.TrimSpace(typed) == "" {
		return "", nil, errors.New("name cannot be empty")
	}

	var notes []string

	hasSlash := strings.Contains(typed, "/")
	hasBackslash := strings.Contains(typed, "\\")
	hasControl := false

	var b strings.Builder
	for _, r := range typed {
		if r == '/' || r == '\\' {
			b.WriteRune('_')
		} else if r < 32 || r == 127 {
			hasControl = true
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	cleaned := b.String()

	if hasSlash {
		notes = append(notes, "changed '/' to '_'")
	}
	if hasBackslash {
		notes = append(notes, "changed '\\' to '_'")
	}
	if hasControl {
		notes = append(notes, "replaced control characters with '_'")
	}

	trimmed := strings.Trim(cleaned, " .")
	if trimmed == "" {
		return "", nil, errors.New("name cannot be empty")
	}

	origExt := filepath.Ext(originalFilename)
	typedExt := filepath.Ext(trimmed)

	finalName := trimmed
	if typedExt == "" {
		if origExt != "" {
			finalName = trimmed + origExt
		}
	} else if strings.EqualFold(typedExt, origExt) {
		finalName = trimmed
	} else {
		finalName = trimmed
	}

	if len([]byte(finalName)) > 255 {
		ext := filepath.Ext(finalName)
		stem := strings.TrimSuffix(finalName, ext)
		maxStemBytes := 255 - len([]byte(ext))
		if maxStemBytes < 1 {
			maxStemBytes = 255
			ext = ""
			stem = finalName
		}
		cutStem := truncateUTF8(stem, maxStemBytes)
		cutStem = strings.TrimRight(cutStem, " .")
		if cutStem == "" {
			return "", nil, errors.New("name exceeds maximum length")
		}
		finalName = cutStem + ext
		notes = append(notes, "truncated to 255 bytes")
	}

	if strings.TrimSpace(finalName) == "" {
		return "", nil, errors.New("name cannot be empty")
	}

	return finalName, notes, nil
}

func truncateUTF8(s string, maxBytes int) string {
	if len([]byte(s)) <= maxBytes {
		return s
	}
	b := []byte(s)
	if maxBytes > len(b) {
		maxBytes = len(b)
	}
	for maxBytes > 0 && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes])
}

func PlanBatch(names []string, originals []string) ([]Entry, error) {
	var err error
	if len(names) != len(originals) {
		err = fmt.Errorf("line count mismatch: got %d names for %d files", len(names), len(originals))
	}

	count := len(names)
	if len(originals) < count {
		count = len(originals)
	}

	usedCounts := make(map[string]int)
	entries := make([]Entry, 0, count)

	for i := 0; i < count; i++ {
		orig := originals[i]
		typed := names[i]
		finalName, notes, nErr := Name(typed, orig)
		if nErr != nil {
			return nil, nErr
		}

		key := strings.ToLower(finalName)
		if existing, ok := usedCounts[key]; ok {
			usedCounts[key] = existing + 1
			suffix := fmt.Sprintf(" (%d)", existing+1)
			ext := filepath.Ext(finalName)
			stem := strings.TrimSuffix(finalName, ext)
			finalName = stem + suffix + ext
			notes = append(notes, fmt.Sprintf("duplicate name, added '%s'", suffix))
			usedCounts[strings.ToLower(finalName)] = 1
		} else {
			usedCounts[key] = 1
		}

		entries = append(entries, Entry{
			OldName: orig,
			NewName: finalName,
			Notes:   notes,
		})
	}

	return entries, err
}
