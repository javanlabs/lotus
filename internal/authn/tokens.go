package authn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

const (
	patTokenPrefix     = "lotus_pat_"
	serviceTokenPrefix = "lotus_svc_"
	tokenRandomBytes   = 16 // -> 22 base64url chars, per task 1.8
	checksumLen        = 4  // base64url chars
)

// GenerateAPIToken returns a new raw token string
// ("lotus_pat_<22chars><4char checksum>") and its sha256 hex hash for
// storage. The checksum is derived from the random part alone, so
// ParseAPIToken can validate a token's shape — and reject a typo — without
// any database round trip (task 1.8's "checksum yang salah ditolak tanpa
// nyentuh database"). It also gives GitHub/GitLab secret scanning a
// precise regex to register against, per the task's own note.
func GenerateAPIToken(kind string) (raw string, hash string, err error) {
	prefix := patTokenPrefix
	if kind == "service" {
		prefix = serviceTokenPrefix
	}
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	random := base64.RawURLEncoding.EncodeToString(b)
	raw = prefix + random + checksum(random)
	return raw, sha256Hex(raw), nil
}

// checksum returns a 4-char base64url encoding of the low 24 bits of
// random's CRC32 — 3 bytes encode to exactly 4 base64 chars with no
// padding.
func checksum(random string) string {
	sum := crc32.ChecksumIEEE([]byte(random)) & 0x00FFFFFF
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], sum) // buf[0] is always 0 (24-bit value)
	return base64.RawURLEncoding.EncodeToString(buf[1:])
}

// ErrBadTokenChecksum means the token is malformed or was mistyped — this
// check never touches the database.
var ErrBadTokenChecksum = errors.New("invalid token")

// ParseAPIToken validates a raw token's shape and checksum, returning its
// kind ("pat" or "service") and sha256 hex hash for the caller to look up.
func ParseAPIToken(raw string) (kind, hash string, err error) {
	var prefix string
	switch {
	case strings.HasPrefix(raw, patTokenPrefix):
		kind, prefix = "pat", patTokenPrefix
	case strings.HasPrefix(raw, serviceTokenPrefix):
		kind, prefix = "service", serviceTokenPrefix
	default:
		return "", "", ErrBadTokenChecksum
	}
	body := raw[len(prefix):]
	if len(body) <= checksumLen {
		return "", "", ErrBadTokenChecksum
	}
	random, sum := body[:len(body)-checksumLen], body[len(body)-checksumLen:]
	if checksum(random) != sum {
		return "", "", ErrBadTokenChecksum
	}
	return kind, sha256Hex(raw), nil
}

// TokenPrefixForDisplay returns the portion of a raw token safe to store
// and show back to a user later (task 1.8: "Simpan dan tampilin
// token_prefix"), e.g. "lotus_pat_AbCd" — enough to recognize which token
// is which without reconstructing the secret.
func TokenPrefixForDisplay(raw string) string {
	if len(raw) <= 14 {
		return raw
	}
	return raw[:14]
}

// ErrBothCookieAndBearer is task 1.8's exclusivity rule: a request
// carrying both a session cookie and a Bearer token is rejected outright,
// never silently resolved to one or the other.
var ErrBothCookieAndBearer = errors.New("request must not carry both a session cookie and a bearer token")

type tokenLocalsKey int

const localsAuthHandledKey tokenLocalsKey = iota

