package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gotd/td/fileid"
	gotdtg "github.com/gotd/td/tg"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/jobs"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/settings"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/telegraph"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/torrent"
	"github.com/burhanverse/tgdl/internal/upload"
)

type HandlerFunc func(ctx context.Context, u *Update)

type prefixHandler struct {
	prefix  string
	handler HandlerFunc
}

type App struct {
	cfg            *config.Config
	auth           *auth.Authorizer
	store          *store.Store
	mgr            *jobs.Manager
	tg             tg.Client
	keys           *upload.Keys
	hosts          *upload.Hosts
	search         *torrent.Magnetio
	lim            *pacing.TelegramLimiter
	runner         *dl.Runner
	telegraph      *telegraph.Helper
	botID          int64
	botUsername    string
	mtproto        *tg.MTProto
	renameSessions *RenameSessionManager
	settingsMgr    *settings.Manager
	settingsUI     *SettingsUI

	commands map[string]HandlerFunc
	prefixes []prefixHandler
}

type Deps struct {
	Cfg         *config.Config
	Auth        *auth.Authorizer
	Store       *store.Store
	Mgr         *jobs.Manager
	Keys        *upload.Keys
	Hosts       *upload.Hosts
	Search      *torrent.Magnetio
	Lim         *pacing.TelegramLimiter
	Runner      *dl.Runner
	Telegraph   *telegraph.Helper
	SettingsMgr *settings.Manager
}

func New(d Deps, tgClient tg.Client) *App {
	tele := d.Telegraph
	if tele == nil {
		tele = telegraph.NewHelper()
	}
	var botID int64
	if d.Cfg != nil {
		botID = parseBotID(d.Cfg.BotToken)
	}
	var setUI *SettingsUI
	if d.SettingsMgr != nil {
		setUI = NewSettingsUI(d.SettingsMgr)
	}
	a := &App{
		cfg:            d.Cfg,
		auth:           d.Auth,
		store:          d.Store,
		mgr:            d.Mgr,
		tg:             tgClient,
		keys:           d.Keys,
		hosts:          d.Hosts,
		search:         d.Search,
		lim:            d.Lim,
		runner:         d.Runner,
		telegraph:      tele,
		botID:          botID,
		renameSessions: NewRenameSessionManager(d.Cfg, tgClient),
		settingsMgr:    d.SettingsMgr,
		settingsUI:     setUI,
		commands:       make(map[string]HandlerFunc),
	}
	a.register()
	return a
}

func (a *App) Stop() {
	if a.renameSessions != nil {
		a.renameSessions.Stop()
	}
}

func parseBotID(token string) int64 {
	if parts := strings.SplitN(token, ":", 2); len(parts) > 0 {
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		return id
	}
	return 0
}

func (a *App) RegisterWithMTProto(m *tg.MTProto) {
	a.mtproto = m
	d := m.Dispatcher()
	d.OnNewMessage(func(ctx context.Context, e gotdtg.Entities, u *gotdtg.UpdateNewMessage) error {
		m.Peers().Ingest(e)
		if upd := a.convertMessage(ctx, u.Message, e); upd != nil {
			a.Dispatch(ctx, upd)
		}
		return nil
	})
	d.OnNewChannelMessage(func(ctx context.Context, e gotdtg.Entities, u *gotdtg.UpdateNewChannelMessage) error {
		m.Peers().Ingest(e)
		if upd := a.convertMessage(ctx, u.Message, e); upd != nil {
			a.Dispatch(ctx, upd)
		}
		return nil
	})
	d.OnBotCallbackQuery(func(ctx context.Context, e gotdtg.Entities, u *gotdtg.UpdateBotCallbackQuery) error {
		m.Peers().Ingest(e)
		if upd := a.convertCallback(u); upd != nil {
			a.Dispatch(ctx, upd)
		}
		return nil
	})
}

