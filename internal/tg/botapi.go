package tg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BotAPI implements Client on top of github.com/go-telegram/bot. Point
// TG_BOT_API_URL at a self-hosted `telegram-bot-api --local` server to lift
// the cloud limits (2000 MB uploads and downloads).
type BotAPI struct {
	B     *bot.Bot
	Token string
	http  *http.Client
}

// NewBotAPI wraps an initialised bot.
func NewBotAPI(b *bot.Bot, token string) *BotAPI {
	return &BotAPI{B: b, Token: token, http: &http.Client{}}
}

func btrue() *bool { v := true; return &v }

func markup(o SendOpts) models.ReplyMarkup {
	if o.ForceReply {
		return &models.ForceReply{ForceReply: true, InputFieldPlaceholder: o.Placeholder, Selective: true}
	}
	if len(o.Keyboard) == 0 {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, len(o.Keyboard))
	for i, r := range o.Keyboard {
		for _, b := range r {
			btn := models.InlineKeyboardButton{Text: b.Text}
			if b.URL != "" {
				btn.URL = b.URL
			} else {
				btn.CallbackData = b.Data
			}
			rows[i] = append(rows[i], btn)
		}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if bot.IsTooManyRequestsError(err) {
		var te *bot.TooManyRequestsError
		if errors.As(err, &te) {
			return &FloodError{Seconds: te.RetryAfter}
		}
		return &FloodError{Seconds: 5}
	}
	msg := err.Error()
	if strings.Contains(msg, "Bad Request") || strings.Contains(msg, "bad request") {
		return &BadRequestError{Message: msg}
	}
	return err
}

func (a *BotAPI) Send(ctx context.Context, chatID int64, html string, o SendOpts) (MessageRef, error) {
	p := &bot.SendMessageParams{
		ChatID: chatID, Text: html, ParseMode: models.ParseModeHTML,
		ReplyMarkup:         markup(o),
		LinkPreviewOptions:  &models.LinkPreviewOptions{IsDisabled: btrue()},
		DisableNotification: o.Silent,
	}
	if o.ReplyTo != 0 {
		p.ReplyParameters = &models.ReplyParameters{MessageID: o.ReplyTo}
	}
	m, err := a.B.SendMessage(ctx, p)
	if err != nil {
		return MessageRef{}, mapErr(err)
	}
	return MessageRef{ChatID: chatID, ID: m.ID}, nil
}

func (a *BotAPI) Edit(ctx context.Context, ref MessageRef, html string, o SendOpts) error {
	p := &bot.EditMessageTextParams{
		ChatID: ref.ChatID, MessageID: ref.ID, Text: html, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: btrue()},
	}
	if mk := markup(o); mk != nil {
		p.ReplyMarkup = mk
	}
	_, err := a.B.EditMessageText(ctx, p)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return mapErr(err)
}

func (a *BotAPI) Delete(ctx context.Context, ref MessageRef) error {
	_, err := a.B.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: ref.ChatID, MessageID: ref.ID})
	return mapErr(err)
}

func (a *BotAPI) Pin(ctx context.Context, ref MessageRef) error {
	_, err := a.B.PinChatMessage(ctx, &bot.PinChatMessageParams{ChatID: ref.ChatID, MessageID: ref.ID, DisableNotification: true})
	return mapErr(err)
}

func (a *BotAPI) Unpin(ctx context.Context, ref MessageRef) error {
	_, err := a.B.UnpinChatMessage(ctx, &bot.UnpinChatMessageParams{ChatID: ref.ChatID, MessageID: ref.ID})
	return mapErr(err)
}

func (a *BotAPI) Answer(ctx context.Context, id, text string, alert bool) error {
	_, err := a.B.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: alert})
	return mapErr(err)
}

// progressReader counts bytes as the HTTP client streams the multipart body.
type progressReader struct {
	f     *os.File
	total int64
	read  atomic.Int64
	cb    ProgressFunc
	ctx   context.Context
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.f.Read(b)
	if n > 0 && p.cb != nil {
		p.cb(p.read.Add(int64(n)), p.total)
	}
	return n, err
}

func openUpload(ctx context.Context, path string, cb ProgressFunc) (*progressReader, *models.InputFileUpload, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	pr := &progressReader{f: f, total: st.Size(), cb: cb, ctx: ctx}
	return pr, &models.InputFileUpload{Filename: filepath.Base(path), Data: pr}, nil
}

func thumbFile(path string) (models.InputFile, *os.File) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	return &models.InputFileUpload{Filename: filepath.Base(path), Data: f}, f
}

