package bot

import (
	"context"
	"reflect"
	"testing"

	gotdtg "github.com/gotd/td/tg"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/tg"
)

func TestParseFlags(t *testing.T) {
	cases := []struct {
		tokens    []string
		wantURL   []string
		wantPass  string
		wantM     bool
		wantTG    bool
		wantUZ    bool
		wantExtra []string
	}{
		{
			tokens:    []string{"-m", "-tg", "-uz", "-p", "secret", "https://gofile.io/d/123"},
			wantURL:   []string{"https://gofile.io/d/123"},
			wantPass:  "secret",
			wantM:     true,
			wantTG:    true,
			wantUZ:    true,
			wantExtra: nil,
		},
		{
			tokens:    []string{"--password=foo", "-mirror", "gofile:https://gofile.io/d/456"},
			wantURL:   []string{"gofile:https://gofile.io/d/456"},
			wantPass:  "foo",
			wantM:     true,
			wantTG:    false,
			wantUZ:    false,
			wantExtra: nil,
		},
		{
			tokens:    []string{"gfdl:https://gofile.io/d/789", "gf:https://gofile.io/d/abc"},
			wantURL:   []string{"gfdl:https://gofile.io/d/789", "gf:https://gofile.io/d/abc"},
			wantPass:  "",
			wantM:     false,
			wantTG:    false,
			wantUZ:    false,
			wantExtra: nil,
		},
		{
			tokens:    []string{"--config", "/etc/passwd", "-c", "my.conf", "--config-file=/bad", "-o", "foo=bar", "-o", "/etc/hosts", "--rate-limit=5M", "https://example.com"},
			wantURL:   []string{"https://example.com"},
			wantPass:  "",
			wantM:     false,
			wantTG:    false,
			wantUZ:    false,
			wantExtra: []string{"-o", "foo=bar", "--rate-limit=5M"},
		},
	}

	for _, c := range cases {
		got := parseFlags(c.tokens)
		if !reflect.DeepEqual(got.URLs, c.wantURL) {
			t.Errorf("URLs for tokens %v: got %v, want %v", c.tokens, got.URLs, c.wantURL)
		}
		if got.Password != c.wantPass {
			t.Errorf("Password: got %q, want %q", got.Password, c.wantPass)
		}
		if got.Mirror != c.wantM {
			t.Errorf("Mirror: got %v, want %v", got.Mirror, c.wantM)
		}
		if got.TG != c.wantTG {
			t.Errorf("TG: got %v, want %v", got.TG, c.wantTG)
		}
		if got.Unzip != c.wantUZ {
			t.Errorf("Unzip: got %v, want %v", got.Unzip, c.wantUZ)
		}
		if !reflect.DeepEqual(got.Extra, c.wantExtra) {
			t.Errorf("Extra for tokens %v: got %v, want %v", c.tokens, got.Extra, c.wantExtra)
		}
	}
}

func TestIsURLToken(t *testing.T) {
	yes := []string{
		"http://example.com/file",
		"https://gofile.io/d/123",
		"gofile:https://gofile.io/d/123",
		"gf:https://gofile.io/d/123",
		"gfdl:https://gofile.io/d/123",
		"gf2tg:https://gofile.io/d/123",
		"gdrive:12345",
		"mega:https://mega.nz/file/123",
		"magnet:?xt=urn:btih:123",
	}
	for _, u := range yes {
		if !isURLToken(u) {
			t.Errorf("isURLToken(%q) = false, want true", u)
		}
	}

	no := []string{
		"-m",
		"-tg",
		"-p",
		"plain_word",
		"",
	}
	for _, u := range no {
		if isURLToken(u) {
			t.Errorf("isURLToken(%q) = true, want false", u)
		}
	}
}

func TestURLsFromText(t *testing.T) {
	text := "Here are the links:\nhttps://gofile.io/d/link1\ngf:https://gofile.io/d/link2 and some text http://example.com/video.mp4"
	urls := urlsFromText(text)
	expected := []string{
		"https://gofile.io/d/link1",
		"gf:https://gofile.io/d/link2",
		"http://example.com/video.mp4",
	}
	if !reflect.DeepEqual(urls, expected) {
		t.Errorf("urlsFromText: got %v, want %v", urls, expected)
	}
}

type mockBotTGClient struct {
	tg.Client
	deleted []tg.MessageRef
}

func (m *mockBotTGClient) Send(ctx context.Context, chatID int64, html string, o tg.SendOpts) (tg.MessageRef, error) {
	return tg.MessageRef{ChatID: chatID, ID: 1}, nil
}

