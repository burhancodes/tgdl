package rename

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/burhanverse/tgdl/internal/fsutil"
)

type Entry struct {
	OldName string          `json:"old_name"`
	NewName fsutil.FileName `json:"new_name"`
	Notes   []string        `json:"notes,omitempty"`
}

func Name(typed, originalFilename string) (fsutil.FileName, []string, error) {
	if strings.TrimSpace(typed) == "" {
		return fsutil.FileName{}, nil, errors.New("name cannot be empty")
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
		return fsutil.FileName{}, nil, errors.New("name cannot be empty")
	}

	origExt := filepath.Ext(originalFilename)
	typedExt := filepath.Ext(trimmed)

	finalDisplay := trimmed
	if typedExt == "" {
		if origExt != "" {
			finalDisplay = trimmed + origExt
		}
	} else if strings.EqualFold(typedExt, origExt) {
		finalDisplay = trimmed
	} else {
		finalDisplay = trimmed
	}

	if strings.TrimSpace(finalDisplay) == "" {
		return fsutil.FileName{}, nil, errors.New("name cannot be empty")
	}

	fn := fsutil.NewFileName(finalDisplay)
	return fn, notes, nil
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
		fn, notes, nErr := Name(typed, orig)
		if nErr != nil {
			return nil, nErr
		}

		key := strings.ToLower(fn.Display)
		if existing, ok := usedCounts[key]; ok {
			usedCounts[key] = existing + 1
			suffix := fmt.Sprintf(" (%d)", existing+1)
			ext := filepath.Ext(fn.Display)
			stem := strings.TrimSuffix(fn.Display, ext)
			newDisplay := stem + suffix + ext
			notes = append(notes, fmt.Sprintf("duplicate name, added '%s'", suffix))
			fn = fsutil.NewFileName(newDisplay)
			usedCounts[strings.ToLower(newDisplay)] = 1
		} else {
			usedCounts[key] = 1
		}

		entries = append(entries, Entry{
			OldName: orig,
			NewName: fn,
			Notes:   notes,
		})
	}

	return entries, err
}