// RequireTokenOrSession is the first of three handlers a route composes
// for task 1.8's dual auth path:
//
//	app.Get(path,
//	    tenant.RequireTenant(),
//	    authn.RequireTokenOrSession(deps),  // resolves a Bearer token, or defers
//	    authn.RequireTenantSession(deps),   // task 1.3; no-ops if already resolved
//	    authz.LoadPrincipal(deps.DB),       // task 1.4; no-ops if already resolved
//	    authz.Require("perm"), handler)
//
// A request carrying both a cookie and a Bearer header is rejected
// outright. With only a Bearer header, this handler fully resolves the
// Principal itself (PAT: intersected with the owner's *current* role,
// looked up fresh, never cached in the token — a demotion or removal
// takes effect on the very next request, no revocation needed, the same
// property task 1.3 gives session-based access. Service token: scoped by
// its own Scopes and, if pinned, by project/component — presenting it on
// the wrong org's host is rejected as 404, matching every other cross-org
// object check in this codebase) and marks the request as handled, so
// RequireTenantSession and authz.LoadPrincipal — which know nothing about
// tokens — correctly no-op instead of demanding a cookie that will never
// come. With neither header, this is a no-op and the normal session path
// runs unchanged.
func RequireTokenOrSession(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get(fiber.HeaderAuthorization)
		hasBearer := strings.HasPrefix(authHeader, "Bearer ")
		hasCookie := c.Cookies(cookieName(tenantSessionCookieBase, deps.CookieSecure)) != ""

		if hasBearer && hasCookie {
			return fiber.NewError(fiber.StatusBadRequest, ErrBothCookieAndBearer.Error())
		}
		if !hasBearer {
			return c.Next()
		}

		raw := strings.TrimPrefix(authHeader, "Bearer ")
		kind, hash, err := ParseAPIToken(raw)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
		}
		tok, err := global.APITokenByHash(c.Context(), deps.DB, hash)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired token")
		}

		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}

		principal, err := principalForToken(c.Context(), deps, kind, tok, org.ID)
		if err != nil {
			return err
		}
		if !ipAllowed(tok.IPAllowlist, c.IP()) {
			return fiber.NewError(fiber.StatusForbidden, "source IP not allowed for this token")
		}

		_ = global.TouchAPITokenUsage(c.Context(), deps.DB, tok.ID, c.IP())
		authz.SetPrincipal(c, principal)
		c.Locals(localsAuthHandledKey, true)
		return c.Next()
	}
}

// authHandled reports whether RequireTokenOrSession already resolved this
// request's Principal — used by RequireTenantSession and
// authz.LoadPrincipal to skip themselves on the token path.
func authHandled(c *fiber.Ctx) bool {
	handled, _ := c.Locals(localsAuthHandledKey).(bool)
	return handled
}

func principalForToken(ctx context.Context, deps Deps, kind string, tok *global.APIToken, requestOrgID uuid.UUID) (authz.Principal, error) {
	switch kind {
	case "pat":
		membership, err := global.ActiveMembership(ctx, deps.DB, *tok.UserID, requestOrgID)
		if err != nil {
			return authz.Principal{}, fiber.NewError(fiber.StatusForbidden, "token owner is not a member of this organization")
		}
		return authz.Principal{
			Kind: authz.KindToken, UserID: *tok.UserID, OrgID: requestOrgID,
			Role: membership.RoleKey, TokenScopes: tok.Scopes,
		}, nil
	case "service":
		if tok.OrgID == nil || *tok.OrgID != requestOrgID {
			// A service token minted for org A presented on org B's host —
			// same 404 posture as a cross-org object id, not 403.
			return authz.Principal{}, fiber.ErrNotFound
		}
		p := authz.Principal{Kind: authz.KindToken, OrgID: requestOrgID, TokenScopes: tok.Scopes}
		// Pin enforcement against a specific requested resource is the
		// consuming endpoint's job (task 3.4's ingest endpoint) — it knows
		// which component the request is about; this middleware doesn't.
		if tok.ProjectID != nil {
			p.PinnedProjectID = *tok.ProjectID
		}
		if tok.ComponentID != nil {
			p.PinnedComponentID = *tok.ComponentID
		}
		return p, nil
	default:
		return authz.Principal{}, fiber.NewError(fiber.StatusUnauthorized, "invalid token")
	}
}

func ipAllowed(allowlist []string, ip string) bool {
	if len(allowlist) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, entry := range allowlist {
		if !strings.Contains(entry, "/") {
			if entry == ip {
				return true
			}
			continue
		}
		_, cidr, err := net.ParseCIDR(entry)
		if err == nil && cidr.Contains(parsed) {
			return true
		}
	}
	return false
}