func (m *mockBotTGClient) Delete(ctx context.Context, ref tg.MessageRef) error {
	m.deleted = append(m.deleted, ref)
	return nil
}

func TestDefault_PinnedServiceMessageDeletion(t *testing.T) {
	tgMock := &mockBotTGClient{}
	app := &App{
		tg:    tgMock,
		botID: 12345,
	}

	updateBotPin := &Update{
		Message: &Message{
			ID:            100,
			Chat:          Chat{ID: 999},
			From:          &User{ID: 12345, IsBot: true},
			PinnedMessage: &Message{ID: 50, From: &User{ID: 12345}},
		},
	}
	app.Default(context.Background(), updateBotPin)
	if len(tgMock.deleted) != 1 || tgMock.deleted[0].ID != 100 {
		t.Fatalf("expected bot's pinned service message to be deleted, got: %+v", tgMock.deleted)
	}

	tgMock.deleted = nil
	updateUserPin := &Update{
		Message: &Message{
			ID:            101,
			Chat:          Chat{ID: 999},
			From:          &User{ID: 88888, IsBot: false},
			PinnedMessage: &Message{ID: 60, From: &User{ID: 88888}},
		},
	}
	app.Default(context.Background(), updateUserPin)
	if len(tgMock.deleted) != 0 {
		t.Fatalf("expected user's pinned service message NOT to be deleted, got: %+v", tgMock.deleted)
	}
}

func TestAuthorize_BypassServiceMessagesAndBot(t *testing.T) {
	cfg := &config.Config{AuthorizedIDs: []int64{100}}
	authz := auth.New(cfg)
	botID := int64(7974384107)
	tgMock := &mockBotTGClient{}
	app := &App{
		auth:  authz,
		botID: botID,
		tg:    tgMock,
	}
	ctx := context.Background()

	updPin := &Update{
		Message: &Message{
			Chat:          Chat{ID: 999},
			From:          &User{ID: botID},
			PinnedMessage: &Message{},
		},
	}
	if !app.authorize(ctx, updPin) {
		t.Errorf("expected pinned service message from bot to pass through authorize")
	}

	updBotMsg := &Update{
		Message: &Message{
			Chat: Chat{ID: 999},
			From: &User{ID: botID},
		},
	}
	if !app.authorize(ctx, updBotMsg) {
		t.Errorf("expected bot's own update to pass through authorize")
	}

	updAuthorized := &Update{
		Message: &Message{
			Chat: Chat{ID: 999},
			From: &User{ID: 100},
		},
	}
	if !app.authorize(ctx, updAuthorized) {
		t.Errorf("expected authorized user to pass through authorize")
	}

	updUnauthorized := &Update{
		Message: &Message{
			Chat: Chat{ID: 999},
			From: &User{ID: 200},
		},
	}
	if app.authorize(ctx, updUnauthorized) {
		t.Errorf("expected unauthorized user to be blocked by authorize")
	}
}

func TestParseAriaFlags(t *testing.T) {
	toks := []string{
		"-c", "8",
		"--split=4",
		"--select=1,3-5",
		"--cookie", "session=abcdef",
		"--speed=5M",
		"--out", "../../safe.mp4",
		"--header", "X-Custom: 123",
		"--opt", "bt-max-peers=80",
		"--opt", "dir=/etc",
		"--opt", "on-download-complete=/bin/sh",
		"--opt=rpc-secret=stolen",
		"https://example.com/file.zip",
	}

	opts, rest := parseAriaFlags(toks, "10M")
	if len(rest) != 1 || rest[0] != "https://example.com/file.zip" {
		t.Fatalf("unexpected rest: %v", rest)
	}

	if opts["max-connection-per-server"] != "8" {
		t.Errorf("expected max-connection-per-server 8, got %v", opts["max-connection-per-server"])
	}
	if opts["split"] != "4" {
		t.Errorf("expected split 4, got %v", opts["split"])
	}
	if opts["select-file"] != "1,3-5" {
		t.Errorf("expected select-file 1,3-5, got %v", opts["select-file"])
	}
	if opts["max-download-limit"] != "5M" {
		t.Errorf("expected max-download-limit 5M, got %v", opts["max-download-limit"])
	}
	if opts["out"] != "safe.mp4" {
		t.Errorf("expected out sanitized to safe.mp4, got %v", opts["out"])
	}
	if opts["bt-max-peers"] != "80" {
		t.Errorf("expected bt-max-peers 80, got %v", opts["bt-max-peers"])
	}

	headers, ok := opts["header"].([]string)
	if !ok || len(headers) != 2 {
		t.Fatalf("expected 2 headers, got %v", opts["header"])
	}
	if headers[0] != "Cookie: session=abcdef" || headers[1] != "X-Custom: 123" {
		t.Errorf("unexpected headers: %v", headers)
	}

	for _, blocked := range []string{"dir", "on-download-complete", "rpc-secret"} {
		if _, exists := opts[blocked]; exists {
			t.Errorf("blocked key %s must not be present in opts", blocked)
		}
	}
}

