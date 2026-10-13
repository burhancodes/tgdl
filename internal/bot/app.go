package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/fsutil"
	"github.com/burhanverse/tgdl/internal/jobs"
	"github.com/burhanverse/tgdl/internal/pacing"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/store"
	"github.com/burhanverse/tgdl/internal/telegraph"
	"github.com/burhanverse/tgdl/internal/tg"
	"github.com/burhanverse/tgdl/internal/torrent"
	"github.com/burhanverse/tgdl/internal/upload"
	"golang.org/x/sync/semaphore"
)

type App struct {
	cfg           *config.Config
	auth          *auth.Authorizer
	store         *store.Store
	mgr           *jobs.Manager
	tg            tg.Client
	api           *tg.BotAPI
	keys          *upload.Keys
	hosts         *upload.Hosts
	search        *torrent.Magnetio
	lim           *pacing.TelegramLimiter
	runner        *dl.Runner
	telegraph     *telegraph.Helper
	hostUploadSem *semaphore.Weighted
	botID         int64
}

type Deps struct {
	Cfg       *config.Config
	Auth      *auth.Authorizer
	Store     *store.Store
	Mgr       *jobs.Manager
	Keys      *upload.Keys
	Hosts     *upload.Hosts
	Search    *torrent.Magnetio
	Lim       *pacing.TelegramLimiter
	Runner    *dl.Runner
	Telegraph *telegraph.Helper
}

func New(d Deps, b *tgbot.Bot, api *tg.BotAPI) *App {
	tele := d.Telegraph
	if tele == nil {
		tele = telegraph.NewHelper()
	}
	semLimit := int64(1)
	if d.Cfg != nil && d.Cfg.MaxConcurrentDL > 0 {
		semLimit = int64(d.Cfg.MaxConcurrentDL)
	}
	var botID int64
	if d.Cfg != nil && d.Cfg.BotToken != "" {
		if parts := strings.SplitN(d.Cfg.BotToken, ":", 2); len(parts) > 0 {
			botID, _ = strconv.ParseInt(parts[0], 10, 64)
		}
	}
	a := &App{cfg: d.Cfg, auth: d.Auth, store: d.Store, mgr: d.Mgr, tg: api, api: api,
		keys: d.Keys, hosts: d.Hosts, search: d.Search, lim: d.Lim, runner: d.Runner, telegraph: tele,
		hostUploadSem: semaphore.NewWeighted(semLimit), botID: botID}
	a.register(b)
	return a
}

func (a *App) queueHostUpload(ctx context.Context, m *models.Message, f *dl.TGFile, host string) {
	if a.cfg != nil {
		free, err := fsutil.FreeDiskSpace(a.cfg.DownloadsDir())
		if err == nil {
			need := uint64(a.cfg.MinFreeDiskBytes)
			if f.Size > 0 {
				need += uint64(f.Size)
			}
			if free < need {
				a.reply(ctx, m, fmt.Sprintf("Upload aborted: insufficient disk space on server (less than %s free).", status.Size(float64(a.cfg.MinFreeDiskBytes))))
				return
			}
		}
	}
	go func() {
		bgCtx := context.WithoutCancel(ctx)
		if a.hostUploadSem != nil {
			if !a.hostUploadSem.TryAcquire(1) {
				ref, _ := a.tg.Send(bgCtx, m.Chat.ID, "Upload queued… waiting for active downloads/uploads to finish.", tg.SendOpts{ReplyTo: m.ID})
				if err := a.hostUploadSem.Acquire(bgCtx, 1); err != nil {
					return
				}
				if ref.Valid() {
					_ = a.tg.Delete(bgCtx, ref)
				}
			}
			defer a.hostUploadSem.Release(1)
		}
		a.hostUpload(bgCtx, m, f, host)
	}()
}

