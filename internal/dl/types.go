package dl

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
)

type Progress struct {
	Current int64
	Total   int64
	File    string
	URL     string
}

type ProgressFunc func(Progress)

type CountFunc func(count int, file, url string)

type Result struct {
	OK        bool
	Files     []string
	ErrorTail string
}

type ProcRegistry interface {
	Register(cmd *exec.Cmd)
	Unregister()
}

type NopProcs struct{}

func (NopProcs) Register(*exec.Cmd) {}
func (NopProcs) Unregister()        {}

type Args struct {
	UserID           json.Number    `json:"user_id,omitempty"`
	Engine           string         `json:"engine,omitempty"`
	Password         string         `json:"password,omitempty"`
	ExtraArgs        []string       `json:"extra_args,omitempty"`
	ArchiveFormat    string         `json:"archive_format,omitempty"`
	MirrorPixeldrain bool           `json:"mirror_pixeldrain,omitempty"`
	IsMirror         bool           `json:"is_mirror,omitempty"`
	UploadTG         bool           `json:"upload_tg,omitempty"`
	Unzip            bool           `json:"unzip,omitempty"`
	AsDoc            bool           `json:"as_doc,omitempty"`
	ReplyMessageID   int            `json:"reply_message_id,omitempty"`
	AriaOptions      map[string]any `json:"aria_options,omitempty"`
	TGFile           *TGFile        `json:"tg_file,omitempty"`
	RenameScheme     string         `json:"rename_scheme,omitempty"`
	RenameNames      []string       `json:"rename_names,omitempty"`
	RenameFiles      []TGFile       `json:"rename_files,omitempty"`
	MirrorHosts      []string       `json:"mirror_hosts,omitempty"`
}

type TGFile struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
}

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

func (a Args) UserIDInt() int64 {
	n, err := a.UserID.Int64()
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

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

func FirstURL(raw string) string {
	raw = strings.TrimSpace(raw)
	prefix := ""
	for _, p := range []string{"mirror:", "direct:", "cdl:", "cyberdrop-dl:", "gdrive:", "gd2tg:", "gofile:"} {
		if strings.HasPrefix(raw, p) {
			prefix = p
			raw = strings.TrimPrefix(raw, p)
			break
		}
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil && len(list) > 0 {
			return prefix + list[0]
		}
	}
	return prefix + raw
}

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

func (a Args) Display() string {
	parts := append([]string(nil), a.ExtraArgs...)
	if a.Password != "" {
		parts = append(parts, "--password ****")
	}
	if a.RenameScheme != "" {
		parts = append(parts, "scheme: "+a.RenameScheme)
	}
	return strings.Join(parts, " ")
}
