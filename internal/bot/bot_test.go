package bot

import (
	"reflect"
	"testing"
)

func TestParseFlags(t *testing.T) {
	cases := []struct {
		tokens   []string
		wantURL  []string
		wantPass string
		wantM    bool
		wantTG   bool
		wantUZ   bool
	}{
		{
			tokens:   []string{"-m", "-tg", "-uz", "-p", "secret", "https://gofile.io/d/123"},
			wantURL:  []string{"https://gofile.io/d/123"},
			wantPass: "secret",
			wantM:    true,
			wantTG:   true,
			wantUZ:   true,
		},
		{
			tokens:   []string{"--password=foo", "-mirror", "gofile:https://gofile.io/d/456"},
			wantURL:  []string{"gofile:https://gofile.io/d/456"},
			wantPass: "foo",
			wantM:    true,
			wantTG:   false,
			wantUZ:   false,
		},
		{
			tokens:   []string{"gfdl:https://gofile.io/d/789", "gf:https://gofile.io/d/abc"},
			wantURL:  []string{"gfdl:https://gofile.io/d/789", "gf:https://gofile.io/d/abc"},
			wantPass: "",
			wantM:    false,
			wantTG:   false,
			wantUZ:   false,
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
