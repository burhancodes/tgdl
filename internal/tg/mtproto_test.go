package tg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"

	_ "modernc.org/sqlite"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestPeerStoreResolve(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "test_peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ps := NewPeerStore(db)

	uPeer, ok := ps.Resolve(123456)
	if ok {
		t.Fatal("expected ok to be false for uncached user")
	}
	if u, ok := uPeer.(*tg.InputPeerUser); !ok || u.UserID != 123456 {
		t.Fatalf("unexpected user peer: %+v", uPeer)
	}

	cPeer, ok := ps.Resolve(-1001234567890)
	if ok {
		t.Fatal("expected ok to be false for uncached channel")
	}
	if ch, ok := cPeer.(*tg.InputPeerChannel); !ok || ch.ChannelID != 1234567890 {
		t.Fatalf("unexpected channel peer: %+v", cPeer)
	}

	chatPeer, ok := ps.Resolve(-98765)
	if !ok {
		t.Fatal("expected ok to be true for basic chat")
	}
	if chat, ok := chatPeer.(*tg.InputPeerChat); !ok || chat.ChatID != 98765 {
		t.Fatalf("unexpected chat peer: %+v", chatPeer)
	}

	ps.Save(123456, 999888, "user")
	ps.Save(-1001234567890, 777666, "channel")

	uPeer2, ok := ps.Resolve(123456)
	if !ok {
		t.Fatal("expected ok to be true for cached user")
	}
	if u, ok := uPeer2.(*tg.InputPeerUser); !ok || u.UserID != 123456 || u.AccessHash != 999888 {
		t.Fatalf("unexpected cached user peer: %+v", uPeer2)
	}

	cPeer2, ok := ps.Resolve(-1001234567890)
	if !ok {
		t.Fatal("expected ok to be true for cached channel")
	}
	if ch, ok := cPeer2.(*tg.InputPeerChannel); !ok || ch.ChannelID != 1234567890 || ch.AccessHash != 777666 {
		t.Fatalf("unexpected cached channel peer: %+v", cPeer2)
	}

	ps2 := NewPeerStore(db)
	uPeer3, ok := ps2.Resolve(123456)
	if !ok {
		t.Fatal("expected ok to be true from reloaded db")
	}
	if u, ok := uPeer3.(*tg.InputPeerUser); !ok || u.UserID != 123456 || u.AccessHash != 999888 {
		t.Fatalf("unexpected reloaded user peer: %+v", uPeer3)
	}
}

func TestPeerStoreIngest(t *testing.T) {
	ps := NewPeerStore(nil)

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			100: {ID: 100, AccessHash: 555},
			101: {ID: 101, AccessHash: 777, Min: true},
		},
		Channels: map[int64]*tg.Channel{
			200: {ID: 200, AccessHash: 666},
			201: {ID: 201, AccessHash: 888, Min: true},
		},
		Chats: map[int64]*tg.Chat{
			300: {ID: 300},
		},
	}
	ps.Ingest(entities)

	uPeer, ok := ps.Resolve(100)
	if !ok {
		t.Fatal("expected user to be resolved")
	}
	if u, ok := uPeer.(*tg.InputPeerUser); !ok || u.AccessHash != 555 {
		t.Fatalf("unexpected user peer: %+v", uPeer)
	}

	uPeerMin, ok := ps.Resolve(101)
	if !ok {
		t.Fatal("expected min user to be resolved")
	}
	if u, ok := uPeerMin.(*tg.InputPeerUser); !ok || u.AccessHash != 777 {
		t.Fatalf("unexpected min user peer: %+v", uPeerMin)
	}

	cPeer, ok := ps.Resolve(-1000000000200)
	if !ok {
		t.Fatal("expected channel to be resolved")
	}
	if ch, ok := cPeer.(*tg.InputPeerChannel); !ok || ch.ChannelID != 200 || ch.AccessHash != 666 {
		t.Fatalf("unexpected channel peer: %+v", cPeer)
	}

	cPeerMin, ok := ps.Resolve(-1000000000201)
	if !ok {
		t.Fatal("expected min channel to be resolved")
	}
	if ch, ok := cPeerMin.(*tg.InputPeerChannel); !ok || ch.ChannelID != 201 || ch.AccessHash != 888 {
		t.Fatalf("unexpected min channel peer: %+v", cPeerMin)
	}

	chatPeer, ok := ps.Resolve(-300)
	if !ok {
		t.Fatal("expected chat to be resolved")
	}
	if ch, ok := chatPeer.(*tg.InputPeerChat); !ok || ch.ChatID != 300 {
		t.Fatalf("unexpected chat peer: %+v", chatPeer)
	}
}

