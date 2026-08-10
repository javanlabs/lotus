package tenant

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/repo/global"
)

// HostKind classifies which of the three site types (task 0.5's host
// taxonomy) a request's Host header resolved to.
type HostKind int

const (
	// HostUnknown covers every rejection case: no base-domain suffix, an
	// unknown/reserved/retired slug, or a deleted organization. Handlers
	// never need to distinguish why — the response is 404 either way, so
	// a reconnaissance attempt can't tell "no such org" from "reserved
	// word" from "org exists but was deleted."
	HostUnknown HostKind = iota
	HostApex
	HostAuth
	HostTenant
)

// DevOrgHeader is the dev-only escape hatch for resolving a tenant without
// wildcard DNS or a custom Host header — e.g. `curl -H "X-Lotus-Org: acme"`.
// Only honored when config.Dev is true; see task 0.5's acceptance criteria.
const DevOrgHeader = "X-Lotus-Org"

// Resolved is the result of classifying a request's Host.
type Resolved struct {
	Kind HostKind
	Org  *global.Org // non-nil only when Kind == HostTenant
}

// Resolver turns a Host header into a Resolved classification — Layer 0 of
// the authorization chain, per task 0.5. It never falls back to any
// organization when a Host can't be classified; unresolvable input is
// always HostUnknown, never a guess.
type Resolver struct {
	cfg   config.Config
	db    *sqlx.DB
	cache *orgCache
}

// NewResolver builds a Resolver with a short-TTL org-lookup cache (30s
// default is enough to absorb request bursts without making a slug change
// or org deletion take unreasonably long to take effect).
func NewResolver(cfg config.Config, db *sqlx.DB) *Resolver {
	return &Resolver{cfg: cfg, db: db, cache: newOrgCache(30 * time.Second)}
}

// InvalidateOrg drops any cached lookup for subdomain, so a slug change or
// org deletion is reflected on the very next request instead of waiting
// out the cache TTL.
func (r *Resolver) InvalidateOrg(subdomain string) {
	r.cache.invalidate(strings.ToLower(subdomain))
}

// Middleware resolves the request's Host and stores the result on the
// fiber.Ctx for downstream handlers (via OrgFromContext / ScopeFromContext)
// and for the RequireAuth / RequireTenant guards. It never itself rejects
// a request — HostUnknown is stored like any other result — so callers
// that don't care about host classification (health checks, the apex
// marketing page) aren't forced through it.
func (r *Resolver) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		host := normalizeHost(c.Hostname())
		devHeader := ""
		if r.cfg.Dev {
			devHeader = c.Get(DevOrgHeader)
		}

		resolved, err := r.classify(c.Context(), host, devHeader)
		if err != nil {
			return err
		}

		c.Locals(localsResolvedKey, resolved)
		if resolved.Kind == HostTenant {
			c.Locals(localsScopeKey, newScope(r.db, resolved.Org.ID))
		}
		return c.Next()
	}
}

// RequireAuth 404s any request whose Host didn't resolve to the auth host,
// so a route registered for auth.lotus.com is unreachable from any tenant
// subdomain (and vice versa via RequireTenant) — closing a bug class that's
// easy to miss in review.
func RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if resolved, ok := c.Locals(localsResolvedKey).(Resolved); !ok || resolved.Kind != HostAuth {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}

// RequireTenant 404s any request whose Host didn't resolve to a known,
// live tenant organization.
func RequireTenant() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if resolved, ok := c.Locals(localsResolvedKey).(Resolved); !ok || resolved.Kind != HostTenant {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}

func (r *Resolver) classify(ctx context.Context, host, devOrgHeader string) (Resolved, error) {
	if devOrgHeader != "" {
		return r.classifyTenantLabel(ctx, strings.ToLower(devOrgHeader))
	}

	base := strings.ToLower(r.cfg.BaseDomain)
	if host == base {
		return Resolved{Kind: HostApex}, nil
	}
	if host == "auth."+base {
		return Resolved{Kind: HostAuth}, nil
	}

	suffix := "." + base
	if !strings.HasSuffix(host, suffix) || host == suffix {
		// No base-domain suffix at all: direct IP access, a misconfigured
		// proxy Host, or an unrelated domain. Reject outright rather than
		// silently resolving to any organization.
		return Resolved{Kind: HostUnknown}, nil
	}
	label := strings.TrimSuffix(host, suffix)
	if strings.Contains(label, ".") {
		// More than one label ahead of the base domain (e.g.
		// "a.b."+base) isn't a form this product resolves.
		return Resolved{Kind: HostUnknown}, nil
	}
	return r.classifyTenantLabel(ctx, label)
}

func (r *Resolver) classifyTenantLabel(ctx context.Context, label string) (Resolved, error) {
	if !validSubdomainLabel(label) {
		return Resolved{Kind: HostUnknown}, nil
	}

	if org, ok := r.cache.get(label); ok {
		return orgResolved(org), nil
	}

	reserved, err := global.IsReservedSubdomain(ctx, r.db, label)
	if err != nil {
		return Resolved{}, err
	}
	if reserved {
		return Resolved{Kind: HostUnknown}, nil
	}

	retired, err := global.IsRetiredSlug(ctx, r.db, label)
	if err != nil {
		return Resolved{}, err
	}
	if retired {
		return Resolved{Kind: HostUnknown}, nil
	}

	org, err := global.OrgBySubdomain(ctx, r.db, label)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Resolved{Kind: HostUnknown}, nil
		}
		return Resolved{}, err
	}
	if org.DeletedAt != nil {
		return Resolved{Kind: HostUnknown}, nil
	}

	r.cache.set(label, org)
	return orgResolved(org), nil
}

func orgResolved(org *global.Org) Resolved {
	return Resolved{Kind: HostTenant, Org: org}
}

// validSubdomainLabel rejects, before any query runs, the cases that
// either aren't caught by the organizations.subdomain CHECK constraint
// (task 0.2) or are cheap to reject up front: all-numeric labels (the DB
// CHECK's character class allows digits, since a label like "500" is a
// syntactically valid host label — it just must never be an org slug) and
// non-ASCII input. A valid org subdomain is always pure ASCII lowercase
// alphanumeric-and-hyphen (enforced at insert time), so rejecting non-ASCII
// here has the same effect as NFKC-normalizing then rejecting non-ASCII
// codepoints — no org can ever match a normalized-but-still-non-ASCII
// label — without adding a normalization dependency for a no-op case.
func validSubdomainLabel(label string) bool {
	if label == "" || strings.HasPrefix(label, "xn--") {
		return false
	}
	allDigits := true
	for _, r := range label {
		if r > 127 {
			return false
		}
		if r < '0' || r > '9' {
			allDigits = false
		}
	}
	return !allDigits
}

// normalizeHost lowercases host and strips a port, if present, so
// "JAVAN.lotus.com:8443" and "javan.lotus.com" classify identically.
func normalizeHost(host string) string {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// orgCache is a short-TTL, invalidate-on-write cache of subdomain -> org
// lookups, so a burst of requests to the same tenant doesn't hit Postgres
// once per request.
type orgCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]cacheEntry
}

type cacheEntry struct {
	org       *global.Org
	expiresAt time.Time
}

func newOrgCache(ttl time.Duration) *orgCache {
	return &orgCache{ttl: ttl, entries: make(map[string]cacheEntry)}
}

func (c *orgCache) get(label string) (*global.Org, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[label]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.org, true
}

func (c *orgCache) set(label string, org *global.Org) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[label] = cacheEntry{org: org, expiresAt: time.Now().Add(c.ttl)}
}

func (c *orgCache) invalidate(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, label)
}