func (a *App) convertMessage(ctx context.Context, msgClass gotdtg.MessageClass, e gotdtg.Entities) *Update {
	switch msg := msgClass.(type) {
	case *gotdtg.MessageService:
		chatID, chatType := peerToChatID(msg.PeerID, e)
		if chatType == "" && chatID != 0 {
			chatType = chatTypeFromID(chatID)
		}
		fromID := peerToUserID(msg.FromID)
		if fromID == 0 && (chatType == "private" || chatID > 0) {
			fromID = chatID
		}
		isBot := false
		if u, exists := e.Users[fromID]; exists && u != nil && u.Bot {
			isBot = true
		}
		m := &Message{
			ID:   msg.ID,
			Chat: Chat{ID: chatID, Type: chatType},
			From: &User{ID: fromID, IsBot: isBot},
		}
		switch msg.Action.(type) {
		case *gotdtg.MessageActionPinMessage:
			pinID := 0
			if rh, ok := msg.ReplyTo.(*gotdtg.MessageReplyHeader); ok {
				pinID = rh.ReplyToMsgID
			}
			m.PinnedMessage = &Message{ID: pinID, From: m.From}
		case *gotdtg.MessageActionChatAddUser, *gotdtg.MessageActionChatJoinedByLink, *gotdtg.MessageActionChatJoinedByRequest:
			m.NewChatMembers = true
		case *gotdtg.MessageActionChatDeleteUser:
			m.LeftChatMember = true
		}
		return &Update{Message: m}
	case *gotdtg.Message:
		chatID, chatType := peerToChatID(msg.PeerID, e)
		if chatType == "" && chatID != 0 {
			chatType = chatTypeFromID(chatID)
		}
		fromID := peerToUserID(msg.FromID)
		if fromID == 0 && (chatType == "private" || chatID > 0) {
			fromID = chatID
		}
		isBot := false
		if u, exists := e.Users[fromID]; exists && u != nil && u.Bot {
			isBot = true
		}
		m := &Message{
			ID:      msg.ID,
			Chat:    Chat{ID: chatID, Type: chatType},
			From:    &User{ID: fromID, IsBot: isBot},
			Text:    msg.Message,
			Caption: msg.Message,
		}
		if msg.Pinned {
			m.PinnedMessage = &Message{ID: msg.ID, From: m.From}
		}
		if msg.ReplyTo != nil {
			if rh, ok := msg.ReplyTo.(*gotdtg.MessageReplyHeader); ok && rh.ReplyToMsgID != 0 {
				m.ReplyToMessage = &Message{
					ID: rh.ReplyToMsgID,
				}
				if a.mtproto != nil && ctx != nil {
					if repMsg, repEntities, err := a.mtproto.GetMessage(ctx, chatID, rh.ReplyToMsgID); err == nil && repMsg != nil {
						if conv := a.convertMessage(nil, repMsg, repEntities); conv != nil && conv.Message != nil {
							m.ReplyToMessage = conv.Message
						}
					}
				}
			}
		}
		if msg.Media != nil {
			switch med := msg.Media.(type) {
			case *gotdtg.MessageMediaDocument:
				if doc, ok := med.Document.(*gotdtg.Document); ok && doc != nil {
					fid := fileid.FromDocument(doc)
					fidStr, _ := fileid.EncodeFileID(fid)
					fn := ""
					var vAttr *gotdtg.DocumentAttributeVideo
					var aAttr *gotdtg.DocumentAttributeAudio
					for _, attr := range doc.Attributes {
						switch at := attr.(type) {
						case *gotdtg.DocumentAttributeFilename:
							fn = at.FileName
						case *gotdtg.DocumentAttributeVideo:
							vAttr = at
						case *gotdtg.DocumentAttributeAudio:
							aAttr = at
						}
					}
					if fn == "" {
						if vAttr != nil {
							fn = fmt.Sprintf("video_%d.mp4", doc.ID)
						} else if aAttr != nil {
							fn = fmt.Sprintf("audio_%d.mp3", doc.ID)
						} else {
							fn = fmt.Sprintf("file_%d", doc.ID)
						}
					}
					if vAttr != nil {
						m.Video = &Video{
							FileID:   fidStr,
							FileName: fn,
							FileSize: doc.Size,
							Width:    vAttr.W,
							Height:   vAttr.H,
							Duration: int(vAttr.Duration),
						}
					}
					if aAttr != nil {
						m.Audio = &Audio{
							FileID:    fidStr,
							FileName:  fn,
							FileSize:  doc.Size,
							Duration:  aAttr.Duration,
							Performer: aAttr.Performer,
							Title:     aAttr.Title,
						}
					}
					m.Document = &Document{
						FileID:   fidStr,
						FileName: fn,
						FileSize: doc.Size,
					}
					m.File = &dl.TGFile{
						FileID: fidStr,
						Name:   fn,
						Size:   doc.Size,
					}
				}
			case *gotdtg.MessageMediaPhoto:
				if photo, ok := med.Photo.(*gotdtg.Photo); ok && photo != nil {
					fid := fileid.FromPhoto(photo, 'x')
					fidStr, _ := fileid.EncodeFileID(fid)
					fn := fmt.Sprintf("photo_%d.jpg", photo.ID)
					m.Photo = append(m.Photo, &PhotoSize{FileID: fidStr})
					m.File = &dl.TGFile{
						FileID: fidStr,
						Name:   fn,
						Size:   0,
					}
				}
			}
		}
		return &Update{Message: m}
	default:
		return nil
	}
}

