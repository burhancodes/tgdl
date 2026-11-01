package bot

import (
	"github.com/burhanverse/tgdl/internal/dl"
)

type User struct {
	ID    int64
	IsBot bool
}

type Chat struct {
	ID   int64
	Type string
}

func (c Chat) IsPrivate() bool {
	return c.Type == "private" || (c.Type == "" && c.ID > 0)
}

func (c Chat) IsGroup() bool {
	return c.Type == "group" || c.Type == "supergroup" || (c.Type == "" && c.ID < 0)
}

func (c Chat) IsChannel() bool {
	return c.Type == "channel"
}

type Document struct {
	FileID   string
	FileName string
	FileSize int64
}

type Video struct {
	FileID   string
	FileName string
	FileSize int64
	Width    int
	Height   int
	Duration int
}

type Audio struct {
	FileID    string
	FileName  string
	FileSize  int64
	Duration  int
	Performer string
	Title     string
}

type PhotoSize struct {
	FileID   string
	FileSize int
}

type Message struct {
	ID             int
	Chat           Chat
	From           *User
	Text           string
	Caption        string
	ReplyToMessage *Message
	PinnedMessage  *Message
	NewChatMembers bool
	LeftChatMember bool
	Document       *Document
	Video          *Video
	Audio          *Audio
	Photo          []*PhotoSize
	File           *dl.TGFile
}

type CallbackQuery struct {
	ID      string
	From    User
	Message *Message
	Data    string
}

type Update struct {
	Message       *Message
	CallbackQuery *CallbackQuery
}