func NewClient(cfg *config.Config, authz *auth.Authorizer, defaultHandler tgbot.HandlerFunc) (*tgbot.Bot, *tg.BotAPI, error) {
	opts := []tgbot.Option{
		tgbot.WithMiddlewares(authMiddleware(authz)),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithErrorsHandler(func(err error) { slog.Warn("telegram client error", "err", err) }),
	}
	if defaultHandler != nil {
		opts = append(opts, tgbot.WithDefaultHandler(defaultHandler))
	}
	if cfg.BotAPIURL != "" {
		opts = append(opts, tgbot.WithServerURL(cfg.BotAPIURL))
	}
	b, err := tgbot.New(cfg.BotToken, opts...)
	if err != nil {
		return nil, nil, err
	}
	api := tg.NewBotAPI(b, cfg.BotToken)
	if cfg.BotAPIURL != "" {
		api.BaseURL = cfg.BotAPIURL
	}
	if cfg.LocalHostPrefix != "" {
		api.SetLocalDirMap(cfg.LocalHostPrefix, cfg.LocalContainerPrefix)
		if err := api.ProbeLocalDirMap(context.Background(), cfg.DataDir); err != nil {
			slog.Error("TG_BOT_API_LOCAL_DIR_MAP probe failed: local telegram-bot-api server cannot access files in DataDir; check container uid:gid permissions or unset TG_BOT_API_LOCAL_DIR_MAP", "err", err)
		}
	}
	if cfg.BotAPIDataDir != "" {
		api.SetDataDir(cfg.BotAPIDataDir)
	}
	return b, api, nil
}

func authMiddleware(a *auth.Authorizer) tgbot.Middleware {
	return func(next tgbot.HandlerFunc) tgbot.HandlerFunc {
		return func(ctx context.Context, b *tgbot.Bot, u *models.Update) {
			var uid int64
			switch {
			case u.Message != nil && u.Message.From != nil:
				uid = u.Message.From.ID
			case u.CallbackQuery != nil:
				uid = u.CallbackQuery.From.ID
			}
			if a.Authorized(uid) {
				next(ctx, b, u)
				return
			}
			slog.Warn("unauthorized access attempt", "user", uid)
			switch {
			case u.CallbackQuery != nil:
				_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
					CallbackQueryID: u.CallbackQuery.ID, Text: "You are not authorized to use this bot.", ShowAlert: true})
			case u.Message != nil:
				_, _ = b.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: u.Message.Chat.ID,
					Text: "You are not authorized to use this bot."})
			}
		}
	}
}

func (a *App) register(b *tgbot.Bot) {
	cmd := func(names []string, h tgbot.HandlerFunc) {
		for _, n := range names {
			b.RegisterHandler(tgbot.HandlerTypeMessageText, n, tgbot.MatchTypeCommand, h)
		}
	}
	cmd([]string{"start", "help"}, a.cmdHelp)
	cmd([]string{"status"}, a.cmdStatus)
	cmd([]string{"cancel"}, a.cmdCancel)
	cmd([]string{"m", "mirror"}, a.cmdMirror)
	cmd([]string{"dl", "direct"}, a.cmdDirect)
	cmd([]string{"gdl", "gallerydl"}, a.cmdEngine("gallery-dl", "gdl"))
	cmd([]string{"cdl", "cyberdropdl"}, a.cmdEngine("cyberdrop-dl", "cdl"))
	cmd([]string{"mega", "meganz"}, a.cmdMega)
	cmd([]string{"tor"}, a.cmdTorrent)
	cmd([]string{"aria"}, a.cmdAria)
	cmd([]string{"ts"}, a.cmdTorrentSearch)
	cmd([]string{"gd2tg", "gdrive", "gd"}, a.cmdDrive)
	cmd([]string{"unzip"}, a.cmdUnzip)
	cmd([]string{"pdup"}, a.cmdHostUpload("pixeldrain"))
	cmd([]string{"gofile", "gfup", "gfdl", "gf2tg"}, a.cmdGofile)
	cmd([]string{"fileditch", "fdup"}, a.cmdHostUpload("fileditch"))
	cmd([]string{"gofilekey", "gofile_key"}, a.cmdKey("gofile", "GoFile"))
	cmd([]string{"pdkey", "pixeldrainkey", "pd_key"}, a.cmdKey("pixeldrain", "Pixeldrain"))
	cmd([]string{"cookies"}, a.cmdCookies)
	cmd([]string{"gdlconf"}, a.cmdGDLConf)

	cb := func(prefix string, h tgbot.HandlerFunc) {
		b.RegisterHandler(tgbot.HandlerTypeCallbackQueryData, prefix, tgbot.MatchTypePrefix, h)
	}
	cb("cancel_job:", a.cbCancel)
	cb("archive_", a.cbArchive)
	cb("convert_", a.cbConvert)
	cb("help_page:", a.cbHelp)
	cb("status:", a.cbStatus)
}

