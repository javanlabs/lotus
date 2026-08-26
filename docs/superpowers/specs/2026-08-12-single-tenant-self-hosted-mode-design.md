# Single-tenant self-hosted mode + generic OIDC

**Date:** 2026-08-12
**Status:** Approved (design), not yet planned or implemented
**Relates to:** ActiveCollab project 697 — new scope, not covered by any existing task

## 1. Context

Lotus was specified end to end as a multi-tenant SaaS: three host kinds
(apex / auth / tenant), wildcard DNS, wildcard TLS via ACME DNS-01. All 33
tasks in project 697 assume that shape; task 5.2 (Deploy) specs it
explicitly.

The product intent is broader than the spec captured: Lotus should install
three ways off one codebase, the way Outline (getoutline.com) does —

1. **self-hosted** on a customer's own box,
2. **locally** for development,
3. **as SaaS** on one production server operated by us.

(1) and (2) cannot depend on wildcard DNS. A self-hoster serving at
`lotus.acme.internal`, or at a bare IP, has no base domain to carve
subdomains out of and no way to get a wildcard certificate for a private
name.

The mechanism Outline uses — and the one adopted here — is that a
self-hosted install **collapses the three hosts into one origin** and holds
exactly one organization.

## 2. Decisions

Three decisions were taken before this document was written:

| Decision | Choice | Rejected alternatives |
|---|---|---|
| Tenancy in self-hosted mode | **Exactly one org per install** | Multiple orgs by path (`/o/<slug>/`); multiple orgs with subdomains optional |
| OIDC configuration source | **Env vars now, per-org `sso_providers` later** | Single-mode-only OIDC; building both at once |
| Bootstrap | **CLI before first boot** | First-login-wins; env-var seeded |

The first is what makes the rest cheap. One org means no path prefixes, no
org switcher, no per-request tenant disambiguation — the resolver simply
always answers with the same organization.

## 3. Non-goals

- **Per-org SSO (`sso_providers`, domain verification, break-glass).** Still
  deferred, exactly as scoped out of task 4.5. Instance-wide OIDC does not
  create it and does not block it.
- **Multiple orgs on one host.** If a self-hoster wants two organizations
  they run two installs against two databases.
- **Migrating an existing install between modes.** Out of scope; a mode is
  chosen at deploy time and not changed.
- **Changing anything in Fase 2–4.** See §7.

## 4. Architecture

### 4.1 Mode selection

```
LOTUS_MODE=saas|single     # default: saas
```

Defaulting to `saas` means no currently-deployed configuration changes
behavior when this ships.

In `single` mode `LOTUS_BASE_DOMAIN` is unused. `LOTUS_URL` replaces it as
the single absolute base URL of the install (`https://lotus.acme.internal`),
which is what builds OAuth/OIDC redirect URIs. It is required in single
mode: an unset `LOTUS_URL` is a fatal startup error, not a guessed default,
because a wrong redirect URI fails at the IdP with an error the user cannot
act on.

`config.AuthHost()` and `config.TenantHost()` are meaningless in single mode
and must not be called there. They gain a mode assertion that panics rather
than silently returning a nonsense host — calling them in single mode is a
programming error, and a panic makes it a test failure instead of a
malformed link in production.

### 4.2 Resolver collapse

`tenant.HostKind` is currently a mutually exclusive enum
(`HostUnknown | HostApex | HostAuth | HostTenant`, [resolve.go:29]),
and `RequireAuth()` / `RequireTenant()` 404 each other's routes. In a
single-origin install one host must satisfy both guards, which the enum
cannot express.

**Chosen approach: mode-aware guards** (rejected: turning `Kind` into a
bitmask, which touches the enum, both guards, and every test asserting on
`Kind` for no benefit in the SaaS mode where the distinction is real).

In single mode:

- `Resolver.classify` **skips host classification entirely**. There is no
  base domain to match a `Host` header against, and a self-hoster may reach
  the app by hostname, by IP, or through a reverse proxy that rewrites
  `Host`. Any host resolves to the one organization.
