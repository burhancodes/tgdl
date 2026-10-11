package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BotAPI implements Client on top of github.com/go-telegram/bot. Point
// TG_BOT_API_URL at a self-hosted `telegram-bot-api --local` server to lift
// the cloud limits (2000 MB uploads and downloads).
type BotAPI struct {
	B            *bot.Bot
	Token        string
	BaseURL      string
	http         *http.Client
	uploadClient *http.Client
	localDirMap  *LocalDirMap
	dataDir      string
}

// LocalDirMap translates local host file paths to paths visible by a local
// telegram-bot-api server container sharing the downloads volume.
type LocalDirMap struct {
	HostPrefix      string
	ContainerPrefix string
}

// FileURL checks if localPath falls under HostPrefix, and if so, returns
// the corresponding file:/// URL as seen by the container.
func (m *LocalDirMap) FileURL(localPath string) (string, bool) {
	if m == nil || m.HostPrefix == "" || m.ContainerPrefix == "" {
		return "", false
	}
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		absPath = localPath
	}
	absPath = filepath.Clean(absPath)

	absHost, err := filepath.Abs(m.HostPrefix)
	if err != nil {
		absHost = m.HostPrefix
	}
	absHost = filepath.Clean(absHost)

	rel, err := filepath.Rel(absHost, absPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	var srvPath string
	if rel == "." {
		srvPath = m.ContainerPrefix
	} else {
		srvPath = filepath.Join(m.ContainerPrefix, rel)
	}
	return "file:///" + strings.TrimPrefix(filepath.ToSlash(srvPath), "/"), true
}

func (a *BotAPI) SetLocalDirMap(hostPrefix, containerPrefix string) {
	a.localDirMap = &LocalDirMap{
		HostPrefix:      hostPrefix,
		ContainerPrefix: containerPrefix,
	}
}

func (a *BotAPI) SetLocalDirMapRaw(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		a.localDirMap = nil
		return nil
	}
	parts := strings.SplitN(raw, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid TG_BOT_API_LOCAL_DIR_MAP %q: expected hostprefix=containerprefix", raw)
	}
	h, c := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if h == "" || c == "" {
		return fmt.Errorf("invalid TG_BOT_API_LOCAL_DIR_MAP %q: empty prefix", raw)
	}
	a.SetLocalDirMap(h, c)
	return nil
}

// NewBotAPI wraps an initialised bot.
func NewBotAPI(b *bot.Bot, token string) *BotAPI {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 0
	return &BotAPI{
		B:            b,
		Token:        token,
		BaseURL:      "https://api.telegram.org",
		http:         &http.Client{},
		uploadClient: &http.Client{Transport: tr, Timeout: 0},
	}
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

type filePart struct {
	fieldName string
	fileName  string
	reader    io.Reader
	size      int64
}

type bufferSwitch struct {
	target *bytes.Buffer
}

func (s *bufferSwitch) Write(p []byte) (int, error) {
	return s.target.Write(p)
}

type bodyTracker struct {
	r     io.Reader
	total int64
	read  atomic.Int64
}

func (t *bodyTracker) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.read.Add(int64(n))
	}
	return n, err
}

func (t *bodyTracker) fullySent() bool {
	return t.total > 0 && t.read.Load() >= t.total
}