func TestBuildMarkup(t *testing.T) {
	m := &MTProto{}

	fr, err := m.buildMarkup(SendOpts{ForceReply: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fr.(*tg.ReplyKeyboardForceReply); !ok {
		t.Fatalf("expected ForceReply markup, got %T", fr)
	}

	kb := Keyboard{
		{
			{Text: "Link", URL: "https://example.com"},
			{Text: "Button", Data: "btn_data"},
		},
	}
	rm, err := m.buildMarkup(SendOpts{Keyboard: kb})
	if err != nil {
		t.Fatal(err)
	}
	rim, ok := rm.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("expected ReplyInlineMarkup, got %T", rm)
	}
	if len(rim.Rows) != 1 || len(rim.Rows[0].Buttons) != 2 {
		t.Fatalf("unexpected row structure: %+v", rim.Rows)
	}
	b1 := rim.Rows[0].Buttons[0]
	if b1.Text != "Link" {
		t.Fatalf("unexpected text: %s", b1.Text)
	}
	if u, ok := b1.Type.(*tg.InlineButtonTypeURL); !ok || u.URL != "https://example.com" {
		t.Fatalf("unexpected button type: %+v", b1.Type)
	}
	b2 := rim.Rows[0].Buttons[1]
	if b2.Text != "Button" {
		t.Fatalf("unexpected text: %s", b2.Text)
	}
	if c, ok := b2.Type.(*tg.InlineButtonTypeCallback); !ok || string(c.Data) != "btn_data" {
		t.Fatalf("unexpected button type: %+v", b2.Type)
	}
}

func TestExtractMessageIDs(t *testing.T) {
	updShort := &tg.UpdateShortSentMessage{ID: 42}
	if id := extractMessageID(updShort); id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}

	updMsg := &tg.UpdateShortMessage{ID: 43}
	if id := extractMessageID(updMsg); id != 43 {
		t.Fatalf("expected 43, got %d", id)
	}

	upds := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{
				Message: &tg.Message{ID: 101},
			},
			&tg.UpdateNewChannelMessage{
				Message: &tg.Message{ID: 102},
			},
		},
	}
	ids := extractMessageIDs(upds)
	if len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Fatalf("unexpected message ids: %+v", ids)
	}
	if id := extractMessageID(upds); id != 101 {
		t.Fatalf("expected 101, got %d", id)
	}
}

func TestMapError(t *testing.T) {
	m := &MTProto{}

	flood := &tgerr.Error{
		Code:    420,
		Message: "FLOOD_WAIT_35",
	}
	err := m.mapError(flood)
	secs, ok := IsFlood(err)
	if !ok || secs != 35 {
		t.Fatalf("expected flood wait 35, got secs=%d, ok=%v", secs, ok)
	}

	badReq := &tgerr.Error{
		Code:    400,
		Message: "CHAT_ADMIN_REQUIRED",
	}
	err2 := m.mapError(badReq)
	msg, ok := IsBadRequest(err2)
	if !ok || msg != "CHAT_ADMIN_REQUIRED" {
		t.Fatalf("expected bad request CHAT_ADMIN_REQUIRED, got msg=%s, ok=%v", msg, ok)
	}

	stdErr := errors.New("io timeout")
	if m.mapError(stdErr) != stdErr {
		t.Fatal("expected standard error to remain untouched")
	}
}