func (a *App) convertCallback(u *gotdtg.UpdateBotCallbackQuery) *Update {
	if u == nil {
		return nil
	}
	chatID, chatType := peerToChatID(u.Peer)
	if chatType == "" && chatID != 0 {
		chatType = chatTypeFromID(chatID)
	}
	q := &CallbackQuery{
		ID:   strconv.FormatInt(u.QueryID, 10),
		From: User{ID: u.UserID},
		Message: &Message{
			ID:   u.MsgID,
			Chat: Chat{ID: chatID, Type: chatType},
		},
		Data: string(u.Data),
	}
	return &Update{CallbackQuery: q}
}

func peerToChatID(p gotdtg.PeerClass, e ...gotdtg.Entities) (int64, string) {
	switch v := p.(type) {
	case *gotdtg.PeerUser:
		return v.UserID, "private"
	case *gotdtg.PeerChat:
		return -v.ChatID, "group"
	case *gotdtg.PeerChannel:
		cid := -1000000000000 - v.ChannelID
		if len(e) > 0 && e[0].Channels != nil {
			if ch, ok := e[0].Channels[v.ChannelID]; ok && ch != nil {
				if ch.Broadcast && !ch.Megagroup {
					return cid, "channel"
				}
			}
		}
		return cid, "supergroup"
	}
	return 0, ""
}

func chatTypeFromID(chatID int64) string {
	if chatID > 0 {
		return "private"
	}
	if chatID <= -1000000000000 {
		return "supergroup"
	}
	if chatID < 0 {
		return "group"
	}
	return ""
}

func peerToUserID(p gotdtg.PeerClass) int64 {
	if u, ok := p.(*gotdtg.PeerUser); ok {
		return u.UserID
	}
	return 0
}

