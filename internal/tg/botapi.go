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
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var localDirMapWarnOnce sync.Once

type BotAPI struct {
	B            *bot.Bot
	Token        string
	BaseURL      string
	http         *http.Client
	uploadClient *http.Client
	localDirMap  *LocalDirMap
	dataDir      string

	serverVersion float64
}

type LocalDirMap struct {
	HostPrefix      string
	ContainerPrefix string
}

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

func (m *LocalDirMap) LocalPath(fileURL string) (string, bool) {
	if m == nil || m.HostPrefix == "" || m.ContainerPrefix == "" {
		return "", false
	}
	raw := strings.TrimPrefix(fileURL, "file://")
	srvPath := "/" + strings.TrimPrefix(raw, "/")
	cleanSrv := filepath.Clean(filepath.FromSlash(srvPath))
	cleanContainer := filepath.Clean(filepath.FromSlash(m.ContainerPrefix))

	rel, err := filepath.Rel(cleanContainer, cleanSrv)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	if rel == "." {
		return filepath.Clean(filepath.FromSlash(m.HostPrefix)), true
	}
	return filepath.Join(filepath.FromSlash(m.HostPrefix), rel), true
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

func (a *BotAPI) SetServerVersion(v float64) {
	if a != nil {
		a.serverVersion = v
	}
}

func (a *BotAPI) ServerVersion() float64 {
	if a != nil && a.serverVersion > 0 {
		return a.serverVersion
	}
	if v := os.Getenv("TG_BOT_API_VERSION"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 9.0
}

func (a *BotAPI) ProbeLocalDirMap(ctx context.Context, dataDir string) error {
	if a == nil || a.localDirMap == nil || a.BaseURL == "" {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	probePath := filepath.Join(dataDir, ".local_dir_probe")
	if err := os.WriteFile(probePath, []byte("probe"), 0o644); err != nil {
		return err
	}
	defer os.Remove(probePath)

	fileURL, ok := a.localDirMap.FileURL(probePath)
	if !ok {
		return fmt.Errorf("probe file %s is not within LocalDirMap host prefix %s", probePath, a.localDirMap.HostPrefix)
	}

	baseURL := strings.TrimRight(a.BaseURL, "/")
	reqURL := fmt.Sprintf("%s/bot%s/sendDocument", baseURL, a.Token)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", "0")
	_ = mw.WriteField("document", fileURL)
	_ = mw.Close()

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, reqURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := a.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var apiResp struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(respBody, &apiResp)

	desc := strings.ToLower(apiResp.Description)
	if strings.Contains(desc, "wrong file") || strings.Contains(desc, "can't open") ||
		strings.Contains(desc, "permission") || (strings.Contains(desc, "not found") && strings.Contains(desc, "file")) {
		return fmt.Errorf("bot api server cannot read local file %s (%s): %s", probePath, fileURL, apiResp.Description)
	}
	return nil
}

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

type groupMediaItem struct {
	Type              string `json:"type"`
	Media             string `json:"media"`
	Caption           string `json:"caption,omitempty"`
	ParseMode         string `json:"parse_mode,omitempty"`
	SupportsStreaming bool   `json:"supports_streaming,omitempty"`
}

func (a *BotAPI) postMultipart(ctx context.Context, method string, fields map[string]string, files []filePart, out any) error {
	return a.postMultipartInternal(ctx, method, fields, files, out, false)
}

func (a *BotAPI) postMultipartInternal(ctx context.Context, method string, fields map[string]string, files []filePart, out any, isRetry bool) error {
	baseURL := a.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	reqURL := fmt.Sprintf("%s/bot%s/%s", baseURL, a.Token, method)

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.size
	}
	if a.localDirMap != nil {
		for _, v := range fields {
			if strings.HasPrefix(v, "file://") {
				if lp, ok := a.localDirMap.LocalPath(v); ok {
					if st, err := os.Stat(lp); err == nil {
						totalBytes += st.Size()
					}
				}
			}
		}
		if mediaJSON, ok := fields["media"]; ok && method == "sendMediaGroup" {
			var mediaItems []groupMediaItem
			if err := json.Unmarshal([]byte(mediaJSON), &mediaItems); err == nil {
				for _, it := range mediaItems {
					if strings.HasPrefix(it.Media, "file://") {
						if lp, ok := a.localDirMap.LocalPath(it.Media); ok {
							if st, err := os.Stat(lp); err == nil {
								totalBytes += st.Size()
							}
						}
					}
				}
			}
		}
	}
	uploadTimeout := 15*time.Minute + time.Duration(totalBytes/(512*1024))*time.Second
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
		if (apiResp.ErrorCode == http.StatusBadRequest || resp.StatusCode == http.StatusBadRequest ||
			strings.Contains(strings.ToLower(apiResp.Description), "bad request")) && !isRetry {
			desc := strings.ToLower(apiResp.Description)
			if strings.Contains(desc, "cover") {
				if _, hasCover := fields["cover"]; hasCover {
					a.SetServerVersion(8.0)
					delete(fields, "cover")
					var filteredFiles []filePart
					for _, f := range files {
						if f.fieldName != "cover" {
							filteredFiles = append(filteredFiles, f)
						}
					}
					return a.postMultipartInternal(ctx, method, fields, filteredFiles, out, true)
				}
			}
			if strings.Contains(desc, "file") || strings.Contains(desc, "not found") ||
				strings.Contains(desc, "permission") || strings.Contains(desc, "wrong file") {
				if fallbackFiles, fallbackFields, ok := a.convertFileURIsToMultipart(method, fields); ok {
					localDirMapWarnOnce.Do(func() {
						slog.Warn("local file:// upload failed with Bot API error; falling back to streaming multipart", "err", apiResp.Description)
					})
					defer func() {
						for _, f := range fallbackFiles {
							if c, ok := f.reader.(io.Closer); ok {
								_ = c.Close()
							}
						}
					}()
					allFiles := append(append([]filePart(nil), files...), fallbackFiles...)
					return a.postMultipartInternal(ctx, method, fallbackFields, allFiles, out, true)
				}
			}
			return &BadRequestError{Message: apiResp.Description}
		}
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

func (a *BotAPI) convertFileURIsToMultipart(method string, fields map[string]string) ([]filePart, map[string]string, bool) {
	if a.localDirMap == nil {
		return nil, nil, false
	}
	var newFiles []filePart
	newFields := make(map[string]string, len(fields))
	for k, v := range fields {
		newFields[k] = v
	}
	convertedAny := false

	if method == "sendMediaGroup" {
		if mediaJSON, ok := newFields["media"]; ok {
			var mediaItems []groupMediaItem
			if err := json.Unmarshal([]byte(mediaJSON), &mediaItems); err == nil {
				for i := range mediaItems {
					if strings.HasPrefix(mediaItems[i].Media, "file://") {
						if lp, ok := a.localDirMap.LocalPath(mediaItems[i].Media); ok {
							f, err := os.Open(lp)
							if err == nil {
								st, serr := f.Stat()
								if serr == nil {
									attachName := fmt.Sprintf("file%d", i)
									mediaItems[i].Media = "attach://" + attachName
									newFiles = append(newFiles, filePart{
										fieldName: attachName,
										fileName:  filepath.Base(lp),
										reader:    f,
										size:      st.Size(),
									})
									convertedAny = true
								} else {
									_ = f.Close()
								}
							}
						}
					}
				}
				if convertedAny {
					b, err := json.Marshal(mediaItems)
					if err == nil {
						newFields["media"] = string(b)
					}
				}
			}
		}
	} else {
		for k, v := range fields {
			if strings.HasPrefix(v, "file://") {
				if lp, ok := a.localDirMap.LocalPath(v); ok {
					f, err := os.Open(lp)
					if err == nil {
						st, serr := f.Stat()
						if serr == nil {
							convertedAny = true
							if k == "thumbnail" {
								newFields["thumbnail"] = "attach://thumb"
								newFiles = append(newFiles, filePart{
									fieldName: "thumb",
									fileName:  filepath.Base(lp),
									reader:    f,
									size:      st.Size(),
								})
							} else {
								delete(newFields, k)
								newFiles = append(newFiles, filePart{
									fieldName: k,
									fileName:  filepath.Base(lp),
									reader:    f,
									size:      st.Size(),
								})
							}
						} else {
							_ = f.Close()
						}
					}
				}
			}
		}
	}

	if !convertedAny {
		for _, f := range newFiles {
			if c, ok := f.reader.(io.Closer); ok {
				_ = c.Close()
			}
		}
		return nil, nil, false
	}
	return newFiles, newFields, true
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
	default:
		method = "sendDocument"
		fieldName = "document"
		fields["disable_content_type_detection"] = "true"
	}

	isLocalFile := false
	if fileURL, ok := a.localDirMap.FileURL(u.Path); ok {
		isLocalFile = true
		fields[fieldName] = fileURL
		if u.Progress != nil {
			u.Progress(0, st.Size())
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
		tf, err := os.Open(u.Thumb)
		if err == nil {
			tst, err := tf.Stat()
			if err == nil && tst.Size() > 0 {
				filesToClose = append(filesToClose, tf)
				fields["thumbnail"] = "attach://thumb"
				files = append(files, filePart{
					fieldName: "thumb",
					fileName:  filepath.Base(u.Thumb),
					reader:    tf,
					size:      tst.Size(),
				})
				if u.Kind == KindVideo && a.ServerVersion() >= 9.0 {
					cf, err := os.Open(u.Thumb)
					if err == nil {
						filesToClose = append(filesToClose, cf)
						fields["cover"] = "attach://cover"
						files = append(files, filePart{
							fieldName: "cover",
							fileName:  filepath.Base(u.Thumb),
							reader:    cf,
							size:      tst.Size(),
						})
					}
				}
			} else {
				_ = tf.Close()
			}
		} else {
			slog.Warn("could not open thumbnail file", "thumb", u.Thumb, "err", err)
		}
	}

	err = a.postMultipart(ctx, method, fields, files, nil)
	if err == nil && isLocalFile && u.Progress != nil {
		u.Progress(st.Size(), st.Size())
	}
	return err
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

func (a *BotAPI) SetDataDir(dir string) { a.dataDir = dir }

func (a *BotAPI) DataDir() string { return a.dataDir }

var knownMediaDirs = []string{
	"documents",
	"videos",
	"photos",
	"animations",
	"audio",
	"voice",
	"video_notes",
	"stickers",
	"music",
	"thumbnails",
	"temp",
}

func MaskToken(text, token string) string {
	if token == "" {
		return text
	}
	text = strings.ReplaceAll(text, "/"+token+"/", "/***/")
	if strings.Contains(text, token) {
		text = strings.ReplaceAll(text, token, "***")
	}
	return text
}

func ExtractRelativeFilePath(rawPath, token string) string {
	rawPath = filepath.ToSlash(rawPath)
	if token != "" {
		tokenSlash := "/" + token + "/"
		if idx := strings.Index(rawPath, tokenSlash); idx != -1 {
			return strings.TrimPrefix(rawPath[idx+len(tokenSlash):], "/")
		}
		if strings.HasPrefix(rawPath, token+"/") {
			return strings.TrimPrefix(rawPath[len(token+"/"):], "/")
		}
	}
	parts := strings.Split(rawPath, "/")
	for i, part := range parts {
		for _, k := range knownMediaDirs {
			if strings.EqualFold(part, k) {
				return strings.Join(parts[i:], "/")
			}
		}
	}
	return strings.TrimPrefix(filepath.Clean(rawPath), "/")
}

func (a *BotAPI) resolveLocalFile(rawPath string) (string, error) {
	if rawPath == "" {
		return "", os.ErrNotExist
	}
	var candidates []string
	if filepath.IsAbs(rawPath) {
		candidates = append(candidates, filepath.Clean(rawPath))
	}
	if a.localDirMap != nil {
		if p, ok := a.localDirMap.LocalPath(rawPath); ok {
			candidates = append(candidates, filepath.Clean(p))
		}
	}
	dataDir := a.dataDir
	if dataDir == "" {
		dataDir = os.Getenv("TG_BOT_API_DATA_DIR")
	}
	if dataDir != "" {
		rel := ExtractRelativeFilePath(rawPath, a.Token)
		if a.Token != "" {
			candidates = append(candidates, filepath.Join(dataDir, a.Token, rel))
		}
		candidates = append(candidates, filepath.Join(dataDir, rel))

		slashPath := filepath.ToSlash(rawPath)
		if prefix := "/var/lib/telegram-bot-api/"; strings.HasPrefix(slashPath, prefix) {
			afterPrefix := strings.TrimPrefix(slashPath, prefix)
			candidates = append(candidates, filepath.Join(dataDir, filepath.FromSlash(afterPrefix)))
		}
	}

	seen := make(map[string]bool)
	var permErr error
	var permPath string

	for _, cand := range candidates {
		cand = filepath.Clean(cand)
		if seen[cand] {
			continue
		}
		seen[cand] = true

		st, err := os.Stat(cand)
		if err == nil {
			if !st.IsDir() {
				return cand, nil
			}
		} else if os.IsPermission(err) {
			if permErr == nil {
				permErr = err
				permPath = cand
			}
		}
	}

	if permErr != nil {
		return "", fmt.Errorf("cannot read %s: %w", permPath, permErr)
	}
	return "", os.ErrNotExist
}

func resolveLocalFile(rawPath string) (string, bool) {
	api := &BotAPI{}
	p, err := api.resolveLocalFile(rawPath)
	return p, err == nil
}

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
	part := dest + ".part"
	total := f.Size
	if total == 0 {
		total = info.FileSize
	}

	if filepath.IsAbs(info.FilePath) {
		resolvedPath, statErr := a.resolveLocalFile(info.FilePath)
		if statErr == nil {
			if testFile, openErr := os.Open(resolvedPath); openErr != nil {
				statErr = openErr
			} else {
				_ = testFile.Close()
			}
		}
		if statErr == nil {
			_ = os.Remove(part)
			_ = os.Remove(dest)
			var copied bool
			if err := os.Link(resolvedPath, dest); err == nil {
				copied = true
			} else {
				if err := copyFileToDest(ctx, resolvedPath, part, dest, total, cb); err == nil {
					copied = true
				}
			}
			if copied {
				st, serr := os.Stat(dest)
				if serr != nil || (info.FileSize > 0 && st.Size() != info.FileSize) {
					_ = os.Remove(dest)
					_ = os.Remove(part)
					var gotSize int64
					if serr == nil {
						gotSize = st.Size()
					}
					return "", fmt.Errorf("telegram file download: size mismatch: expected %d bytes, got %d", info.FileSize, gotSize)
				}
				if cb != nil {
					cb(st.Size(), st.Size())
				}
				if err := os.Remove(resolvedPath); err != nil && !os.IsNotExist(err) {
					slog.Warn("failed to delete bot api source file", "path", MaskToken(resolvedPath, a.Token), "err", err, "hint", "tgdl should run as root with DAC_OVERRIDE or have write permissions")
				}
				return dest, nil
			}
		}

		slog.Warn("telegram file download cannot read local file", "path", MaskToken(info.FilePath, a.Token), "err", statErr)

		relPath := ExtractRelativeFilePath(info.FilePath, a.Token)
		fallbackURL := fmt.Sprintf("%s/file/bot%s/%s", strings.TrimRight(a.BaseURL, "/"), a.Token, relPath)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fallbackURL, nil)
		if err != nil {
			return "", err
		}
		resp, err := a.http.Do(req)
		if err != nil {
			masked := MaskToken(info.FilePath, a.Token)
			if statErr != nil && (os.IsPermission(statErr) || strings.Contains(statErr.Error(), "permission denied")) {
				return "", fmt.Errorf("telegram file download: cannot read %s (permission denied); HTTP fallback error: %w", masked, err)
			}
			return "", fmt.Errorf("telegram file download: file not found on the shared volume: %s; HTTP fallback error: %w", masked, err)
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			masked := MaskToken(info.FilePath, a.Token)
			if statErr != nil && (os.IsPermission(statErr) || strings.Contains(statErr.Error(), "permission denied")) {
				return "", fmt.Errorf("telegram file download: cannot read %s (permission denied); HTTP fallback returned %d", masked, resp.StatusCode)
			}
			return "", fmt.Errorf("telegram file download: file not found on the shared volume: %s; HTTP fallback returned %d", masked, resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
		}
		return a.streamHTTPResponse(ctx, resp, dest, part, info.FileSize, total, cb)
	}

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
	return a.streamHTTPResponse(ctx, resp, dest, part, info.FileSize, total, cb)
}

func (a *BotAPI) streamHTTPResponse(ctx context.Context, resp *http.Response, dest, part string, expectedSize, total int64, cb ProgressFunc) (string, error) {
	defer resp.Body.Close()
	if total == 0 {
		total = resp.ContentLength
	}
	_ = os.Remove(part)
	_ = os.Remove(dest)
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
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
	if expectedSize == 0 && total > 0 {
		expectedSize = total
	}
	if expectedSize > 0 && done != expectedSize {
		_ = os.Remove(part)
		return "", fmt.Errorf("telegram file download: size mismatch: expected %d bytes, got %d", expectedSize, done)
	}
	if err := os.Rename(part, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func copyFileToDest(ctx context.Context, src, part, dest string, total int64, cb ProgressFunc) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_ = os.Remove(part)
	_ = os.Remove(dest)
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				_ = out.Close()
				_ = os.Remove(part)
				return werr
			}
			done += int64(n)
			if cb != nil {
				cb(done, total)
			}
		}
		if ctx.Err() != nil {
			_ = out.Close()
			_ = os.Remove(part)
			return ctx.Err()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			_ = os.Remove(part)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

func (a *BotAPI) SetCommands(ctx context.Context, cmds []Command) error {
	bc := make([]models.BotCommand, len(cmds))
	for i, c := range cmds {
		bc[i] = models.BotCommand{Command: c.Name, Description: c.Description}
	}
	_, err := a.B.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: bc})
	return mapErr(err)
}

func (a *BotAPI) SetChatCommands(ctx context.Context, chatID int64, cmds []Command) error {
	bc := make([]models.BotCommand, len(cmds))
	for i, c := range cmds {
		bc[i] = models.BotCommand{Command: c.Name, Description: c.Description}
	}
	_, err := a.B.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: bc,
		Scope:    &models.BotCommandScopeChat{ChatID: chatID},
	})
	return mapErr(err)
}