func TestDownloadProgressWriter(t *testing.T) {
	var buf bytes.Buffer
	var current, total int64
	pw := &downloadProgressWriter{
		w:     &buf,
		total: 100,
		fn: func(c, tot int64) {
			current = c
			total = tot
		},
	}

	n, err := pw.Write([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 11 {
		t.Fatalf("expected 11, got %d", n)
	}
	if current != 11 || total != 100 {
		t.Fatalf("expected current 11, total 100, got current=%d total=%d", current, total)
	}
}

func TestUploadProgressTracker(t *testing.T) {
	var current, total int64
	upt := &uploadProgressTracker{
		fn: func(c, tot int64) {
			current = c
			total = tot
		},
		total: 200,
	}

	err := upt.Chunk(context.Background(), uploader.ProgressState{Uploaded: 50, Total: 200})
	if err != nil {
		t.Fatal(err)
	}
	if current != 50 || total != 200 {
		t.Fatalf("expected current 50, total 200, got current=%d total=%d", current, total)
	}
	_ = time.Second
}

func TestParseHTMLAndEntities(t *testing.T) {
	text := `<b>Task #a7fb60fd</b>

<blockquote>• <b>Status</b>: <code>DOWNLOADING</code>
• <b>Target</b>:
<code>mirror_tg:Gemma3-1B-IT_multi-prefill-seq_q4_ekv2048.task</code>
• <b>Auto Split</b>: <code>Enabled (2GB)</code>
</blockquote>
<b>Downloader</b>
<blockquote>• <b>Engine</b>: <code>Telegram</code>
• <b>Progress</b>: <code>1.2%</code> <code>[○○○○○○○○○○○○]</code>
• <b>Downloaded</b>: <code>6.50MB / 528.97MB</code>
• <b>Speed</b>: <code>1.06MB/s</code>
• <b>Current</b>:
<code>Gemma3-1B-IT_multi-prefill-seq_q4_ekv2048.task</code>
</blockquote>
<b>System</b>
<blockquote>• <b>CPU / RAM</b>: <code>9.3% / 27.7%</code>
• <b>Disk Free</b>: <code>120.90GB</code>
• <b>Network</b>: <code>↑159.09KB ↓5.67MB</code>
• <b>Uptime</b>: <code>9d9h56m35s</code>
</blockquote>`

	msg, entities, err := parseHTML(text)
	if err != nil {
		t.Fatalf("parseHTML failed: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected entities to be extracted")
	}

	for i := 1; i < len(entities); i++ {
		prev := entities[i-1]
		curr := entities[i]
		if curr.GetOffset() < prev.GetOffset() {
			t.Fatalf("entities not sorted by offset: [%d] off=%d < [%d] off=%d", i, curr.GetOffset(), i-1, prev.GetOffset())
		}
	}

	if entities[0].GetOffset() != 0 || entities[0].GetLength() != 14 {
		t.Fatalf("first bold entity corrupted: offset=%d len=%d", entities[0].GetOffset(), entities[0].GetLength())
	}

	u16 := utf16.Encode([]rune(msg))
	for i, ent := range entities {
		off := ent.GetOffset()
		l := ent.GetLength()
		sub := u16[off : off+l]
		if len(sub) == 0 {
			t.Fatalf("entity [%d] has zero length", i)
		}
		if sub[0] == ' ' || sub[0] == '\n' || sub[0] == '\t' || sub[0] == '\r' {
			t.Fatalf("entity [%d] has leading whitespace: %q", i, string(utf16.Decode(sub)))
		}
		if sub[len(sub)-1] == ' ' || sub[len(sub)-1] == '\n' || sub[len(sub)-1] == '\t' || sub[len(sub)-1] == '\r' {
			t.Fatalf("entity [%d] has trailing whitespace: %q", i, string(utf16.Decode(sub)))
		}
	}
}

func TestParseHTMLTrailingNewlineInBlockquote(t *testing.T) {
	text := "<b>Task #faa4cc70 Queued</b>\n\n<blockquote>• <b>Status</b>: <code>QUEUED</code>\n• <b>Target</b>:\n<code>mirror_tg:Gemma3-1B-IT_multi-prefill-seq_q4_ekv2048.task</code>\n</blockquote>"
	msg, entities, err := parseHTML(text)
	if err != nil {
		t.Fatalf("parseHTML failed: %v", err)
	}
	u16 := utf16.Encode([]rune(msg))
	for i, ent := range entities {
		off := ent.GetOffset()
		l := ent.GetLength()
		sub := u16[off : off+l]
		if sub[len(sub)-1] == '\n' || sub[len(sub)-1] == ' ' {
			t.Fatalf("entity [%d] has trailing whitespace: %q", i, string(utf16.Decode(sub)))
		}
	}
}