func (a *App) register() {
	cmd := func(names []string, h HandlerFunc) {
		for _, n := range names {
			a.commands[strings.ToLower(n)] = h
		}
	}
	cmd([]string{"start", "help"}, a.cmdHelp)
	cmd([]string{"status"}, a.cmdStatus)
	cmd([]string{"cancel"}, a.cmdCancel)
	cmd([]string{"m", "mirror"}, a.cmdMirrorTo(nil))
	cmd([]string{"gofile", "gf", "gfup"}, a.cmdMirrorTo([]upload.HostID{upload.GoFile}))
	cmd([]string{"fileditch", "fd", "fdup"}, a.cmdMirrorTo([]upload.HostID{upload.FileDitch}))
	cmd([]string{"pixeldrain", "pd", "pdup"}, a.cmdMirrorTo([]upload.HostID{upload.Pixeldrain}))
	cmd([]string{"gfdl", "gf2tg"}, a.cmdGofileDL)
	cmd([]string{"dl", "direct"}, a.cmdDirect)
	cmd([]string{"gdl", "gallerydl"}, a.cmdEngine("gallery-dl", "gdl"))
	cmd([]string{"cdl", "cyberdropdl"}, a.cmdEngine("cyberdrop-dl", "cdl"))
	cmd([]string{"mega", "meganz"}, a.cmdMega)
	cmd([]string{"tor"}, a.cmdTorrent)
	cmd([]string{"aria"}, a.cmdAria)
	cmd([]string{"ts"}, a.cmdTorrentSearch)
	cmd([]string{"gd2tg", "gdrive", "gd"}, a.cmdDrive)
	cmd([]string{"unzip"}, a.cmdUnzip)
	cmd([]string{"gofilekey", "gofile_key"}, a.cmdKey("gofile", "GoFile"))
	cmd([]string{"pdkey", "pixeldrainkey", "pd_key"}, a.cmdKey("pixeldrain", "Pixeldrain"))
	cmd([]string{"fdkey", "fileditchkey", "fd_key"}, a.cmdKey("fileditch", "FileDitch"))
	cmd([]string{"cookies"}, a.cmdCookies)
	cmd([]string{"gdlconf"}, a.cmdGDLConf)
	cmd([]string{"rename"}, a.cmdRename)
	cmd([]string{"settings"}, a.cmdSettings)
	cmd([]string{"set"}, a.cmdSet)
	cmd([]string{"get"}, a.cmdGet)
	cmd([]string{"unset"}, a.cmdUnset)
	cmd([]string{"restart"}, a.cmdRestart)

	cb := func(prefix string, h HandlerFunc) {
		a.prefixes = append(a.prefixes, prefixHandler{prefix: prefix, handler: h})
	}
	cb("cancel_job:", a.cbCancel)
	cb("archive_", a.cbArchive)
	cb("convert_", a.cbConvert)
	cb("help_page:", a.cbHelp)
	cb("status:", a.cbStatus)
	cb("rename:", a.cbRename)
	cb("set:", a.cbSettings)
}

func (a *App) Dispatch(ctx context.Context, u *Update) {
	if u == nil {
		return
	}
	if !a.authorize(ctx, u) {
		return
	}
	if u.CallbackQuery != nil {
		data := u.CallbackQuery.Data
		for _, ph := range a.prefixes {
			if strings.HasPrefix(data, ph.prefix) {
				ph.handler(ctx, u)
				return
			}
		}
		return
	}
	if u.Message != nil {
		m := u.Message
		text := textOf(m)
		if c, ok := a.matchCommand(text); ok {
			if h, exists := a.commands[c]; exists {
				slog.Info("handling command", "cmd", c, "chat", m.Chat.ID, "user", userID(m))
				h(ctx, u)
				return
			}
		}
		a.Default(ctx, u)
	}
}

func (a *App) getBotUsername() string {
	if a.botUsername != "" {
		return a.botUsername
	}
	if a.mtproto != nil {
		if self := a.mtproto.Self(); self != nil {
			a.botUsername = self.Username
			if a.botID == 0 {
				a.botID = self.ID
			}
		}
	}
	return a.botUsername
}

func (a *App) matchCommand(text string) (string, bool) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", false
	}
	raw := f[0]
	if !strings.HasPrefix(raw, "/") {
		return "", false
	}
	c := strings.TrimPrefix(raw, "/")
	if i := strings.Index(c, "@"); i >= 0 {
		target := c[i+1:]
		c = c[:i]
		botUser := a.getBotUsername()
		if target != "" && botUser != "" && !strings.EqualFold(target, botUser) {
			return "", false
		}
	}
	return strings.ToLower(c), true
}