func TestIsAriaOptBlocked(t *testing.T) {
	cases := []struct {
		key     string
		blocked bool
	}{
		{"dir", true},
		{"DIR", true},
		{"input-file", true},
		{"save-session", true},
		{"on-download-complete", true},
		{"on-download-start", true},
		{"on-bt-download-complete", true},
		{"rpc-secret", true},
		{"rpc-listen-port", true},
		{"certificate", true},
		{"load-cookies", true},
		{"max-download-limit", false},
		{"split", false},
		{"bt-max-peers", false},
		{"stream-piece-selector", false},
	}

	for _, tc := range cases {
		if got := isAriaOptBlocked(tc.key); got != tc.blocked {
			t.Errorf("isAriaOptBlocked(%q) = %v, want %v", tc.key, got, tc.blocked)
		}
	}
}

func TestMatchCommand(t *testing.T) {
	app := &App{botUsername: "my_bot"}

	cmd, ok := app.matchCommand("/start")
	if !ok || cmd != "start" {
		t.Fatalf("expected start, got cmd=%s, ok=%v", cmd, ok)
	}

	cmd2, ok := app.matchCommand("/HELP@my_bot")
	if !ok || cmd2 != "help" {
		t.Fatalf("expected help, got cmd=%s, ok=%v", cmd2, ok)
	}

	_, ok = app.matchCommand("/help@other_bot")
	if ok {
		t.Fatal("expected matchCommand to reject command for other bot")
	}

	_, ok = app.matchCommand("not a command")
	if ok {
		t.Fatal("expected non-command to be false")
	}
}

func TestChatTypeHelpers(t *testing.T) {
	cases := []struct {
		chat        Chat
		wantPrivate bool
		wantGroup   bool
		wantChannel bool
	}{
		{Chat{ID: 12345, Type: "private"}, true, false, false},
		{Chat{ID: 12345, Type: ""}, true, false, false},
		{Chat{ID: -500, Type: "group"}, false, true, false},
		{Chat{ID: -500, Type: ""}, false, true, false},
		{Chat{ID: -1001234567890, Type: "supergroup"}, false, true, false},
		{Chat{ID: -1001234567890, Type: ""}, false, true, false},
		{Chat{ID: -1009876543210, Type: "channel"}, false, false, true},
	}

	for _, tc := range cases {
		if got := tc.chat.IsPrivate(); got != tc.wantPrivate {
			t.Errorf("Chat%+v.IsPrivate() = %v, want %v", tc.chat, got, tc.wantPrivate)
		}
		if got := tc.chat.IsGroup(); got != tc.wantGroup {
			t.Errorf("Chat%+v.IsGroup() = %v, want %v", tc.chat, got, tc.wantGroup)
		}
		if got := tc.chat.IsChannel(); got != tc.wantChannel {
			t.Errorf("Chat%+v.IsChannel() = %v, want %v", tc.chat, got, tc.wantChannel)
		}
	}
}

func TestPeerToChatID(t *testing.T) {
	uPeer := &gotdtg.PeerUser{UserID: 12345}
	cID, cType := peerToChatID(uPeer)
	if cID != 12345 || cType != "private" {
		t.Fatalf("unexpected user peer: id=%d, type=%s", cID, cType)
	}

	chatPeer := &gotdtg.PeerChat{ChatID: 678}
	cID, cType = peerToChatID(chatPeer)
	if cID != -678 || cType != "group" {
		t.Fatalf("unexpected chat peer: id=%d, type=%s", cID, cType)
	}

	chanPeer := &gotdtg.PeerChannel{ChannelID: 1000}
	cID, cType = peerToChatID(chanPeer)
	if cID != -1000000001000 || cType != "supergroup" {
		t.Fatalf("unexpected default channel peer: id=%d, type=%s", cID, cType)
	}

	entities := gotdtg.Entities{
		Channels: map[int64]*gotdtg.Channel{
			1000: {ID: 1000, Broadcast: true, Megagroup: false},
		},
	}
	cID, cType = peerToChatID(chanPeer, entities)
	if cID != -1000000001000 || cType != "channel" {
		t.Fatalf("unexpected broadcast channel peer: id=%d, type=%s", cID, cType)
	}

	entitiesMega := gotdtg.Entities{
		Channels: map[int64]*gotdtg.Channel{
			1000: {ID: 1000, Broadcast: false, Megagroup: true},
		},
	}
	cID, cType = peerToChatID(chanPeer, entitiesMega)
	if cID != -1000000001000 || cType != "supergroup" {
		t.Fatalf("unexpected megagroup channel peer: id=%d, type=%s", cID, cType)
	}
}