- `RequireAuth()` and `RequireTenant()` both short-circuit to `c.Next()`.
- The `X-Lotus-Org` dev header is ignored — there is nothing to select
  between.
- `IsReservedSubdomain` / `IsRetiredSlug` are not consulted. Reserved words
  exist to stop a tenant claiming `auth.lotus.com`; with no subdomains
  there is nothing to protect.

The org lookup still runs through the same 30-second `orgCache`, so the
hot path keeps its shape.

**The cost of this approach**, stated plainly: "which guard applies" becomes
mode-dependent, so a route could be correctly guarded in one mode and
unguarded in the other. §8 makes that a test failure rather than a review
burden.

### 4.3 Route table

`internal/web/server.go` is 209 lines with 20 host-guarded routes, so this
is small and mechanical. Routes are split into shared and SaaS-only sets;
SaaS-only routes are **not registered at all** in single mode, so the route
table itself documents what exists where.

| Routes | Single mode | Why |
|---|---|---|
| `/login/:provider`, `/login/:provider/callback` | keep | Same origin now |
| `/handoff/start`, `/handoff/finish`, `/handoff/authorize` | **not registered** | No cross-host hop exists; the handshake + ticket dance in `internal/authn/handoff.go` has nothing to bridge |
| `/signup`, `/welcome`, `/onboarding/org`, `/onboarding/slug-check`, `/onboarding/invite` (7 routes) | **not registered** | Self-serve org creation must not exist on a self-hosted box — the org is created by `lotus init` |
| `/invite/:token`, `/invite/:token/confirm`, `/invite/:token/decline` | keep | Invitations are how members join |
| `/` (apex) | redirects to the dashboard (or to login when there is no session), replacing the Fase-0 placeholder | The single origin *is* the app; there is no separate marketing surface to land on |
| `/healthz` | keep | Already `authz.Public` and host-independent |

### 4.4 Generic OIDC provider

New `internal/authn/provider_oidc.go`, implementing the existing
`authn.Provider` interface ([identity.go:30]) alongside `provider_github.go`
and `provider_google.go`:

```go
Name() string
AuthCodeURL(state, codeChallenge string) string
Exchange(ctx, code, codeVerifier) (accessToken string, err error)
FetchIdentity(ctx, accessToken) (Identity, error)
```

This maps onto OIDC without strain: `FetchIdentity` calls the discovered
`userinfo_endpoint` and reads `sub`, `email`, `email_verified`, `name`. That
mirrors how `GoogleProvider` already works — using the REST userinfo
endpoint rather than verifying the ID token JWT against a JWKS — and keeps
it just as easy to point at an `httptest.Server` in tests.

`email_verified` matters: `ResolveUser` only links an incoming identity to
an existing user when the provider reports the email verified. Keycloak
returns `email_verified` in userinfo, so the existing account-takeover
guard carries over unchanged.

Registration follows the established pattern — a provider with an empty
client secret is simply not added to the `Registry`
(`map[string]Provider`, [handlers.go:16]), so an install with no OIDC
configured behaves exactly as today.

```
OIDC_ISSUER=https://kc.example.com/realms/acme
OIDC_CLIENT_ID=lotus
OIDC_CLIENT_SECRET=...
OIDC_DISPLAY_NAME=Keycloak                 # login button label; default "SSO"
OIDC_SCOPES=openid profile email           # default
```

One `OIDC_ISSUER` plus `.well-known/openid-configuration` discovery, rather
than four hand-set endpoint URLs. Keycloak publishes discovery, and it
removes a class of misconfiguration where the authorize and token endpoints
come from different realms. Discovery is fetched once at startup; a failure
is fatal at boot rather than at first login, so a typo surfaces in the
deploy, not in a user's browser.

Redirect URI:
- single mode: `<LOTUS_URL>/login/oidc/callback`
- saas mode: `<scheme>://auth.<LOTUS_BASE_DOMAIN>/login/oidc/callback`

Available in **both** modes as an instance-wide provider. In SaaS mode it
sits beside GitHub and Google as a third button; it is not per-customer.

