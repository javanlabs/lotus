package authn

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"
)

// ErrNotTheOwner is returned when someone logs into a not-yet-claimed
// single-mode instance with an address that is not the one `lotus init`
// nominated. Until an Owner exists nobody else gets in — otherwise a fresh
// instance reachable from the network is a land-grab.
var ErrNotTheOwner = errors.New("authn: this instance is awaiting its first Owner login")

// ClaimOwnershipIfPending is the single-mode first-login step. It reports
// whether this login claimed ownership; false with a nil error means there
// was nothing pending (the normal steady state), so callers can invoke it
// unconditionally on every login.
//
// The verified-email requirement is the load-bearing part: without it, an
// IdP that lets a user self-assert an arbitrary address would hand
// ownership of a fresh instance to whoever asked first.
func ClaimOwnershipIfPending(ctx context.Context, db *sqlx.DB, userID uuid.UUID, id Identity) (bool, error) {
	pending, err := global.PendingBootstrap(ctx, db)
	if err != nil || pending == nil {
		return false, err
	}
	if !strings.EqualFold(strings.TrimSpace(id.Email), strings.TrimSpace(pending.OwnerEmail)) {
		return false, ErrNotTheOwner
	}
	if !id.EmailVerified {
		return false, ErrNotTheOwner
	}
	if err := global.ConsumeBootstrap(ctx, db, userID); err != nil {
		if errors.Is(err, global.ErrBootstrapExists) {
			return false, nil // lost the race; someone else claimed it
		}
		return false, err
	}
	return true, nil
}