func (a *App) authorize(ctx context.Context, u *Update) bool {
	var uid int64
	var chatID int64
	if u.Message != nil {
		if u.Message.PinnedMessage != nil || u.Message.NewChatMembers || u.Message.LeftChatMember {
			return true
		}
		if u.Message.From != nil {
			uid = u.Message.From.ID
			if a.botID != 0 && uid == a.botID {
				return true
			}
		}
		chatID = u.Message.Chat.ID
	} else if u.CallbackQuery != nil {
		uid = u.CallbackQuery.From.ID
		if u.CallbackQuery.Message != nil {
			chatID = u.CallbackQuery.Message.Chat.ID
		}
	}
	if uid == 0 {
		if chatID != 0 && a.auth.Authorized(chatID) {
			return true
		}
		return false
	}
	if a.auth.Authorized(uid) || (chatID != 0 && a.auth.Authorized(chatID)) {
		return true
	}
	slog.Warn("unauthorized access attempt", "user", uid)
	if u.CallbackQuery != nil {
		_ = a.tg.Answer(ctx, u.CallbackQuery.ID, "You are not authorized to use this bot.", true)
	} else if u.Message != nil && chatID != 0 {
		_, _ = a.tg.Send(ctx, chatID, "You are not authorized to use this bot.", tg.SendOpts{})
	}
	return false
}

func (a *App) Default(ctx context.Context, u *Update) {
	m := u.Message
	if m == nil {
		return
	}
	if a.settingsUI != nil && m.Chat.IsPrivate() && m.Text != "" {
		if a.handleSettingsInput(ctx, m) {
			return
		}
	}
	if m.PinnedMessage != nil {
		isBotPin := false
		if a.botID != 0 {
			if m.From != nil && m.From.ID == a.botID {
				isBotPin = true
			} else if m.PinnedMessage != nil && m.PinnedMessage.From != nil && m.PinnedMessage.From.ID == a.botID {
				isBotPin = true
			}
		} else if m.From != nil && m.From.IsBot {
			isBotPin = true
		}
		if !isBotPin && a.mgr != nil && m.PinnedMessage != nil {
			if a.mgr.IsStatusMessage(m.Chat.ID, m.PinnedMessage.ID) {
				isBotPin = true
			}
		}
		if isBotPin {
			_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
		}
		return
	}
	if m.NewChatMembers || m.LeftChatMember {
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
		return
	}
	if m.Caption != "" && strings.HasPrefix(m.Caption, "/") {
		if c, ok := a.matchCommand(m.Caption); ok {
			switch c {
			case "unzip":
				a.cmdUnzip(ctx, u)
				return
			case "rename":
				a.cmdRename(ctx, u)
				return
			case "cookies":
				a.cmdCookies(ctx, u)
				return
			case "gdlconf":
				a.cmdGDLConf(ctx, u)
				return
			}
		}
	}
	if a.renameSessions != nil {
		if _, ok := a.renameSessions.Get(m.Chat.ID, userID(m)); ok {
			if file := mediaOf(m); file != nil {
				added, atLimit, _ := a.renameSessions.AddFile(m.Chat.ID, userID(m), *file)
				if atLimit && !added {
					a.reply(ctx, m, fmt.Sprintf("Batch limit of %d files reached.", a.cfg.RenameMaxBatch))
				}
				return
			}
		}
	}
	if m.ReplyToMessage != nil && m.Text != "" && !strings.HasPrefix(m.Text, "/") {
		if a.mgr != nil && a.mgr.DeliverPassword(m.Chat.ID, m.ReplyToMessage.ID, strings.TrimSpace(m.Text)) {
			_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
			return
		}
	}
	if a.renameSessions != nil && m.Text != "" && !strings.HasPrefix(m.Text, "/") {
		if pending, ok := a.renameSessions.GetPendingSingle(m.Chat.ID, userID(m)); ok {
			a.renameSessions.RemovePendingSingle(m.Chat.ID, userID(m))
			a.handlePendingSingleName(ctx, m, pending)
			return
		}
		if sess, ok := a.renameSessions.Get(m.Chat.ID, userID(m)); ok {
			a.handleBatchNamesMessage(ctx, m, sess)
			return
		}
	}
}

