package auth

import (
	"log/slog"
	"slices"

	"github.com/burhanverse/tgdl/internal/config"
)

type Authorizer struct{ cfg *config.Config }

func New(cfg *config.Config) *Authorizer { return &Authorizer{cfg: cfg} }

func (a *Authorizer) Authorized(userID int64) bool {
	a.cfg.RLock()
	defer a.cfg.RUnlock()
	if len(a.cfg.AuthorizedIDs) == 0 {
		return true
	}
	return slices.Contains(a.cfg.AuthorizedIDs, userID)
}

func (a *Authorizer) IsOwner(userID int64) bool {
	if userID == 0 {
		return false
	}
	a.cfg.RLock()
	defer a.cfg.RUnlock()
	if a.cfg.HasOwner {
		return userID == a.cfg.OwnerID
	}
	if len(a.cfg.AuthorizedIDs) > 0 {
		return userID == a.cfg.AuthorizedIDs[0]
	}
	return false
}

func (a *Authorizer) HasOwner() bool {
	a.cfg.RLock()
	defer a.cfg.RUnlock()
	return a.cfg.HasOwner || len(a.cfg.AuthorizedIDs) > 0
}

func (a *Authorizer) WarnIfUnrestricted() {
	a.cfg.RLock()
	defer a.cfg.RUnlock()
	if len(a.cfg.AuthorizedIDs) == 0 {
		slog.Warn("AUTHORIZED_USER_IDS is not configured: the bot is running in UNRESTRICTED / PUBLIC mode; any Telegram user can consume host resources")
	}
}
