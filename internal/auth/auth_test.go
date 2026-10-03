package auth

import (
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
)

func TestAuthorizedUnrestricted(t *testing.T) {
	cfg := &config.Config{AuthorizedIDs: nil}
	a := New(cfg)
	if !a.Authorized(12345) {
		t.Errorf("expected user to be authorized in unrestricted mode")
	}
	a.WarnIfUnrestricted()
}

func TestAuthorizedRestricted(t *testing.T) {
	cfg := &config.Config{AuthorizedIDs: []int64{100, 200}}
	a := New(cfg)
	if !a.Authorized(100) {
		t.Errorf("expected 100 to be authorized")
	}
	if !a.Authorized(200) {
		t.Errorf("expected 200 to be authorized")
	}
	if a.Authorized(300) {
		t.Errorf("did not expect 300 to be authorized")
	}
}

func TestIsOwner(t *testing.T) {
	t.Run("explicit owner", func(t *testing.T) {
		cfg := &config.Config{OwnerID: 999, HasOwner: true, AuthorizedIDs: []int64{100, 200}}
		a := New(cfg)
		if !a.IsOwner(999) {
			t.Errorf("expected 999 to be owner")
		}
		if a.IsOwner(100) {
			t.Errorf("did not expect 100 to be owner when explicit owner is set")
		}
		if a.IsOwner(0) {
			t.Errorf("user 0 should never be owner")
		}
	})

	t.Run("first authorized user fallback", func(t *testing.T) {
		cfg := &config.Config{HasOwner: false, AuthorizedIDs: []int64{100, 200}}
		a := New(cfg)
		if !a.IsOwner(100) {
			t.Errorf("expected 100 to be fallback owner")
		}
		if a.IsOwner(200) {
			t.Errorf("did not expect 200 to be owner")
		}
	})

	t.Run("no owner and no authorized", func(t *testing.T) {
		cfg := &config.Config{HasOwner: false, AuthorizedIDs: nil}
		a := New(cfg)
		if a.IsOwner(100) {
			t.Errorf("expected no owner when none set")
		}
	})
}
