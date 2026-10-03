// Package tg defines the narrow Telegram surface the rest of the bot depends
// on. Keeping it an interface isolates the MTProto/Bot API library choice to
// one adapter (see botapi.go) and makes the job manager testable with a fake.
package tg

import (
	"context"
	"errors"
	"fmt"
)

// MessageRef identifies a sent message.
type MessageRef struct {
	ChatID int64
	ID     int
}

// Valid reports whether the ref points at a real message.
func (m MessageRef) Valid() bool { return m.ID != 0 }

// Button is one inline keyboard button carrying callback data.
type Button struct{ Text, Data string }

// Keyboard is rows of inline buttons.
type Keyboard [][]Button

// SendOpts are optional message parameters. Text is always HTML.
type SendOpts struct {
	Keyboard    Keyboard
	ForceReply  bool
	Placeholder string
	ReplyTo     int
	Silent      bool
}

// Kind selects the Telegram media method for an upload.
type Kind int

const (
	KindDocument Kind = iota
	KindVideo
	KindAudio
	KindPhoto
)

// ProgressFunc reports bytes transferred so far and the total.
type ProgressFunc func(current, total int64)

// Upload describes one file upload.
type Upload struct {
	Path      string
	Caption   string // HTML
	Kind      Kind
	Thumb     string
	Duration  int
	Width     int
	Height    int
	Performer string
	Title     string
	Progress  ProgressFunc
}

// GroupItem is one member of an album.
type GroupItem struct {
	Path    string
	Caption string // HTML; Telegram shows only the first caption for photos
	Video   bool
	Photo   bool
}

// FileRef identifies a Telegram-hosted file.
type FileRef struct {
	FileID string
	Name   string
	Size   int64
}

// Command is a bot menu entry.
type Command struct{ Name, Description string }

// Client is the Telegram capability set used by the bot.
type Client interface {
	Send(ctx context.Context, chatID int64, html string, o SendOpts) (MessageRef, error)
	Edit(ctx context.Context, ref MessageRef, html string, o SendOpts) error
	Delete(ctx context.Context, ref MessageRef) error
	Pin(ctx context.Context, ref MessageRef) error
	Unpin(ctx context.Context, ref MessageRef) error
	Answer(ctx context.Context, callbackID, text string, alert bool) error
	Upload(ctx context.Context, chatID int64, u Upload) error
	UploadGroup(ctx context.Context, chatID int64, items []GroupItem) error
	Download(ctx context.Context, f FileRef, destDir string, progress ProgressFunc) (string, error)
	SetCommands(ctx context.Context, cmds []Command) error
}

// FloodError is returned when Telegram asks the caller to wait.
type FloodError struct{ Seconds int }

func (e *FloodError) Error() string { return fmt.Sprintf("telegram flood wait: %ds", e.Seconds) }

// BadRequestError wraps a non-retryable Telegram 400.
type BadRequestError struct{ Message string }

func (e *BadRequestError) Error() string { return "telegram bad request: " + e.Message }

// IsFlood extracts the wait from err.
func IsFlood(err error) (int, bool) {
	var fe *FloodError
	if errors.As(err, &fe) {
		return fe.Seconds, true
	}
	return 0, false
}

// IsBadRequest reports whether err is a bad-request error and returns its text.
func IsBadRequest(err error) (string, bool) {
	var be *BadRequestError
	if errors.As(err, &be) {
		return be.Message, true
	}
	return "", false
}
