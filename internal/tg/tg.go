package tg

import (
	"context"
	"errors"
	"fmt"
)

type MessageRef struct {
	ChatID int64
	ID     int
}

func (m MessageRef) Valid() bool { return m.ID != 0 }

type Button struct {
	Text string
	Data string
	URL  string
}

type Keyboard [][]Button

type SendOpts struct {
	Keyboard    Keyboard
	ForceReply  bool
	Placeholder string
	ReplyTo     int
	Silent      bool
}

type Kind int

const (
	KindDocument Kind = iota
	KindVideo
	KindAudio
	KindPhoto
)

type ProgressFunc func(current, total int64)

type Upload struct {
	Path      string
	Caption   string
	Kind      Kind
	Thumb     string
	Duration  int
	Width     int
	Height    int
	Performer string
	Title     string
	Progress  ProgressFunc
}

type GroupItem struct {
	Path    string
	Caption string
	Video   bool
	Photo   bool
}

type FileRef struct {
	FileID string
	Name   string
	Size   int64
}

type Command struct{ Name, Description string }

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
	SetChatCommands(ctx context.Context, chatID int64, cmds []Command) error
}

type FloodError struct{ Seconds int }

func (e *FloodError) Error() string { return fmt.Sprintf("telegram flood wait: %ds", e.Seconds) }

type BadRequestError struct{ Message string }

func (e *BadRequestError) Error() string { return "telegram bad request: " + e.Message }

func IsFlood(err error) (int, bool) {
	var fe *FloodError
	if errors.As(err, &fe) {
		return fe.Seconds, true
	}
	return 0, false
}

func IsBadRequest(err error) (string, bool) {
	var be *BadRequestError
	if errors.As(err, &be) {
		return be.Message, true
	}
	return "", false
}

var ErrAmbiguousUpload = errors.New("telegram upload failed after request body was sent; state ambiguous")