PKCE is already implemented (`internal/authn/pkce.go`) and applies
unchanged.

### 4.5 Bootstrap: `lotus init`

```bash
lotus init --org-name="Javan" --slug=javan --owner-email=you@javan.co.id
```

Registered in `cmd/lotus/main.go`'s existing subcommand switch beside
`serve`, `migrate`, `rewrap-keys`, and `import-legacy`.

**The constraint that shapes this:** `global.CreateOrgWithOwner(ctx, db,
subdomain, name, ownerUserID)` requires an existing user id, and
`memberships.user_id` is `NOT NULL`. At `lotus init` time nobody has logged
in, so no user row exists. `SendInvitation` is likewise unusable — it
requires an `ActorID` and an existing org.

**Resolution — create the org immediately, defer only the membership:**

1. `lotus init` inserts the `organizations` row with **zero memberships**,
   and a single `instance_bootstrap` row holding the pending owner email.
   The `enforce_min_one_owner` trigger fires `BEFORE UPDATE OR DELETE`
   only, never on `INSERT`, so a temporarily owner-less org is a legal
   state — no schema change or trigger relaxation is needed.
2. `lotus init` fails loudly if an organization already exists. A single
   install holds exactly one.
3. Because the org exists from the start, the resolver in single mode
   always has an org to answer with. There is no "not yet initialized"
   request state to design around, and slug validity is checked at init
   time rather than at first login.
4. On the first successful login in single mode, if the bootstrap row is
   unconsumed:
   - the identity is resolved through the normal `ResolveUser` path, which
     creates the user;
   - if the login email matches `owner_email` case-insensitively **and
     arrived verified**, an Owner membership is inserted and the bootstrap
     row is marked consumed;
   - otherwise the login is rejected. Until an Owner exists, no other
     account can get in.
5. Once consumed, normal invitation rules apply and the bootstrap row is
   inert.

The verified-email requirement in step 4 is deliberate: without it, an IdP
that lets a user self-assert an arbitrary email would let anyone claim
ownership of a fresh instance.

`--slug` is optional and defaults to a slugified `--org-name`. It is
retained even in single mode because the `organizations.subdomain` column
is `NOT NULL UNIQUE` and is used in generated links and exports; it just
never appears in a hostname.

## 5. Data model changes

One new migration, `00021_instance_bootstrap.sql`:

```sql
CREATE TABLE instance_bootstrap (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),  -- at most one row
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    owner_email text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz
);
```

The `boolean PRIMARY KEY DEFAULT true CHECK (id)` idiom makes "at most one
row" a schema guarantee rather than an application convention — a second
`lotus init` fails on the primary key even if the application check is
bypassed.

No other schema changes. Notably **no changes to RLS policies, tenant-plane
tables, or the composite foreign keys** — single mode has one org, which is
the trivially isolated case of the model already in place.

## 6. Env var reference (additions)

| Var | Default | Meaning |
|---|---|---|
| `LOTUS_MODE` | `saas` | `saas` (multi-tenant, subdomains) or `single` (one org, one origin) |
| `LOTUS_URL` | — | Absolute base URL of the install. Required in single mode, unused in saas mode |
| `OIDC_ISSUER` | unset | OIDC issuer URL; discovery at `<issuer>/.well-known/openid-configuration`. Unset means OIDC is not offered |
| `OIDC_CLIENT_ID` | unset | OIDC client id |
| `OIDC_CLIENT_SECRET` | unset | OIDC client secret |
| `OIDC_DISPLAY_NAME` | `SSO` | Login button label |
| `OIDC_SCOPES` | `openid profile email` | Requested scopes |

`LOTUS_BASE_DOMAIN` remains required in saas mode and is ignored in single
mode. README.md's env table is updated in the same change.

## 7. What does not change, and why

Fase 2, 3, and 4 are untouched — projects, components, metric bindings,
credential storage, all four collectors, the scheduler, dashboard
projection, timeseries, charts, and every settings screen.