func (a *BotAPI) Upload(ctx context.Context, chatID int64, u Upload) error {
	pr, file, err := openUpload(ctx, u.Path, u.Progress)
	if err != nil {
		return err
	}
	defer pr.f.Close()
	thumb, tf := thumbFile(u.Thumb)
	if tf != nil {
		defer tf.Close()
	}
	switch u.Kind {
	case KindVideo:
		_, err = a.B.SendVideo(ctx, &bot.SendVideoParams{ChatID: chatID, Video: file, Caption: u.Caption, ParseMode: models.ParseModeHTML,
			Duration: u.Duration, Width: u.Width, Height: u.Height, SupportsStreaming: true, Thumbnail: thumb, DisableNotification: true})
	case KindAudio:
		_, err = a.B.SendAudio(ctx, &bot.SendAudioParams{ChatID: chatID, Audio: file, Caption: u.Caption, ParseMode: models.ParseModeHTML,
			Duration: u.Duration, Performer: u.Performer, Title: u.Title, Thumbnail: thumb, DisableNotification: true})
	case KindPhoto:
		_, err = a.B.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: file, Caption: u.Caption, ParseMode: models.ParseModeHTML, DisableNotification: true})
	default:
		_, err = a.B.SendDocument(ctx, &bot.SendDocumentParams{ChatID: chatID, Document: file, Caption: u.Caption, ParseMode: models.ParseModeHTML,
			Thumbnail: thumb, DisableContentTypeDetection: true, DisableNotification: true})
	}
	return mapErr(err)
}

func (a *BotAPI) UploadGroup(ctx context.Context, chatID int64, items []GroupItem) error {
	if len(items) < 2 || len(items) > 10 {
		return fmt.Errorf("album needs 2-10 items, got %d", len(items))
	}
	var media []models.InputMedia
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	for i, it := range items {
		f, err := os.Open(it.Path)
		if err != nil {
			return err
		}
		files = append(files, f)
		name := fmt.Sprintf("file%d", i)
		switch {
		case it.Video:
			media = append(media, &models.InputMediaVideo{Media: "attach://" + name, MediaAttachment: f, Caption: it.Caption, ParseMode: models.ParseModeHTML, SupportsStreaming: true})
		case it.Photo:
			media = append(media, &models.InputMediaPhoto{Media: "attach://" + name, MediaAttachment: f, Caption: it.Caption, ParseMode: models.ParseModeHTML})
		default:
			media = append(media, &models.InputMediaDocument{Media: "attach://" + name, MediaAttachment: f, Caption: it.Caption, ParseMode: models.ParseModeHTML})
		}
	}
	_, err := a.B.SendMediaGroup(ctx, &bot.SendMediaGroupParams{ChatID: chatID, Media: media, DisableNotification: true})
	return mapErr(err)
}

// Download fetches a Telegram file into destDir. Against a --local Bot API
// server getFile returns an absolute path on the server's disk; when that path
// is visible to us the file is moved/copied instead of re-downloaded.
func (a *BotAPI) Download(ctx context.Context, f FileRef, destDir string, cb ProgressFunc) (string, error) {
	info, err := a.B.GetFile(ctx, &bot.GetFileParams{FileID: f.FileID})
	if err != nil {
		return "", mapErr(err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	name := f.Name
	if name == "" {
		name = filepath.Base(info.FilePath)
	}
	dest := filepath.Join(destDir, filepath.Base(name))
	total := f.Size
	if total == 0 {
		total = info.FileSize
	}

	var src io.ReadCloser
	if filepath.IsAbs(info.FilePath) {
		if s, err := os.Open(info.FilePath); err == nil {
			src = s
		}
	}
	if src == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.B.FileDownloadLink(info), nil)
		if err != nil {
			return "", err
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
		}
		if total == 0 {
			total = resp.ContentLength
		}
		src = resp.Body
	}
	defer src.Close()

	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				_ = out.Close()
				_ = os.Remove(part)
				return "", werr
			}
			done += int64(n)
			if cb != nil {
				cb(done, total)
			}
		}
		if ctx.Err() != nil {
			_ = out.Close()
			_ = os.Remove(part)
			return "", ctx.Err()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			_ = os.Remove(part)
			return "", rerr
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (a *BotAPI) SetCommands(ctx context.Context, cmds []Command) error {
	bc := make([]models.BotCommand, len(cmds))
	for i, c := range cmds {
		bc[i] = models.BotCommand{Command: c.Name, Description: c.Description}
	}
	_, err := a.B.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: bc})
	return mapErr(err)
}
