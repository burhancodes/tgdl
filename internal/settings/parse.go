package settings

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/burhanverse/tgdl/internal/upload"
)

func ParseBytes(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("size cannot be negative: %s", raw)
		}
		return n, nil
	}
	unitIdx := len(s)
	for i, r := range s {
		if !unicode.IsDigit(r) && r != '.' {
			unitIdx = i
			break
		}
	}
	numStr := strings.TrimSpace(s[:unitIdx])
	unitStr := strings.ToUpper(strings.TrimSpace(s[unitIdx:]))

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value: %s", raw)
	}
	if val < 0 {
		return 0, fmt.Errorf("size cannot be negative: %s", raw)
	}

	var mult float64
	switch unitStr {
	case "", "B", "BYTES":
		mult = 1
	case "K", "KB", "KIB":
		mult = 1024
	case "M", "MB", "MIB":
		mult = 1024 * 1024
	case "G", "GB", "GIB":
		mult = 1024 * 1024 * 1024
	case "T", "TB", "TIB":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit %q in %s", unitStr, raw)
	}

	res := int64(val * mult)
	return res, nil
}

func ParseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		if secs < 0 {
			return 0, errors.New("duration cannot be negative")
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid duration: %s", raw)
}

func ParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q (must be true/false/yes/no)", raw)
	}
}

func ParseIDList(raw string) ([]int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID %q: %w", part, err)
		}
		if id == 0 {
			return nil, errors.New("user ID cannot be 0")
		}
		out = append(out, id)
	}
	return out, nil
}

func ValidateIntRange(min, max int64) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return fmt.Errorf("must be an integer: %w", err)
		}
		if v < min || (max > 0 && v > max) {
			if max > 0 {
				return fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
			}
			return fmt.Errorf("value %d must be at least %d", v, min)
		}
		return nil
	}
}

func ValidateFloatRange(min, max float64) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("must be a float: %w", err)
		}
		if v < min || (max > 0 && v > max) {
			if max > 0 {
				return fmt.Errorf("value %g out of range [%g, %g]", v, min, max)
			}
			return fmt.Errorf("value %g must be at least %g", v, min)
		}
		return nil
	}
}

func ValidateSizeRange(min, max int64) func(string) error {
	return func(s string) error {
		v, err := ParseBytes(s)
		if err != nil {
			return err
		}
		if v < min || (max > 0 && v > max) {
			if max > 0 {
				return fmt.Errorf("size %d bytes out of range [%d, %d]", v, min, max)
			}
			return fmt.Errorf("size %d bytes must be at least %d", v, min)
		}
		return nil
	}
}

func ValidateDurationRange(min, max time.Duration) func(string) error {
	return func(s string) error {
		d, err := ParseDuration(s)
		if err != nil {
			return err
		}
		if d < min || (max > 0 && d > max) {
			if max > 0 {
				return fmt.Errorf("duration %v out of range [%v, %v]", d, min, max)
			}
			return fmt.Errorf("duration %v must be at least %v", d, min)
		}
		return nil
	}
}

func ValidateEnumValues(allowed []string) func(string) error {
	return func(s string) error {
		clean := strings.TrimSpace(s)
		for _, a := range allowed {
			if strings.EqualFold(a, clean) {
				return nil
			}
		}
		return fmt.Errorf("invalid value %q, allowed: %s", clean, strings.Join(allowed, ", "))
	}
}

func ValidatePath(s string) error {
	clean := filepath.Clean(strings.TrimSpace(s))
	if clean == "" || clean == "." {
		return errors.New("path cannot be empty")
	}
	return nil
}

func ValidateBoolValue(s string) error {
	_, err := ParseBool(s)
	return err
}

func ValidateIDListValue(s string) error {
	_, err := ParseIDList(s)
	return err
}

func ValidateMirrorHosts(s string) error {
	_, err := upload.ParseHosts(s)
	return err
}

func FormatValue(spec *Spec, raw string) string {
	if raw == "" {
		return ""
	}
	if spec == nil {
		return raw
	}
	switch spec.Type {
	case "bool":
		b, err := ParseBool(raw)
		if err == nil {
			if b {
				return "true"
			}
			return "false"
		}
	case "size":
		b, err := ParseBytes(raw)
		if err == nil {
			if b == 0 {
				return "0"
			}
			if b >= 1024*1024*1024 && b%(1024*1024*1024) == 0 {
				return fmt.Sprintf("%dG", b/(1024*1024*1024))
			}
			if b >= 1024*1024 && b%(1024*1024) == 0 {
				return fmt.Sprintf("%dM", b/(1024*1024))
			}
			if b >= 1024 && b%1024 == 0 {
				return fmt.Sprintf("%dK", b/1024)
			}
			return strconv.FormatInt(b, 10)
		}
	case "duration":
		d, err := ParseDuration(raw)
		if err == nil {
			return d.String()
		}
	case "enum":
		for _, e := range spec.Enum {
			if strings.EqualFold(e, raw) {
				return e
			}
		}
	}
	return raw
}

func ValuesEqual(spec *Spec, a, b string) bool {
	if a == b {
		return true
	}
	if spec == nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	switch spec.Type {
	case "bool":
		ba, erra := ParseBool(a)
		bb, errb := ParseBool(b)
		if erra == nil && errb == nil {
			return ba == bb
		}
	case "size":
		sa, erra := ParseBytes(a)
		sb, errb := ParseBytes(b)
		if erra == nil && errb == nil {
			return sa == sb
		}
	case "duration":
		da, erra := ParseDuration(a)
		db, errb := ParseDuration(b)
		if erra == nil && errb == nil {
			return da == db
		}
	case "idlist":
		ia, erra := ParseIDList(a)
		ib, errb := ParseIDList(b)
		if erra == nil && errb == nil {
			return slices.Equal(ia, ib)
		}
	case "enum":
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}