func (a *App) Default(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	m := u.Message
	if m == nil {
		return
	}
	if m.PinnedMessage != nil {
		isBotPin := false
		if a.botID != 0 {
			if m.From != nil && m.From.ID == a.botID {
				isBotPin = true
			} else if m.PinnedMessage.Message != nil && m.PinnedMessage.Message.From != nil && m.PinnedMessage.Message.From.ID == a.botID {
				isBotPin = true
			}
		} else if m.From != nil && m.From.IsBot {
			isBotPin = true
		}
		if !isBotPin && a.mgr != nil && m.PinnedMessage.Message != nil {
			if a.mgr.IsStatusMessage(m.Chat.ID, m.PinnedMessage.Message.ID) {
				isBotPin = true
			}
		}
		if isBotPin {
			_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
		}
		return
	}
	if m.NewChatMembers != nil || m.LeftChatMember != nil {
		_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
		return
	}
	if m.Caption != "" && strings.HasPrefix(m.Caption, "/") {
		switch commandOf(m.Caption) {
		case "unzip":
			a.cmdUnzip(ctx, b, u)
			return
		case "cookies":
			a.cmdCookies(ctx, b, u)
			return
		case "gdlconf":
			a.cmdGDLConf(ctx, b, u)
			return
		}
	}
	if m.ReplyToMessage != nil && m.Text != "" && !strings.HasPrefix(m.Text, "/") {
		if a.mgr.DeliverPassword(m.Chat.ID, m.ReplyToMessage.ID, strings.TrimSpace(m.Text)) {
			_ = a.tg.Delete(ctx, tg.MessageRef{ChatID: m.Chat.ID, ID: m.ID})
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

func textOf(m *models.Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

func tokens(m *models.Message) []string {
	f := strings.Fields(textOf(m))
	if len(f) == 0 {
		return nil
	}
	return f[1:]
}

func userID(m *models.Message) int64 {
	if m.From != nil {
		return m.From.ID
	}
	return 0
}

func (a *App) reply(ctx context.Context, m *models.Message, html string) {
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

func (a *App) replyKB(ctx context.Context, m *models.Message, html string, kb tg.Keyboard) tg.MessageRef {
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

func mediaOf(m *models.Message) *dl.TGFile {
	if m == nil {
		return nil
	}
	switch {
	case m.Document != nil:
		return &dl.TGFile{FileID: m.Document.FileID, Name: m.Document.FileName, Size: m.Document.FileSize}
	case m.Video != nil:
		return &dl.TGFile{FileID: m.Video.FileID, Name: m.Video.FileName, Size: m.Video.FileSize}
	case m.Audio != nil:
		return &dl.TGFile{FileID: m.Audio.FileID, Name: m.Audio.FileName, Size: m.Audio.FileSize}
	case m.Animation != nil:
		return &dl.TGFile{FileID: m.Animation.FileID, Name: m.Animation.FileName, Size: m.Animation.FileSize}
	case m.Voice != nil:
		return &dl.TGFile{FileID: m.Voice.FileID, Name: fmt.Sprintf("voice_%d.ogg", m.ID), Size: m.Voice.FileSize}
	case m.VideoNote != nil:
		return &dl.TGFile{FileID: m.VideoNote.FileID, Name: fmt.Sprintf("videonote_%d.mp4", m.ID), Size: int64(m.VideoNote.FileSize)}
	case m.Sticker != nil:
		return &dl.TGFile{FileID: m.Sticker.FileID, Name: fmt.Sprintf("sticker_%d.webp", m.ID), Size: int64(m.Sticker.FileSize)}
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1]
		return &dl.TGFile{FileID: p.FileID, Name: fmt.Sprintf("photo_%d.jpg", m.ID), Size: int64(p.FileSize)}
	}
	return nil
}

func mediaSource(m *models.Message) (*models.Message, *dl.TGFile) {
	if f := mediaOf(m.ReplyToMessage); f != nil {
		return m.ReplyToMessage, f
	}
	if f := mediaOf(m); f != nil {
		return m, f
	}
	return nil, nil
}

func (a *App) enqueue(ctx context.Context, m *models.Message, target string, args dl.Args) {
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
	a.mgr.Add(job.ID)
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

func (a *App) ack(ctx context.Context, q *models.CallbackQuery, text string, alert bool) {
	_ = a.tg.Answer(ctx, q.ID, text, alert)
}
