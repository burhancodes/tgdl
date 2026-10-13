package dl

import (
	"reflect"
	"testing"
)

func TestParseArgsLegacyAndJSON(t *testing.T) {
	a := ParseArgs(`{"user_id": 42, "engine": "aria2", "password": "pw", "extra_args": ["-x"]}`)
	if a.UserIDInt() != 42 || a.Engine != "aria2" || a.Password != "pw" || len(a.ExtraArgs) != 1 {
		t.Errorf("unexpected args: %+v", a)
	}
	legacy := ParseArgs(`["--foo", "bar"]`)
	if len(legacy.ExtraArgs) != 2 || legacy.ExtraArgs[0] != "--foo" {
		t.Errorf("legacy list args not parsed: %+v", legacy)
	}
	if ParseArgs("not json").UserIDInt() != 0 {
		t.Error("invalid args should yield zero value")
	}
}

func TestFirstURL(t *testing.T) {
	if got := FirstURL(`["https://a/1","https://a/2"]`); got != "https://a/1" {
		t.Errorf("got %q", got)
	}
	if got := FirstURL("https://a/1"); got != "https://a/1" {
		t.Errorf("got %q", got)
	}
}

func TestIsDirectURL(t *testing.T) {
	yes := []string{"https://x.com/file.zip", "https://x.com/v.mp4?token=1", "direct:https://x.com/a", `["https://x.com/a.pdf"]`}
	no := []string{"https://x.com/page", "https://x.com/gallery/123", ""}
	for _, u := range yes {
		if !IsDirectURL(u) {
			t.Errorf("IsDirectURL(%q) = false", u)
		}
	}
	for _, u := range no {
		if IsDirectURL(u) {
			t.Errorf("IsDirectURL(%q) = true", u)
		}
	}
}

func TestParseURLsSorted(t *testing.T) {
	got := ParseURLs(`["https://x/img10.jpg","https://x/img2.jpg"]`)
	if len(got) != 2 || got[0] != "https://x/img2.jpg" {
		t.Errorf("expected natural order, got %v", got)
	}
}

func TestSanitizeExtraArgs(t *testing.T) {
	cases := []struct {
		input []string
		want  []string
	}{
		{
			input: []string{"--config", "evil.conf", "--verbose"},
			want:  []string{"--verbose"},
		},
		{
			input: []string{"--config=evil.conf", "-v"},
			want:  []string{"-v"},
		},
		{
			input: []string{"--config-file", "evil.yaml", "--threads", "4"},
			want:  []string{"--threads", "4"},
		},
		{
			input: []string{"--config-file=evil.yaml"},
			want:  nil,
		},
		{
			input: []string{"-c", "my.conf", "--sleep", "2"},
			want:  []string{"--sleep", "2"},
		},
		{
			input: []string{"-c=my.conf"},
			want:  nil,
		},
		{
			input: []string{"-o", "/etc/passwd", "-o", "extractor.oauth=false"},
			want:  []string{"-o", "extractor.oauth=false"},
		},
		{
			input: []string{"-o=/etc/passwd", "-o=extractor.oauth=false"},
			want:  []string{"-o=extractor.oauth=false"},
		},
		{
			input: []string{"-o", "file.json", "--option", "file.yaml", "--option", "safe=1"},
			want:  []string{"--option", "safe=1"},
		},
		{
			input: []string{"--option=/root/.bashrc", "--option=safe=1"},
			want:  []string{"--option=safe=1"},
		},
	}

	for _, c := range cases {
		got := SanitizeExtraArgs(c.input)
		if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("SanitizeExtraArgs(%v) = %v, want %v", c.input, got, c.want)
		}
	}
}
