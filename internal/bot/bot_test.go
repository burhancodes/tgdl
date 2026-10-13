package bot

import (
	"context"
	"reflect"
	"testing"

	"github.com/burhanverse/tgdl/internal/auth"
	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/tg"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
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

	updateBotPin := &models.Update{
		Message: &models.Message{
			ID:   100,
			Chat: models.Chat{ID: 999},
			From: &models.User{ID: 12345, IsBot: true},
			PinnedMessage: &models.MaybeInaccessibleMessage{
				Message: &models.Message{ID: 50, From: &models.User{ID: 12345}},
			},
		},
	}
	app.Default(context.Background(), nil, updateBotPin)
	if len(tgMock.deleted) != 1 || tgMock.deleted[0].ID != 100 {
		t.Fatalf("expected bot's pinned service message to be deleted, got: %+v", tgMock.deleted)
	}

	tgMock.deleted = nil
	updateUserPin := &models.Update{
		Message: &models.Message{
			ID:   101,
			Chat: models.Chat{ID: 999},
			From: &models.User{ID: 88888, IsBot: false},
			PinnedMessage: &models.MaybeInaccessibleMessage{
				Message: &models.Message{ID: 60, From: &models.User{ID: 88888}},
			},
		},
	}
	app.Default(context.Background(), nil, updateUserPin)
	if len(tgMock.deleted) != 0 {
		t.Fatalf("expected user's pinned service message NOT to be deleted, got: %+v", tgMock.deleted)
	}
}

func TestAuthMiddleware_BypassServiceMessagesAndBot(t *testing.T) {
	cfg := &config.Config{AuthorizedIDs: []int64{100}}
	authz := auth.New(cfg)
	botID := int64(7974384107)

	called := false
	next := func(ctx context.Context, b *tgbot.Bot, u *models.Update) {
		called = true
	}

	mw := authMiddleware(authz, botID)(next)
	ctx := context.Background()

	called = false
	updPin := &models.Update{
		Message: &models.Message{
			Chat:          models.Chat{ID: 999},
			From:          &models.User{ID: botID},
			PinnedMessage: &models.MaybeInaccessibleMessage{},
		},
	}
	mw(ctx, nil, updPin)
	if !called {
		t.Errorf("expected pinned service message from bot to pass through auth middleware")
	}

	called = false
	updBotMsg := &models.Update{
		Message: &models.Message{
			Chat: models.Chat{ID: 999},
			From: &models.User{ID: botID},
		},
	}
	mw(ctx, nil, updBotMsg)
	if !called {
		t.Errorf("expected bot's own update to pass through auth middleware")
	}

	called = false
	updAuthorized := &models.Update{
		Message: &models.Message{
			Chat: models.Chat{ID: 999},
			From: &models.User{ID: 100},
		},
	}
	mw(ctx, nil, updAuthorized)
	if !called {
		t.Errorf("expected authorized user to pass through auth middleware")
	}

	called = false
	updUnauthorized := &models.Update{
		Message: &models.Message{
			Chat: models.Chat{ID: 999},
			From: &models.User{ID: 200},
		},
	}
	mw(ctx, nil, updUnauthorized)
	if called {
		t.Errorf("expected unauthorized user to be blocked by auth middleware")
	}
}
