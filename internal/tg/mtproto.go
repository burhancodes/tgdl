package tg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/fileid"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type MTProtoConfig struct {
	APIID    int
	APIHash  string
	BotToken string
	DataDir  string
}

type MTProto struct {
	cfg        MTProtoConfig
	client     *telegram.Client
	rawAPI     *tg.Client
	sender     *message.Sender
	peers      *PeerStore
	dispatcher tg.UpdateDispatcher

	ready chan struct{}
	close sync.Once
	self  *tg.User
}

type PeerRecord struct {
	AccessHash int64
	PeerType   string
}

type PeerStore struct {
	mu    sync.RWMutex
	db    *sql.DB
	cache map[int64]PeerRecord
}

func NewPeerStore(db *sql.DB) *PeerStore {
	ps := &PeerStore{
		db:    db,
		cache: make(map[int64]PeerRecord),
	}
	if db != nil {
		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS tg_peers (
			chat_id INTEGER PRIMARY KEY,
			access_hash INTEGER NOT NULL,
			peer_type TEXT NOT NULL
		);`)
		rows, err := db.Query(`SELECT chat_id, access_hash, peer_type FROM tg_peers`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var (
					cid  int64
					hash int64
					pt   string
				)
				if err := rows.Scan(&cid, &hash, &pt); err == nil {
					ps.cache[cid] = PeerRecord{AccessHash: hash, PeerType: pt}
				}
			}
		}
	}
	return ps
}

func (ps *PeerStore) Save(chatID int64, accessHash int64, peerType string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.cache[chatID] = PeerRecord{AccessHash: accessHash, PeerType: peerType}
	if ps.db != nil {
		_, _ = ps.db.Exec(`INSERT INTO tg_peers (chat_id, access_hash, peer_type) VALUES (?, ?, ?)
			ON CONFLICT(chat_id) DO UPDATE SET access_hash = excluded.access_hash, peer_type = excluded.peer_type`,
			chatID, accessHash, peerType)
	}
}

func (ps *PeerStore) Resolve(chatID int64) (tg.InputPeerClass, bool) {
	ps.mu.RLock()
	rec, ok := ps.cache[chatID]
	ps.mu.RUnlock()

	switch {
	case chatID <= -1000000000000:
		chID := -chatID - 1000000000000
		if ok && rec.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: chID, AccessHash: rec.AccessHash}, true
		}
		return &tg.InputPeerChannel{ChannelID: chID}, false
	case chatID < 0:
		return &tg.InputPeerChat{ChatID: -chatID}, true
	case chatID > 0:
		if ok && rec.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: chatID, AccessHash: rec.AccessHash}, true
		}
		return &tg.InputPeerUser{UserID: chatID}, false
	default:
		return nil, false
	}
}

func (ps *PeerStore) Ingest(entities tg.Entities) {
	for _, u := range entities.Users {
		if u != nil && u.AccessHash != 0 {
			ps.Save(u.ID, u.AccessHash, "user")
		}
	}
	for _, ch := range entities.Channels {
		if ch != nil && ch.AccessHash != 0 {
			cid := -1000000000000 - ch.ID
			ps.Save(cid, ch.AccessHash, "channel")
		}
	}
	for _, c := range entities.Chats {
		if c != nil {
			cid := -c.ID
			ps.Save(cid, 0, "chat")
		}
	}
}

type updateHandler struct {
	dispatcher *tg.UpdateDispatcher
}

func (h *updateHandler) Handle(ctx context.Context, u tg.UpdatesClass) error {
	switch v := u.(type) {
	case *tg.UpdateShortMessage:
		return h.dispatcher.Handle(ctx, &tg.Updates{
			Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{
					Message: &tg.Message{
						Out:      v.Out,
						Silent:   v.Silent,
						ID:       v.ID,
						FromID:   &tg.PeerUser{UserID: v.UserID},
						PeerID:   &tg.PeerUser{UserID: v.UserID},
						Message:  v.Message,
						Entities: v.Entities,
					},
					Pts:      v.Pts,
					PtsCount: v.PtsCount,
				},
			},
		})
	case *tg.UpdateShortChatMessage:
		return h.dispatcher.Handle(ctx, &tg.Updates{
			Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{
					Message: &tg.Message{
						Out:      v.Out,
						Silent:   v.Silent,
						ID:       v.ID,
						FromID:   &tg.PeerUser{UserID: v.FromID},
						PeerID:   &tg.PeerChat{ChatID: v.ChatID},
						Message:  v.Message,
						Entities: v.Entities,
					},
					Pts:      v.Pts,
					PtsCount: v.PtsCount,
				},
			},
		})
	default:
		return h.dispatcher.Handle(ctx, u)
	}
}

func NewMTProto(cfg MTProtoConfig, db *sql.DB) *MTProto {
	peers := NewPeerStore(db)
	dispatcher := tg.NewUpdateDispatcher()

	opts := telegram.Options{
		UpdateHandler: &updateHandler{dispatcher: &dispatcher},
	}
	if cfg.DataDir != "" {
		_ = os.MkdirAll(cfg.DataDir, 0755)
		opts.SessionStorage = &session.FileStorage{
			Path: filepath.Join(cfg.DataDir, "tg_session.json"),
		}
	}

	client := telegram.NewClient(cfg.APIID, cfg.APIHash, opts)
	rawAPI := client.API()
	sender := message.NewSender(rawAPI)

	m := &MTProto{
		cfg:        cfg,
		client:     client,
		rawAPI:     rawAPI,
		sender:     sender,
		peers:      peers,
		dispatcher: dispatcher,
		ready:      make(chan struct{}),
	}
	return m
}

func (m *MTProto) Dispatcher() *tg.UpdateDispatcher {
	return &m.dispatcher
}

func (m *MTProto) Peers() *PeerStore {
	return m.peers
}

func (m *MTProto) Client() *telegram.Client {
	return m.client
}

func (m *MTProto) Ready() <-chan struct{} {
	return m.ready
}

func (m *MTProto) Self() *tg.User {
	return m.self
}

func (m *MTProto) waitReady(ctx context.Context) error {
	select {
	case <-m.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *MTProto) Start(ctx context.Context) error {
	defer m.close.Do(func() {
		close(m.ready)
	})
	return m.client.Run(ctx, func(runCtx context.Context) error {
		status, err := m.client.Auth().Status(runCtx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			if _, err := m.client.Auth().Bot(runCtx, m.cfg.BotToken); err != nil {
				return err
			}
		}
		if self, err := m.client.Self(runCtx); err == nil && self != nil {
			m.self = self
			slog.Info("authenticated to Telegram MTProto successfully", "bot", self.Username, "id", self.ID)
		} else {
			slog.Info("authenticated to Telegram MTProto successfully")
		}
		m.close.Do(func() {
			close(m.ready)
		})
		<-runCtx.Done()
		return runCtx.Err()
	})
}

func (m *MTProto) ResolvePeer(chatID int64) (tg.InputPeerClass, error) {
	peer, _ := m.peers.Resolve(chatID)
	if peer == nil {
		return nil, fmt.Errorf("unable to resolve peer for chat %d", chatID)
	}
	return peer, nil
}

func (m *MTProto) GetMessage(ctx context.Context, chatID int64, msgID int) (tg.MessageClass, tg.Entities, error) {
	if err := m.waitReady(ctx); err != nil {
		return nil, tg.Entities{}, err
	}
	peer, err := m.ResolvePeer(chatID)
	if err != nil {
		return nil, tg.Entities{}, err
	}
	var res tg.MessagesMessagesClass
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		res, err = m.rawAPI.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
	default:
		res, err = m.rawAPI.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
	}
	if err != nil {
		return nil, tg.Entities{}, m.mapError(err)
	}
	mod, ok := res.AsModified()
	if !ok {
		return nil, tg.Entities{}, fmt.Errorf("messages not modified or empty")
	}
	msgs := mod.GetMessages()
	if len(msgs) == 0 {
		return nil, tg.Entities{}, fmt.Errorf("message %d not found", msgID)
	}
	e := tg.Entities{
		Users:    make(map[int64]*tg.User),
		Chats:    make(map[int64]*tg.Chat),
		Channels: make(map[int64]*tg.Channel),
	}
	for _, u := range mod.GetUsers() {
		if user, ok := u.(*tg.User); ok {
			e.Users[user.ID] = user
		}
	}
	for _, c := range mod.GetChats() {
		switch chat := c.(type) {
		case *tg.Chat:
			e.Chats[chat.ID] = chat
		case *tg.Channel:
			e.Channels[chat.ID] = chat
		}
	}
	m.peers.Ingest(e)
	return msgs[0], e, nil
}

func (m *MTProto) mapError(err error) error {
	if err == nil {
		return nil
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &FloodError{Seconds: int(d.Seconds())}
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		if strings.HasPrefix(rpcErr.Message, "FLOOD_WAIT_") {
			secs, _ := strconv.Atoi(strings.TrimPrefix(rpcErr.Message, "FLOOD_WAIT_"))
			return &FloodError{Seconds: secs}
		}
		return &BadRequestError{Message: rpcErr.Message}
	}
	return err
}

func (m *MTProto) buildMarkup(o SendOpts) (tg.ReplyMarkupClass, error) {
	if o.ForceReply {
		return &tg.ReplyKeyboardForceReply{SingleUse: true}, nil
	}
	if len(o.Keyboard) == 0 {
		return nil, nil
	}
	var rows []tg.KeyboardInlineButtonRow
	for _, row := range o.Keyboard {
		var buttons []tg.KeyboardInlineButton
		for _, btn := range row {
			var b tg.KeyboardInlineButton
			b.Text = btn.Text
			if btn.URL != "" {
				b.Type = &tg.InlineButtonTypeURL{URL: btn.URL}
			} else {
				b.Type = &tg.InlineButtonTypeCallback{Data: []byte(btn.Data)}
			}
			buttons = append(buttons, b)
		}
		rows = append(rows, tg.KeyboardInlineButtonRow{Buttons: buttons})
	}
	return &tg.ReplyInlineMarkup{Rows: rows}, nil
}

func setEntityBounds(ent tg.MessageEntityClass, offset, length int) {
	switch v := ent.(type) {
	case *tg.MessageEntityBold:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityItalic:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityCode:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityPre:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityBlockquote:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityUnderline:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityStrike:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntitySpoiler:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityTextURL:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityMentionName:
		v.Offset, v.Length = offset, length
	case *tg.MessageEntityCustomEmoji:
		v.Offset, v.Length = offset, length
	}
}

func trimEntities(msg string, entities []tg.MessageEntityClass) []tg.MessageEntityClass {
	u16 := utf16.Encode([]rune(msg))
	n := len(u16)
	valid := make([]tg.MessageEntityClass, 0, len(entities))
	isSpace := func(r uint16) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}
	for _, ent := range entities {
		off := ent.GetOffset()
		length := ent.GetLength()
		if off < 0 || off >= n || length <= 0 || off+length > n {
			continue
		}
		for length > 0 && isSpace(u16[off]) {
			off++
			length--
		}
		for length > 0 && isSpace(u16[off+length-1]) {
			length--
		}
		if length <= 0 {
			continue
		}
		setEntityBounds(ent, off, length)
		valid = append(valid, ent)
	}
	return valid
}

func parseHTML(htmlText string) (string, []tg.MessageEntityClass, error) {
	if htmlText == "" {
		return "", nil, nil
	}
	var b entity.Builder
	if err := html.HTML(strings.NewReader(htmlText), &b, html.Options{}); err != nil {
		return htmlText, nil, err
	}
	msg, entities := b.Raw()
	entities = trimEntities(msg, entities)
	sortEntities(entities)
	return msg, entities, nil
}

func sortEntities(entities []tg.MessageEntityClass) {
	sort.SliceStable(entities, func(i, j int) bool {
		offA, offB := entities[i].GetOffset(), entities[j].GetOffset()
		if offA != offB {
			return offA < offB
		}
		return entities[i].GetLength() > entities[j].GetLength()
	})
}

func isMessageNotModified(err error) bool {
	if err == nil {
		return false
	}
	if tgerr.Is(err, "MESSAGE_NOT_MODIFIED") {
		return true
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) && rpcErr.Message == "MESSAGE_NOT_MODIFIED" {
		return true
	}
	return strings.Contains(err.Error(), "MESSAGE_NOT_MODIFIED")
}

func (m *MTProto) Send(ctx context.Context, chatID int64, htmlText string, o SendOpts) (MessageRef, error) {
	if err := m.waitReady(ctx); err != nil {
		return MessageRef{}, err
	}
	peer, err := m.ResolvePeer(chatID)
	if err != nil {
		return MessageRef{}, err
	}
	msg, entities, err := parseHTML(htmlText)
	if err != nil {
		msg = htmlText
		entities = nil
	}
	markup, err := m.buildMarkup(o)
	if err != nil {
		return MessageRef{}, err
	}
	randomID, err := crypto.RandInt64(rand.Reader)
	if err != nil {
		return MessageRef{}, err
	}
	req := &tg.MessagesSendMessageRequest{
		Peer:        peer,
		Message:     msg,
		RandomID:    randomID,
		Silent:      o.Silent,
		ReplyMarkup: markup,
		Entities:    entities,
	}
	if o.ReplyTo != 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: o.ReplyTo}
	}
	upd, err := m.rawAPI.MessagesSendMessage(ctx, req)
	if err != nil {
		return MessageRef{}, m.mapError(err)
	}
	return MessageRef{ChatID: chatID, ID: extractMessageID(upd)}, nil
}

func (m *MTProto) Edit(ctx context.Context, ref MessageRef, htmlText string, o SendOpts) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	peer, err := m.ResolvePeer(ref.ChatID)
	if err != nil {
		return err
	}
	msg, entities, err := parseHTML(htmlText)
	if err != nil {
		msg = htmlText
		entities = nil
	}
	markup, err := m.buildMarkup(o)
	if err != nil {
		return err
	}
	req := &tg.MessagesEditMessageRequest{
		Peer:        peer,
		ID:          ref.ID,
		Message:     msg,
		ReplyMarkup: markup,
		Entities:    entities,
	}
	_, err = m.rawAPI.MessagesEditMessage(ctx, req)
	if err != nil {
		if isMessageNotModified(err) {
			return nil
		}
		return m.mapError(err)
	}
	return nil
}

func (m *MTProto) Delete(ctx context.Context, ref MessageRef) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	peer, err := m.ResolvePeer(ref.ChatID)
	if err != nil {
		return err
	}
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		_, err = m.rawAPI.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []int{ref.ID},
		})
	default:
		_, err = m.rawAPI.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
			Revoke: true,
			ID:     []int{ref.ID},
		})
	}
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

func (m *MTProto) Pin(ctx context.Context, ref MessageRef) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	peer, err := m.ResolvePeer(ref.ChatID)
	if err != nil {
		return err
	}
	_, err = m.rawAPI.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Silent: true,
		Peer:   peer,
		ID:     ref.ID,
	})
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

func (m *MTProto) Unpin(ctx context.Context, ref MessageRef) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	peer, err := m.ResolvePeer(ref.ChatID)
	if err != nil {
		return err
	}
	_, err = m.rawAPI.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Unpin: true,
		Peer:  peer,
		ID:    ref.ID,
	})
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

func (m *MTProto) Answer(ctx context.Context, callbackID, text string, alert bool) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	queryID, err := strconv.ParseInt(callbackID, 10, 64)
	if err != nil {
		return err
	}
	req := &tg.MessagesSetBotCallbackAnswerRequest{
		QueryID: queryID,
		Alert:   alert,
	}
	if text != "" {
		req.SetMessage(text)
	}
	_, err = m.rawAPI.MessagesSetBotCallbackAnswer(ctx, req)
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

type uploadProgressTracker struct {
	fn    ProgressFunc
	total int64
}

func (p *uploadProgressTracker) Chunk(_ context.Context, state uploader.ProgressState) error {
	if p.fn != nil {
		p.fn(state.Uploaded, p.total)
	}
	return nil
}

func (m *MTProto) Upload(ctx context.Context, chatID int64, u Upload) (MessageRef, error) {
	if err := m.waitReady(ctx); err != nil {
		return MessageRef{}, err
	}
	peer, err := m.ResolvePeer(chatID)
	if err != nil {
		return MessageRef{}, err
	}

	st, err := os.Stat(u.Path)
	if err != nil {
		return MessageRef{}, err
	}
	totalBytes := st.Size()

	up := uploader.NewUploader(m.rawAPI).WithPartSize(512 * 1024)
	if u.Progress != nil {
		up = up.WithProgress(&uploadProgressTracker{fn: u.Progress, total: totalBytes})
	}

	file, err := up.FromPath(ctx, u.Path)
	if err != nil {
		return MessageRef{}, m.mapError(err)
	}

	var thumbFile tg.InputFileClass
	if u.Thumb != "" {
		if _, err := os.Stat(u.Thumb); err == nil {
			tUp := uploader.NewUploader(m.rawAPI).WithPartSize(128 * 1024)
			thumbFile, _ = tUp.FromPath(ctx, u.Thumb)
		}
	}

	filename := u.FileName
	if filename == "" {
		filename = filepath.Base(u.Path)
	}

	var doc *message.UploadedDocumentBuilder
	switch u.Kind {
	case KindPhoto:
		photo := message.UploadedPhoto(file, html.String(nil, u.Caption))
		upd, err := m.sender.To(peer).Media(ctx, photo)
		if err != nil {
			return MessageRef{}, m.mapError(err)
		}
		return MessageRef{ChatID: chatID, ID: extractMessageID(upd)}, nil
	case KindVideo:
		doc = message.UploadedDocument(file, html.String(nil, u.Caption)).
			MIME("video/mp4").
			Filename(filename).
			Attributes(&tg.DocumentAttributeVideo{
				Duration:          float64(u.Duration),
				W:                 u.Width,
				H:                 u.Height,
				SupportsStreaming: true,
			})
	case KindAudio:
		doc = message.UploadedDocument(file, html.String(nil, u.Caption)).
			MIME("audio/mpeg").
			Filename(filename).
			Attributes(&tg.DocumentAttributeAudio{
				Duration:  u.Duration,
				Title:     u.Title,
				Performer: u.Performer,
			})
	default:
		doc = message.UploadedDocument(file, html.String(nil, u.Caption)).
			Filename(filename)
	}

	if thumbFile != nil {
		doc = doc.Thumb(thumbFile)
	}

	upd, err := m.sender.To(peer).Media(ctx, doc)
	if err != nil {
		return MessageRef{}, m.mapError(err)
	}
	return MessageRef{ChatID: chatID, ID: extractMessageID(upd)}, nil
}

func (m *MTProto) UploadGroup(ctx context.Context, chatID int64, items []GroupItem) ([]MessageRef, error) {
	if err := m.waitReady(ctx); err != nil {
		return nil, err
	}
	peer, err := m.ResolvePeer(chatID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	var albumItems []message.MultiMediaOption
	for _, it := range items {
		st, err := os.Stat(it.Path)
		if err != nil {
			return nil, err
		}
		_ = st
		up := uploader.NewUploader(m.rawAPI).WithPartSize(512 * 1024)
		file, err := up.FromPath(ctx, it.Path)
		if err != nil {
			return nil, m.mapError(err)
		}
		fn := it.FileName
		if fn == "" {
			fn = filepath.Base(it.Path)
		}
		if it.Photo {
			albumItems = append(albumItems, message.UploadedPhoto(file, html.String(nil, it.Caption)))
		} else {
			doc := message.UploadedDocument(file, html.String(nil, it.Caption)).
				MIME("video/mp4").
				Filename(fn).
				Attributes(&tg.DocumentAttributeVideo{SupportsStreaming: true})
			albumItems = append(albumItems, doc)
		}
	}

	upd, err := m.sender.To(peer).Album(ctx, albumItems[0], albumItems[1:]...)
	if err != nil {
		return nil, m.mapError(err)
	}
	ids := extractMessageIDs(upd)
	var refs []MessageRef
	for _, id := range ids {
		refs = append(refs, MessageRef{ChatID: chatID, ID: id})
	}
	return refs, nil
}

type downloadProgressWriter struct {
	w     io.Writer
	cur   int64
	total int64
	fn    ProgressFunc
}

func (pw *downloadProgressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.cur += int64(n)
	if pw.fn != nil {
		pw.fn(pw.cur, pw.total)
	}
	return n, err
}

func (m *MTProto) Download(ctx context.Context, f FileRef, destDir string, progress ProgressFunc) (string, error) {
	if err := m.waitReady(ctx); err != nil {
		return "", err
	}
	fid, err := fileid.DecodeFileID(f.FileID)
	if err != nil {
		return "", fmt.Errorf("decode file id: %w", err)
	}
	loc, ok := fid.AsInputFileLocation()
	if !ok {
		return "", fmt.Errorf("cannot convert file id to input file location: %s", f.FileID)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	name := f.Name
	if name == "" {
		name = fmt.Sprintf("file_%d", time.Now().UnixNano())
	}
	destPath := filepath.Join(destDir, name)
	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", err
	}
	defer destFile.Close()

	pw := &downloadProgressWriter{
		w:     destFile,
		total: f.Size,
		fn:    progress,
	}

	d := downloader.NewDownloader().WithPartSize(512 * 1024)
	if _, err := d.Download(m.rawAPI, loc).Stream(ctx, pw); err != nil {
		_ = os.Remove(destPath)
		return "", m.mapError(err)
	}
	return destPath, nil
}

func (m *MTProto) SetCommands(ctx context.Context, cmds []Command) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	var botCmds []tg.BotCommand
	for _, c := range cmds {
		botCmds = append(botCmds, tg.BotCommand{
			Command:     c.Name,
			Description: c.Description,
		})
	}
	_, err := m.rawAPI.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
		Scope:    &tg.BotCommandScopeDefault{},
		LangCode: "",
		Commands: botCmds,
	})
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

func (m *MTProto) SetChatCommands(ctx context.Context, chatID int64, cmds []Command) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	peer, err := m.ResolvePeer(chatID)
	if err != nil {
		return err
	}
	var botCmds []tg.BotCommand
	for _, c := range cmds {
		botCmds = append(botCmds, tg.BotCommand{
			Command:     c.Name,
			Description: c.Description,
		})
	}
	_, err = m.rawAPI.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
		Scope:    &tg.BotCommandScopePeer{Peer: peer},
		LangCode: "",
		Commands: botCmds,
	})
	if err != nil {
		return m.mapError(err)
	}
	return nil
}

func extractMessageIDs(u tg.UpdatesClass) []int {
	var ids []int
	switch upd := u.(type) {
	case *tg.Updates:
		for _, rawUpd := range upd.Updates {
			if id := extractUpdateMessageID(rawUpd); id != 0 {
				ids = append(ids, id)
			}
		}
	case *tg.UpdatesCombined:
		for _, rawUpd := range upd.Updates {
			if id := extractUpdateMessageID(rawUpd); id != 0 {
				ids = append(ids, id)
			}
		}
	case *tg.UpdateShort:
		if id := extractUpdateMessageID(upd.Update); id != 0 {
			ids = append(ids, id)
		}
	case *tg.UpdateShortSentMessage:
		ids = append(ids, upd.ID)
	case *tg.UpdateShortMessage:
		ids = append(ids, upd.ID)
	case *tg.UpdateShortChatMessage:
		ids = append(ids, upd.ID)
	}
	return ids
}

func extractMessageID(u tg.UpdatesClass) int {
	ids := extractMessageIDs(u)
	if len(ids) > 0 {
		return ids[0]
	}
	return 0
}

func extractUpdateMessageID(u tg.UpdateClass) int {
	switch v := u.(type) {
	case *tg.UpdateNewMessage:
		if v.Message != nil {
			return v.Message.GetID()
		}
	case *tg.UpdateNewChannelMessage:
		if v.Message != nil {
			return v.Message.GetID()
		}
	case *tg.UpdateEditMessage:
		if v.Message != nil {
			return v.Message.GetID()
		}
	case *tg.UpdateEditChannelMessage:
		if v.Message != nil {
			return v.Message.GetID()
		}
	case *tg.UpdateMessageID:
		return v.ID
	}
	return 0
}