This holds because every one of those features operates on `tenant.Scope`,
which carries a resolved org id and does not care how it was resolved. RLS
enforces isolation in the database regardless of mode. The change is
confined to how a request acquires its org — Fase 0/1 surface — and stops
there.

`internal/authn/handoff.go` is likewise unmodified. It is simply not routed
in single mode.

## 8. Testing

**Route coverage in both modes.** `TestRouteCoverage`
(`internal/web/route_coverage_test.go`) is parameterized over
`LOTUS_MODE=saas` and `LOTUS_MODE=single`, each building its full app and
asserting every registered route carries an explicit
`authz.Require(...)` / `authz.Public(reason)`. This is what converts §4.2's
mode-dependent guard risk into a CI failure. Given that this test was
already described in task 1.5 as plausibly the highest-value test in the
project, extending it is the cheapest possible insurance here.

**Resolver tests.** Single mode resolves an arbitrary `Host` (hostname, IP,
proxy-rewritten) to the one org; `X-Lotus-Org` is ignored; reserved and
retired slug tables are not consulted.

**Bootstrap tests.** Second `lotus init` fails; first matching verified
login claims ownership; non-matching login is rejected while unconsumed;
unverified matching email is rejected; consumed bootstrap is inert.

**OIDC provider tests.** Pointed at an `httptest.Server` serving a
discovery document plus token and userinfo endpoints, matching how the
GitHub and Google providers are already tested.

**E2E.** A new Playwright project for single mode against a bare
`http://localhost:PORT` with no subdomain, covering login → dashboard →
invite a member. Plus a `docker-compose.yml` (none exists today) running
Keycloak with a realm-import JSON, so the OIDC path is exercised offline in
CI rather than against a live IdP.

## 9. Risks and open questions

1. **Mode-dependent route guards** (§4.2). Mitigated by dual-mode route
   coverage, but it remains the sharpest edge in this design.
2. **Task 5.2 needs amending.** It currently specs wildcard DNS + ACME
   DNS-01 as *the* deploy story. It gains a second, simpler path: single
   binary, single vhost, ordinary single-name certificate. The two deploy
   modes should be documented separately rather than as variations of one
   procedure.
3. **Task 5.3 (cross-tenant isolation) is unaffected** — it is a SaaS-mode
   concern by definition and its two-org fixture stays as specified.
4. **Open: the SaaS apex page.** Single mode is settled — `/` serves the
   app (§4.3). But SaaS mode's apex still serves the Fase-0 scaffold
   placeholder, and no task in project 697 covers building a real public
   marketing page there. That gap predates this design and is not resolved
   by it.
5. **Open: upgrade path.** Not designed. If a self-hoster later wants to
   move to hosted SaaS, that is an export/import problem, not a mode
   switch.

## 10. Impact on project 697

This is new scope. Project 697 contains no task mentioning self-hosted or
single-tenant deployment. Proposed additions, to be logged in ActiveCollab:

- **TENSION** — the spec assumes multi-tenant SaaS throughout; the product
  intent includes self-hosted single-tenant. Record that the phase plan
  grows rather than silently absorbing it.
- **4.7 Generic OIDC provider (env-var configured)** — §4.4.
- **4.8 Single-tenant mode: resolver collapse + route split** — §4.2, §4.3.
- **4.9 `lotus init` bootstrap** — §4.5, §5.
- **5.2 amendment** — second deploy path for single-name TLS.

## 11. Implementation order

The dependency chain is linear. Each step merges cleanly on its own and
leaves `main` working, though single mode is not actually usable until
step 4 — steps 1–3 change no default behavior, since `LOTUS_MODE` defaults
to `saas`:

1. `LOTUS_MODE` + `LOTUS_URL` config plumbing (no behavior change at
   default).
2. Generic OIDC provider — works in saas mode immediately, independent of
   everything else, and delivers the Keycloak capability that prompted
   this design.
3. Resolver collapse + route split + dual-mode route coverage.
4. `lotus init` + `instance_bootstrap` migration + first-login ownership
   claim.
5. Single-mode e2e + Keycloak compose file + README.
