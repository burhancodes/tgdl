package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/dl"
	"github.com/burhanverse/tgdl/internal/rename"
	"github.com/burhanverse/tgdl/internal/status"
	"github.com/burhanverse/tgdl/internal/tg"
)

type sessionKey struct {
	chatID int64
	userID int64
}

type RenameSession struct {
	ChatID        int64
	UserID        int64
	Scheme        string
	Opts          rename.Options
	AsDoc         bool
	Files         []dl.TGFile
	StatusMsgID   int
	UpdatedAt     time.Time
	debounceTimer *time.Timer
}

type RenameSessionManager struct {
	mu       sync.Mutex
	sessions map[sessionKey]*RenameSession
	cfg      *config.Config
	tg       tg.Client
	stopCh   chan struct{}
}

func NewRenameSessionManager(cfg *config.Config, tg tg.Client) *RenameSessionManager {
	m := &RenameSessionManager{
		sessions: make(map[sessionKey]*RenameSession),
		cfg:      cfg,
		tg:       tg,
		stopCh:   make(chan struct{}),
	}
	m.startSweeper()
	return m
}

func (m *RenameSessionManager) startSweeper() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.sweep()
			}
		}
	}()
}

func (m *RenameSessionManager) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	ttl := m.cfg.RenameSessionTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now := time.Now()
	for k, sess := range m.sessions {
		if now.Sub(sess.UpdatedAt) > ttl {
			if sess.debounceTimer != nil {
				sess.debounceTimer.Stop()
			}
			delete(m.sessions, k)
		}
	}
}

func (m *RenameSessionManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	for _, sess := range m.sessions {
		if sess.debounceTimer != nil {
			sess.debounceTimer.Stop()
		}
	}
	m.sessions = make(map[sessionKey]*RenameSession)
}

func (m *RenameSessionManager) Get(chatID, userID int64) (*RenameSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionKey{chatID, userID}]
	if !ok {
		return nil, false
	}
	ttl := m.cfg.RenameSessionTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if time.Since(sess.UpdatedAt) > ttl {
		if sess.debounceTimer != nil {
			sess.debounceTimer.Stop()
		}
		delete(m.sessions, sessionKey{chatID, userID})
		return nil, false
	}
	return sess, true
}

func (m *RenameSessionManager) Start(chatID, userID int64, scheme string, opts rename.Options, asDoc bool, msgID int) *RenameSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := sessionKey{chatID, userID}
	if existing, ok := m.sessions[k]; ok {
		if existing.debounceTimer != nil {
			existing.debounceTimer.Stop()
		}
	}
	sess := &RenameSession{
		ChatID:      chatID,
		UserID:      userID,
		Scheme:      scheme,
		Opts:        opts,
		AsDoc:       asDoc,
		StatusMsgID: msgID,
		UpdatedAt:   time.Now(),
	}
	m.sessions[k] = sess
	return sess
}

func (m *RenameSessionManager) AddFile(chatID, userID int64, f dl.TGFile) (added bool, atLimit bool, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := sessionKey{chatID, userID}
	sess, ok := m.sessions[k]
	if !ok {
		return false, false, 0
	}
	maxBatch := m.cfg.RenameMaxBatch
	if maxBatch <= 0 {
		maxBatch = 200
	}
	if len(sess.Files) >= maxBatch {
		return false, true, len(sess.Files)
	}
	sess.Files = append(sess.Files, f)
	sess.UpdatedAt = time.Now()
	count = len(sess.Files)

	if sess.debounceTimer == nil {
		sess.debounceTimer = time.AfterFunc(2*time.Second, func() {
			m.flushCard(chatID, userID)
		})
	}
	return true, count >= maxBatch, count
}

func (m *RenameSessionManager) flushCard(chatID, userID int64) {
	m.mu.Lock()
	k := sessionKey{chatID, userID}
	sess, ok := m.sessions[k]
	if !ok {
		m.mu.Unlock()
		return
	}
	sess.debounceTimer = nil
	scheme := sess.Scheme
	count := len(sess.Files)
	msgID := sess.StatusMsgID
	maxBatch := m.cfg.RenameMaxBatch
	m.mu.Unlock()

	if msgID == 0 {
		return
	}
	cardText := formatBatchCard(scheme, count, maxBatch)
	kb := batchKeyboard()
	_ = m.tg.Edit(context.Background(), tg.MessageRef{ChatID: chatID, ID: msgID}, cardText, tg.SendOpts{Keyboard: kb})
}

func (m *RenameSessionManager) Clear(chatID, userID int64) bool {
	m.mu.Lock()
	k := sessionKey{chatID, userID}
	sess, ok := m.sessions[k]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if sess.debounceTimer != nil {
		sess.debounceTimer.Stop()
		sess.debounceTimer = nil
	}
	sess.Files = nil
	sess.UpdatedAt = time.Now()
	scheme := sess.Scheme
	msgID := sess.StatusMsgID
	maxBatch := m.cfg.RenameMaxBatch
	m.mu.Unlock()

	if msgID != 0 {
		cardText := formatBatchCard(scheme, 0, maxBatch)
		kb := batchKeyboard()
		_ = m.tg.Edit(context.Background(), tg.MessageRef{ChatID: chatID, ID: msgID}, cardText, tg.SendOpts{Keyboard: kb})
	}
	return true
}

func (m *RenameSessionManager) SetScheme(chatID, userID int64, scheme string) bool {
	m.mu.Lock()
	k := sessionKey{chatID, userID}
	sess, ok := m.sessions[k]
	if !ok {
		m.mu.Unlock()
		return false
	}
	sess.Scheme = scheme
	sess.UpdatedAt = time.Now()
	count := len(sess.Files)
	msgID := sess.StatusMsgID
	maxBatch := m.cfg.RenameMaxBatch
	m.mu.Unlock()

	if msgID != 0 {
		cardText := formatBatchCard(scheme, count, maxBatch)
		kb := batchKeyboard()
		_ = m.tg.Edit(context.Background(), tg.MessageRef{ChatID: chatID, ID: msgID}, cardText, tg.SendOpts{Keyboard: kb})
	}
	return true
}

func (m *RenameSessionManager) Remove(chatID, userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := sessionKey{chatID, userID}
	if sess, ok := m.sessions[k]; ok {
		if sess.debounceTimer != nil {
			sess.debounceTimer.Stop()
		}
		delete(m.sessions, k)
	}
}

func formatBatchCard(scheme string, count, maxCount int) string {
	if maxCount <= 0 {
		maxCount = 200
	}
	return fmt.Sprintf("<b>Batch Rename Session</b>\n\n• <b>Scheme</b>: <code>%s</code>\n• <b>Files in batch</b>: <code>%d / %d</code>\n\n<i>Send media files (or albums) to add them to this batch.</i>",
		status.Esc(scheme), count, maxCount)
}

func batchKeyboard() tg.Keyboard {
	return tg.Keyboard{
		{
			tg.Button{Text: "Preview", Data: "rename:preview"},
			tg.Button{Text: "Apply", Data: "rename:apply"},
		},
		{
			tg.Button{Text: "Clear", Data: "rename:clear"},
			tg.Button{Text: "Cancel", Data: "rename:cancel"},
		},
	}
}
