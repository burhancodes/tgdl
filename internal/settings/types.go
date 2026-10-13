package settings

import (
	"context"
	"strings"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

type ApplyMode int

const (
	ApplyLive ApplyMode = iota
	ApplyRestart
	ApplyReadOnly
)

func (m ApplyMode) String() string {
	switch m {
	case ApplyLive:
		return "Live"
	case ApplyRestart:
		return "Restart required"
	case ApplyReadOnly:
		return "Read-only"
	default:
		return "Unknown"
	}
}

type ValueSource string

const (
	SourceDefault  ValueSource = "default"
	SourceEnv      ValueSource = "env"
	SourceOverride ValueSource = "bot override"
)

const (
	GroupGeneral     = "General"
	GroupTelegram    = "Telegram"
	GroupLimits      = "Limits & concurrency"
	GroupStorage     = "Storage & disk"
	GroupArchives    = "Archives"
	GroupDownloaders = "Downloaders"
	GroupUploadHosts = "Upload hosts"
	GroupCloud       = "Cloud (Drive/MEGA)"
	GroupTorrents    = "Torrents/Search"
	GroupLogging     = "Logging"
	GroupSecurity    = "Security"
)

var AllGroups = []string{
	GroupGeneral,
	GroupTelegram,
	GroupLimits,
	GroupStorage,
	GroupArchives,
	GroupDownloaders,
	GroupUploadHosts,
	GroupCloud,
	GroupTorrents,
	GroupLogging,
	GroupSecurity,
}

type Hook func(ctx context.Context, cfg *config.Config, key, val string) error

type Spec struct {
	Key         string
	Group       string
	Description string
	Type        string
	Default     string
	Enum        []string
	Min         int64
	Max         int64
	Secret      bool
	Apply       ApplyMode
	Validate    func(string) error
	OnChange    func(*config.Config, string) error
}

type ChangeRecord struct {
	Timestamp time.Time `json:"timestamp"`
	UserID    int64     `json:"user_id"`
	Key       string    `json:"key"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	Source    string    `json:"source"`
}

type Diff struct {
	Key      string
	OldVal   string
	NewVal   string
	IsSecret bool
}

func MaskValue(val string, isSecret bool) string {
	if !isSecret {
		return val
	}
	if val == "" {
		return ""
	}
	if len(val) < 12 {
		return "••••••••"
	}
	return "••••" + val[len(val)-4:]
}

func IsDangerous(spec *Spec, newVal string) bool {
	if spec == nil {
		return false
	}
	if spec.Group == GroupSecurity {
		return true
	}
	if spec.Key == "AUTHORIZED_USER_IDS" {
		return true
	}
	if spec.Secret {
		return true
	}
	if spec.Key == "MIN_FREE_DISK_BYTES" {
		v, err := ParseBytes(newVal)
		if err == nil && v == 0 {
			return true
		}
	}
	return false
}

func CleanKey(k string) string {
	return strings.ToUpper(strings.TrimSpace(k))
}
