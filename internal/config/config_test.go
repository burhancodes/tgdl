package config

import (
	"testing"
)

func TestUploadLimit(t *testing.T) {
	c := &Config{}
	want := int64(2000 * 1024 * 1024)
	if got := c.UploadLimit(); got != want {
		t.Errorf("upload limit = %d; want %d", got, want)
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

func TestOwnerResolution(t *testing.T) {
	t.Run("explicit owner id", func(t *testing.T) {
		t.Setenv("OWNER_ID", "555")
		t.Setenv("AUTHORIZED_USER_IDS", "100,200")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.HasOwner || !cfg.HasExplicitOwner || cfg.OwnerID != 555 {
			t.Errorf("got (%v, %v, %d); want (true, true, 555)", cfg.HasOwner, cfg.HasExplicitOwner, cfg.OwnerID)
		}
	})

	t.Run("fallback to first authorized user", func(t *testing.T) {
		t.Setenv("OWNER_ID", "")
		t.Setenv("AUTHORIZED_USER_IDS", "100,200")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.HasOwner || cfg.HasExplicitOwner || cfg.OwnerID != 100 {
			t.Errorf("got (%v, %v, %d); want (true, false, 100)", cfg.HasOwner, cfg.HasExplicitOwner, cfg.OwnerID)
		}
	})

	t.Run("no owner and no authorized", func(t *testing.T) {
		t.Setenv("OWNER_ID", "")
		t.Setenv("AUTHORIZED_USER_IDS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.HasOwner || cfg.HasExplicitOwner || cfg.OwnerID != 0 {
			t.Errorf("got (%v, %v, %d); want (false, false, 0)", cfg.HasOwner, cfg.HasExplicitOwner, cfg.OwnerID)
		}
	})
}
