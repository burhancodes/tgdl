// Package dl contains the download engines and shared types.
package dl

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
)

// Progress is a snapshot reported by byte-oriented downloaders.
type Progress struct {
	Current int64
	Total   int64
	File    string
	URL     string
}

// ProgressFunc receives progress snapshots. Implementations must be cheap and
// must not block; they are invoked from the download goroutine.
type ProgressFunc func(Progress)

// CountFunc receives progress from subprocess-based downloaders that only know
// how many files have completed.
type CountFunc func(count int, file, url string)

// Result is the outcome of a download attempt.
type Result struct {
	OK        bool
	Files     []string
	ErrorTail string
}

// ProcRegistry lets the queue manager kill a running subprocess on cancel.
type ProcRegistry interface {
	Register(cmd *exec.Cmd)
	Unregister()
}

// NopProcs is a ProcRegistry that ignores registrations.
type NopProcs struct{}

func (NopProcs) Register(*exec.Cmd) {}
func (NopProcs) Unregister()        {}

// Args mirrors the JSON blob persisted in jobs.args.
type Args struct {
	UserID          json.Number    `json:"user_id,omitempty"`
	Engine          string         `json:"engine,omitempty"`
	Password        string         `json:"password,omitempty"`
	ExtraArgs       []string       `json:"extra_args,omitempty"`
	ArchiveFormat   string         `json:"archive_format,omitempty"`
	MirrorPixeldrain bool          `json:"mirror_pixeldrain,omitempty"`
	IsMirror        bool           `json:"is_mirror,omitempty"`
	UploadTG        bool           `json:"upload_tg,omitempty"`
	Unzip           bool           `json:"unzip,omitempty"`
	AsDoc           bool           `json:"as_doc,omitempty"`
	ReplyMessageID  int            `json:"reply_message_id,omitempty"`
	TargetURL       string         `json:"target_url,omitempty"`
	OriginalFilename string        `json:"original_filename,omitempty"`
	AriaOptions     map[string]any `json:"aria_options,omitempty"`
	// TGFile references a Telegram-hosted file (replied media) for unzip, patch
	// and mirror-of-Telegram-file jobs. The Bot API cannot fetch arbitrary
	// messages by id, so the handler captures the file reference up front.
	TGFile *TGFile `json:"tg_file,omitempty"`
}

// TGFile is a serialisable Telegram file reference.
type TGFile struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
}

// ParseArgs decodes a job's args JSON. Legacy list-form args (a bare list of
// extra CLI arguments) are accepted too. Invalid input yields zero Args.
func ParseArgs(raw string) Args {
	var a Args
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return a
	}
	if strings.HasPrefix(raw, "[") {
		var list []any
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, v := range list {
				a.ExtraArgs = append(a.ExtraArgs, toString(v))
			}
		}
		return a
	}
	_ = json.Unmarshal([]byte(raw), &a)
	return a
}

// UserIDInt returns the positive integer user id, or 0.
func (a Args) UserIDInt() int64 {
	n, err := a.UserID.Int64()
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// CLIExtra returns password/extra args in the form passed to subprocess tools.
func (a Args) CLIExtra() []string {
	var out []string
	if a.Password != "" {
		out = append(out, "--password", a.Password)
	}
	return append(out, a.ExtraArgs...)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// FirstURL unwraps a JSON-array job URL to its first element.
func FirstURL(raw string) string {
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil && len(list) > 0 {
			return list[0]
		}
	}
	return raw
}

// Canceller is a small helper shared by engines to observe cancellation.
type Canceller struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (c *Canceller) Set(f context.CancelFunc) { c.mu.Lock(); c.cancel = f; c.mu.Unlock() }
func (c *Canceller) Cancel() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
}

// Display returns a short human string of user-visible extra arguments.
func (a Args) Display() string {
	parts := append([]string(nil), a.ExtraArgs...)
	if a.Password != "" {
		parts = append(parts, "--password ****")
	}
	return strings.Join(parts, " ")
}
