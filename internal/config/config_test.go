package config

import (
	"testing"
)

func TestUploadLimit(t *testing.T) {
	cDefault := &Config{}
	if got := cDefault.UploadLimit(); got != 49*1024*1024 {
		t.Errorf("default upload limit = %d; want %d", got, 49*1024*1024)
	}

	cLocal := &Config{BotAPIURL: "http://telegram-bot-api:8081"}
	wantLocal := int64(195 * 1024 * 1024 * 1024 / 100)
	if got := cLocal.UploadLimit(); got != wantLocal {
		t.Errorf("local upload limit = %d; want %d", got, wantLocal)
	}
}

func TestValidate(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err == nil {
		t.Errorf("expected error on empty config")
	}

	c.APIID = 12345
	c.APIHash = "abcdef"
	c.BotToken = "123456:ABC-DEF"
	if err := c.Validate(); err != nil {
		t.Errorf("unexpected error on valid config: %v", err)
	}
}

func TestParseIDList(t *testing.T) {
	ids := parseIDList("123, 456, invalid, 789 , ")
	if len(ids) != 3 || ids[0] != 123 || ids[1] != 456 || ids[2] != 789 {
		t.Errorf("parseIDList mismatch: got %v", ids)
	}
}

func TestPixeldrainDomain(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pixeldrain.com", "pixeldrain.com"},
		{"pixeldra.in", "pixeldra.in"},
		{"PIXELDRA.IN", "pixeldra.in"},
		{"other.domain.com", "pixeldrain.com"},
		{"", "pixeldrain.com"},
	}
	for _, tc := range cases {
		if got := pixeldrainDomain(tc.in); got != tc.want {
			t.Errorf("pixeldrainDomain(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseLocalDirMap(t *testing.T) {
	cases := []struct {
		raw      string
		wantHost string
		wantCont string
		wantOK   bool
	}{
		{"/app/data=/app/data", "/app/data", "/app/data", true},
		{" ./data = /var/data ", "./data", "/var/data", true},
		{"hostprefix=containerprefix", "hostprefix", "containerprefix", true},
		{"invalid_no_equal", "", "", false},
		{"=container", "", "", false},
		{"host=", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		h, c, ok := ParseLocalDirMap(tc.raw)
		if ok != tc.wantOK || h != tc.wantHost || c != tc.wantCont {
			t.Errorf("ParseLocalDirMap(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tc.raw, h, c, ok, tc.wantHost, tc.wantCont, tc.wantOK)
		}
	}
}