func commandOf(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	c := strings.TrimPrefix(f[0], "/")
	if i := strings.Index(c, "@"); i >= 0 {
		c = c[:i]
	}
	return strings.ToLower(c)
}

func textOf(m *Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

func tokens(m *Message) []string {
	f := strings.Fields(textOf(m))
	if len(f) == 0 {
		return nil
	}
	return f[1:]
}

func userID(m *Message) int64 {
	if m.From != nil {
		return m.From.ID
	}
	return 0
}

func (a *App) reply(ctx context.Context, m *Message, html string) {
	if a.lim != nil {
		_ = a.lim.Acquire(ctx, m.Chat.ID)
	}
	if _, err := a.tg.Send(ctx, m.Chat.ID, html, tg.SendOpts{ReplyTo: m.ID}); err != nil {
		if a.lim != nil {
			if secs, ok := tg.IsFlood(err); ok {
				a.lim.NotifyFloodWait(secs, m.Chat.ID)
			}
		}
		slog.Warn("reply failed", "err", err)
	} else if a.lim != nil {
		a.lim.RecordCallDone(m.Chat.ID)
	}
}

func (a *App) replyKB(ctx context.Context, m *Message, html string, kb tg.Keyboard) tg.MessageRef {
	if a.lim != nil {
		_ = a.lim.Acquire(ctx, m.Chat.ID)
	}
	ref, err := a.tg.Send(ctx, m.Chat.ID, html, tg.SendOpts{ReplyTo: m.ID, Keyboard: kb})
	if err != nil {
		if a.lim != nil {
			if secs, ok := tg.IsFlood(err); ok {
				a.lim.NotifyFloodWait(secs, m.Chat.ID)
			}
		}
		slog.Warn("reply failed", "err", err)
	} else if a.lim != nil {
		a.lim.RecordCallDone(m.Chat.ID)
	}
	return ref
}

func esc(s string) string  { return status.Esc(s) }
func code(s string) string { return status.Code(s) }

func mediaOf(m *Message) *dl.TGFile {
	if m == nil {
		return nil
	}
	if m.File != nil {
		return m.File
	}
	switch {
	case m.Document != nil:
		return &dl.TGFile{FileID: m.Document.FileID, Name: m.Document.FileName, Size: m.Document.FileSize}
	case m.Video != nil:
		return &dl.TGFile{FileID: m.Video.FileID, Name: m.Video.FileName, Size: m.Video.FileSize}
	case m.Audio != nil:
		return &dl.TGFile{FileID: m.Audio.FileID, Name: m.Audio.FileName, Size: m.Audio.FileSize}
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1]
		return &dl.TGFile{FileID: p.FileID, Name: fmt.Sprintf("photo_%d.jpg", m.ID), Size: int64(p.FileSize)}
	}
	return nil
}

func mediaSource(m *Message) (*Message, *dl.TGFile) {
	if f := mediaOf(m.ReplyToMessage); f != nil {
		return m.ReplyToMessage, f
	}
	if f := mediaOf(m); f != nil {
		return m, f
	}
	return nil, nil
}