func (a *BotAPI) postMultipart(ctx context.Context, method string, fields map[string]string, files []filePart, out any) error {
	baseURL := a.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	reqURL := fmt.Sprintf("%s/bot%s/%s", baseURL, a.Token, method)

	var totalFileSize int64
	for _, f := range files {
		totalFileSize += f.size
	}
	uploadTimeout := 15*time.Minute + time.Duration(totalFileSize/(512*1024))*time.Second
	uploadCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	var (
		readers     []io.Reader
		totalLength int64
		prefixBuf   bytes.Buffer
		suffixBuf   bytes.Buffer
		betweenBufs = make([]bytes.Buffer, max(0, len(files)-1))
		contentType string
	)

	sw := &bufferSwitch{target: &prefixBuf}
	mw := multipart.NewWriter(sw)

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}

	if len(files) == 0 {
		if err := mw.Close(); err != nil {
			return err
		}
		readers = append(readers, bytes.NewReader(prefixBuf.Bytes()))
		totalLength = int64(prefixBuf.Len())
	} else {
		if _, err := mw.CreateFormFile(files[0].fieldName, files[0].fileName); err != nil {
			return err
		}
		readers = append(readers, bytes.NewReader(prefixBuf.Bytes()), files[0].reader)
		totalLength += int64(prefixBuf.Len()) + files[0].size

		for i := 1; i < len(files); i++ {
			sw.target = &betweenBufs[i-1]
			if _, err := mw.CreateFormFile(files[i].fieldName, files[i].fileName); err != nil {
				return err
			}
			readers = append(readers, bytes.NewReader(betweenBufs[i-1].Bytes()), files[i].reader)
			totalLength += int64(betweenBufs[i-1].Len()) + files[i].size
		}

		sw.target = &suffixBuf
		if err := mw.Close(); err != nil {
			return err
		}
		readers = append(readers, bytes.NewReader(suffixBuf.Bytes()))
		totalLength += int64(suffixBuf.Len())
	}

	contentType = mw.FormDataContentType()
	tracker := &bodyTracker{r: io.MultiReader(readers...), total: totalLength}

	req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, reqURL, tracker)
	if err != nil {
		return err
	}
	req.ContentLength = totalLength
	req.Header.Set("Content-Type", contentType)

	client := a.uploadClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tracker.fullySent() {
			return fmt.Errorf("%w: %v", ErrAmbiguousUpload, err)
		}
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if tracker.fullySent() {
			return fmt.Errorf("%w: read response: %v", ErrAmbiguousUpload, err)
		}
		return err
	}

	var apiResp struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result,omitempty"`
		ErrorCode   int             `json:"error_code,omitempty"`
		Description string          `json:"description,omitempty"`
		Parameters  *struct {
			RetryAfter      int   `json:"retry_after,omitempty"`
			MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
		} `json:"parameters,omitempty"`
	}

	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		if resp.StatusCode != http.StatusOK {
			if tracker.fullySent() && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout) {
				return fmt.Errorf("%w: server returned HTTP %d", ErrAmbiguousUpload, resp.StatusCode)
			}
			return fmt.Errorf("telegram bot api HTTP %d: %s", resp.StatusCode, string(bytes.TrimSpace(respBody)))
		}
		return fmt.Errorf("decode bot api response: %w", err)
	}

	if !apiResp.OK {
		if apiResp.Parameters != nil && apiResp.Parameters.RetryAfter > 0 {
			return &FloodError{Seconds: apiResp.Parameters.RetryAfter}
		}
		if apiResp.ErrorCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTooManyRequests {
			return &FloodError{Seconds: 5}
		}
		if apiResp.ErrorCode == http.StatusBadRequest || resp.StatusCode == http.StatusBadRequest ||
			strings.Contains(strings.ToLower(apiResp.Description), "bad request") {
			return &BadRequestError{Message: apiResp.Description}
		}
		if tracker.fullySent() && (apiResp.ErrorCode == http.StatusBadGateway || apiResp.ErrorCode == http.StatusGatewayTimeout) {
			return fmt.Errorf("%w: %d %s", ErrAmbiguousUpload, apiResp.ErrorCode, apiResp.Description)
		}
		return fmt.Errorf("telegram bot api error: %d %s", apiResp.ErrorCode, apiResp.Description)
	}

	if out != nil && len(apiResp.Result) > 0 {
		if err := json.Unmarshal(apiResp.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

func (a *BotAPI) Upload(ctx context.Context, chatID int64, u Upload) error {
	st, err := os.Stat(u.Path)
	if err != nil {
		return err
	}

	var files []filePart
	var filesToClose []io.Closer
	defer func() {
		for _, c := range filesToClose {
			_ = c.Close()
		}
	}()

	fields := map[string]string{
		"chat_id":              strconv.FormatInt(chatID, 10),
		"disable_notification": "true",
	}
	if u.Caption != "" {
		fields["caption"] = u.Caption
		fields["parse_mode"] = "HTML"
	}

	var method, fieldName string
	switch u.Kind {
	case KindVideo:
		method = "sendVideo"
		fieldName = "video"
		fields["supports_streaming"] = "true"
		if u.Duration > 0 {
			fields["duration"] = strconv.Itoa(u.Duration)
		}
		if u.Width > 0 {
			fields["width"] = strconv.Itoa(u.Width)
		}
		if u.Height > 0 {
			fields["height"] = strconv.Itoa(u.Height)
		}
	case KindAudio:
		method = "sendAudio"
		fieldName = "audio"
		if u.Duration > 0 {
			fields["duration"] = strconv.Itoa(u.Duration)
		}
		if u.Performer != "" {
			fields["performer"] = u.Performer
		}
		if u.Title != "" {
			fields["title"] = u.Title
		}
	case KindPhoto:
		method = "sendPhoto"
		fieldName = "photo"
	default: // KindDocument
		method = "sendDocument"
		fieldName = "document"
		fields["disable_content_type_detection"] = "true"
	}

	if fileURL, ok := a.localDirMap.FileURL(u.Path); ok {
		fields[fieldName] = fileURL
		if u.Progress != nil {
			u.Progress(st.Size(), st.Size())
		}
	} else {
		f, err := os.Open(u.Path)
		if err != nil {
			return err
		}
		filesToClose = append(filesToClose, f)
		pr := &progressReader{f: f, total: st.Size(), cb: u.Progress, ctx: ctx}
		files = append(files, filePart{
			fieldName: fieldName,
			fileName:  filepath.Base(u.Path),
			reader:    pr,
			size:      st.Size(),
		})
	}

	if u.Thumb != "" && u.Kind != KindPhoto {
		if thumbURL, ok := a.localDirMap.FileURL(u.Thumb); ok {
			fields["thumbnail"] = thumbURL
		} else {
			tf, err := os.Open(u.Thumb)
			if err == nil {
				tst, err := tf.Stat()
				if err == nil {
					filesToClose = append(filesToClose, tf)
					fields["thumbnail"] = "attach://thumb"
					files = append(files, filePart{
						fieldName: "thumb",
						fileName:  filepath.Base(u.Thumb),
						reader:    tf,
						size:      tst.Size(),
					})
				} else {
					_ = tf.Close()
				}
			}
		}
	}

	return a.postMultipart(ctx, method, fields, files, nil)
}

func (a *BotAPI) UploadGroup(ctx context.Context, chatID int64, items []GroupItem) error {
	if len(items) < 2 || len(items) > 10 {
		return fmt.Errorf("album needs 2-10 items, got %d", len(items))
	}
	var files []filePart
	var filesToClose []io.Closer
	defer func() {
		for _, c := range filesToClose {
			_ = c.Close()
		}
	}()

	type groupMediaItem struct {
		Type              string `json:"type"`
		Media             string `json:"media"`
		Caption           string `json:"caption,omitempty"`
		ParseMode         string `json:"parse_mode,omitempty"`
		SupportsStreaming bool   `json:"supports_streaming,omitempty"`
	}

	media := make([]groupMediaItem, 0, len(items))
	for i, it := range items {
		st, err := os.Stat(it.Path)
		if err != nil {
			return err
		}

		mediaType := "document"
		supportsStreaming := false
		switch {
		case it.Video:
			mediaType = "video"
			supportsStreaming = true
		case it.Photo:
			mediaType = "photo"
		}

		itemObj := groupMediaItem{
			Type:              mediaType,
			Caption:           it.Caption,
			SupportsStreaming: supportsStreaming,
		}
		if it.Caption != "" {
			itemObj.ParseMode = "HTML"
		}

		if fileURL, ok := a.localDirMap.FileURL(it.Path); ok {
			itemObj.Media = fileURL
		} else {
			name := fmt.Sprintf("file%d", i)
			itemObj.Media = "attach://" + name
			f, err := os.Open(it.Path)
			if err != nil {
				return err
			}
			filesToClose = append(filesToClose, f)
			files = append(files, filePart{
				fieldName: name,
				fileName:  filepath.Base(it.Path),
				reader:    f,
				size:      st.Size(),
			})
		}
		media = append(media, itemObj)
	}

	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return err
	}

	fields := map[string]string{
		"chat_id":              strconv.FormatInt(chatID, 10),
		"disable_notification": "true",
		"media":                string(mediaJSON),
	}

	return a.postMultipart(ctx, "sendMediaGroup", fields, files, nil)
}

// SetDataDir sets the configured Bot API data directory for local file cleanup.
func (a *BotAPI) SetDataDir(dir string) { a.dataDir = dir }

// IsInsideDir reports whether path is inside dir without escaping.
func IsInsideDir(path, dir string) bool {
	if dir == "" || path == "" || !filepath.IsAbs(path) {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

	inDataDir := IsInsideDir(info.FilePath, a.dataDir)
	if filepath.IsAbs(info.FilePath) && inDataDir {
		// Prefer os.Rename when source and dest are on the same filesystem.
		if err := os.Rename(info.FilePath, dest); err == nil {
			if cb != nil {
				cb(total, total)
			}
			return dest, nil
		}
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
	if filepath.IsAbs(info.FilePath) && inDataDir {
		if err := os.Remove(info.FilePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove local Bot API source file", "path", info.FilePath, "err", err)
		}
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