func TestConvertCallback_SetsChatType(t *testing.T) {
	app := &App{}
	cbUser := &gotdtg.UpdateBotCallbackQuery{
		QueryID: 999,
		UserID:  12345,
		Peer:    &gotdtg.PeerUser{UserID: 12345},
		MsgID:   42,
		Data:    []byte("test_data"),
	}
	upd := app.convertCallback(cbUser)
	if upd == nil || upd.CallbackQuery == nil || upd.CallbackQuery.Message == nil {
		t.Fatal("expected non-nil update and message")
	}
	if upd.CallbackQuery.Message.Chat.ID != 12345 {
		t.Fatalf("unexpected chat id: %d", upd.CallbackQuery.Message.Chat.ID)
	}
	if upd.CallbackQuery.Message.Chat.Type != "private" || !upd.CallbackQuery.Message.Chat.IsPrivate() {
		t.Fatalf("expected private chat type, got %+v", upd.CallbackQuery.Message.Chat)
	}

	cbGroup := &gotdtg.UpdateBotCallbackQuery{
		QueryID: 999,
		UserID:  12345,
		Peer:    &gotdtg.PeerChannel{ChannelID: 555},
		MsgID:   43,
		Data:    []byte("test_group"),
	}
	updGroup := app.convertCallback(cbGroup)
	if updGroup == nil || updGroup.CallbackQuery == nil || updGroup.CallbackQuery.Message == nil {
		t.Fatal("expected non-nil update and message")
	}
	if updGroup.CallbackQuery.Message.Chat.ID != -1000000000555 {
		t.Fatalf("unexpected channel chat id: %d", updGroup.CallbackQuery.Message.Chat.ID)
	}
	if updGroup.CallbackQuery.Message.Chat.Type != "supergroup" || !updGroup.CallbackQuery.Message.Chat.IsGroup() {
		t.Fatalf("expected supergroup chat type, got %+v", updGroup.CallbackQuery.Message.Chat)
	}
}

func TestConvertMessage_ServiceMessage(t *testing.T) {
	app := &App{botID: 777}

	pinSvc := &gotdtg.MessageService{
		ID:     50,
		PeerID: &gotdtg.PeerChannel{ChannelID: 100},
		FromID: &gotdtg.PeerUser{UserID: 777},
		Action: &gotdtg.MessageActionPinMessage{},
		ReplyTo: &gotdtg.MessageReplyHeader{
			ReplyToMsgID: 40,
		},
	}
	updPin := app.convertMessage(context.Background(), pinSvc, gotdtg.Entities{})
	if updPin == nil || updPin.Message == nil {
		t.Fatal("expected non-nil service message")
	}
	if updPin.Message.PinnedMessage == nil || updPin.Message.PinnedMessage.ID != 40 {
		t.Fatalf("expected pinned message with id 40, got %+v", updPin.Message.PinnedMessage)
	}
	if updPin.Message.Chat.ID != -1000000000100 || !updPin.Message.Chat.IsGroup() {
		t.Fatalf("expected group chat, got %+v", updPin.Message.Chat)
	}

	joinSvc := &gotdtg.MessageService{
		ID:     51,
		PeerID: &gotdtg.PeerChat{ChatID: 200},
		FromID: &gotdtg.PeerUser{UserID: 888},
		Action: &gotdtg.MessageActionChatAddUser{
			Users: []int64{888},
		},
	}
	updJoin := app.convertMessage(context.Background(), joinSvc, gotdtg.Entities{})
	if updJoin == nil || updJoin.Message == nil || !updJoin.Message.NewChatMembers {
		t.Fatalf("expected NewChatMembers to be true, got %+v", updJoin)
	}

	leaveSvc := &gotdtg.MessageService{
		ID:     52,
		PeerID: &gotdtg.PeerChat{ChatID: 200},
		FromID: &gotdtg.PeerUser{UserID: 888},
		Action: &gotdtg.MessageActionChatDeleteUser{
			UserID: 888,
		},
	}
	updLeave := app.convertMessage(context.Background(), leaveSvc, gotdtg.Entities{})
	if updLeave == nil || updLeave.Message == nil || !updLeave.Message.LeftChatMember {
		t.Fatalf("expected LeftChatMember to be true, got %+v", updLeave)
	}
}