func (a *App) enqueue(ctx context.Context, m *Message, target string, args dl.Args) {
	active, err := a.store.ActiveJobsForChat(ctx, m.Chat.ID)
	if err != nil {
		slog.Error("count active jobs failed", "err", err)
	}
	if lim := a.cfg.MaxJobsPerChat; lim > 0 && len(active) >= lim {
		a.reply(ctx, m, fmt.Sprintf("<b>Queue Limit Reached</b>: you have %d active or queued job(s). The maximum per chat is %d.", len(active), lim))
		return
	}
	if uid := userID(m); uid > 0 {
		args.UserID = json.Number(strconv.FormatInt(uid, 10))
	}
	raw := ""
	if b, err := json.Marshal(args); err == nil && string(b) != "{}" {
		raw = string(b)
	}
	job, err := a.store.CreateJob(ctx, m.Chat.ID, target, true, raw)
	if err != nil {
		slog.Error("create job failed", "err", err)
		a.reply(ctx, m, "Could not create the job: "+code(err.Error()))
		return
	}
	if a.mgr != nil {
		a.mgr.Add(job.ID)
	}
}

var urlPrefixes = []string{"http://", "https://", "magnet:", "gdrive:", "gd2tg:", "mega:", "gofile:", "gf:", "gfdl:",
	"gf2tg:", "fpd:", "cdl:", "direct:"}

func isURLToken(t string) bool {
	for _, p := range urlPrefixes {
		if strings.HasPrefix(strings.ToLower(t), p) {
			return true
		}
	}
	return false
}

type flags struct {
	Mirror, TG, Unzip bool
	Password          string
	ToHosts           string
	URLs              []string
	Extra             []string
}

func parseFlags(toks []string) flags {
	var f flags
	for i := 0; i < len(toks); i++ {
		t := strings.TrimSpace(toks[i])
		low := strings.ToLower(t)
		switch {
		case t == "":
		case low == "-m" || low == "-mirror" || low == "--mirror":
			f.Mirror = true
		case low == "-tg" || low == "--tg":
			f.TG = true
		case low == "-uz" || low == "-unzip" || low == "--unzip":
			f.Unzip = true
		case low == "-to" || low == "--to":
			if i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
				i++
				f.ToHosts = strings.TrimSpace(toks[i])
			}
		case strings.HasPrefix(low, "-to=") || strings.HasPrefix(low, "--to="):
			f.ToHosts = strings.TrimSpace(t[strings.Index(t, "=")+1:])
		case low == "-p" || low == "-pass" || low == "--pass" || low == "--password":
			if i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
				i++
				f.Password = strings.TrimSpace(toks[i])
			}
		case strings.HasPrefix(low, "-p=") || strings.HasPrefix(low, "-pass=") || strings.HasPrefix(low, "--pass=") || strings.HasPrefix(low, "--password="):
			f.Password = strings.TrimSpace(t[strings.Index(t, "=")+1:])
		case isURLToken(t):
			f.URLs = append(f.URLs, t)
		case low == "-c" || low == "--config" || low == "--config-file":
			if i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
				i++
			}
		case strings.HasPrefix(low, "-c=") || strings.HasPrefix(low, "--config=") || strings.HasPrefix(low, "--config-file="):
		case low == "-o" || low == "--option":
			if i+1 < len(toks) {
				next := strings.TrimSpace(toks[i+1])
				if dl.PointsToFile(next) {
					i++
				} else {
					f.Extra = append(f.Extra, t, next)
					i++
				}
			}
		case strings.HasPrefix(low, "-o=") || strings.HasPrefix(low, "--option="):
			val := t[strings.Index(t, "=")+1:]
			if !dl.PointsToFile(val) {
				f.Extra = append(f.Extra, t)
			}
		default:
			if strings.HasPrefix(t, "-") {
				f.Extra = append(f.Extra, t)
			}
		}
	}
	return f
}

func jobTarget(urls []string) string {
	if len(urls) == 1 {
		return urls[0]
	}
	b, _ := json.Marshal(urls)
	return string(b)
}

func (a *App) ack(ctx context.Context, q *CallbackQuery, text string, alert bool) {
	_ = a.tg.Answer(ctx, q.ID, text, alert)
}
