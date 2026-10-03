// Package auth implements user authorization checks.
package auth

import (
	"log/slog"
	"slices"

	"github.com/burhanverse/tgdl/internal/config"
)

// Authorizer decides which Telegram users may use the bot.
type Authorizer struct{ cfg *config.Config }

func New(cfg *config.Config) *Authorizer { return &Authorizer{cfg: cfg} }

// Authorized reports whether userID may use the bot. With no allow-list
// configured the bot runs in unrestricted mode.
func (a *Authorizer) Authorized(userID int64) bool {
	if len(a.cfg.AuthorizedIDs) == 0 {
		return true
	}
	return slices.Contains(a.cfg.AuthorizedIDs, userID)
}

// IsOwner reports whether userID is the bot owner: OWNER_ID if set, otherwise
// the first authorized user.
func (a *Authorizer) IsOwner(userID int64) bool {
	if userID == 0 {
		return false
	}
	if a.cfg.HasOwner {
		return userID == a.cfg.OwnerID
	}
	if len(a.cfg.AuthorizedIDs) > 0 {
		return userID == a.cfg.AuthorizedIDs[0]
	}
	return false
}

// WarnIfUnrestricted logs a loud warning when no allow-list is configured.
func (a *Authorizer) WarnIfUnrestricted() {
	if len(a.cfg.AuthorizedIDs) == 0 {
		slog.Warn("AUTHORIZED_USER_IDS is not configured: the bot is running in UNRESTRICTED / PUBLIC mode; any Telegram user can consume host resources")
	}
}
