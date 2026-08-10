-- +goose Up
-- Control plane: identity, organization list, membership, session. Read
-- before we know which tenant is speaking, so none of this is subject to
-- per-org RLS (that starts with the tenant plane in migration 00003).

CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text NOT NULL UNIQUE CHECK (email = lower(email)),
    name              text NOT NULL DEFAULT '',
    avatar_url        text,
    email_verified_at timestamptz,
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- provider is 'google', 'github', or 'oidc:<sso_provider_id>' (per-org SSO,
-- task 4.5); stored as free text rather than an enum since the oidc: form is
-- open-ended and new global providers may be added without a migration.
CREATE TABLE user_identities (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider      text NOT NULL,
    provider_sub  text NOT NULL,
    email_at_link text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_sub)
);
CREATE INDEX idx_user_identities_user_id ON user_identities (user_id);

-- Subdomain label rules follow RFC 1035 hostname labels (lowercase
-- alphanumeric, internal hyphens, 1-63 chars) plus a blanket ban on any
-- consecutive hyphen — which also rejects punycode ("xn--...") collisions
-- as a side effect, so one rule covers both concerns.
CREATE TABLE organizations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subdomain     text NOT NULL UNIQUE
                  CHECK (subdomain ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$')
                  CHECK (subdomain NOT LIKE '%--%'),
    name          text NOT NULL,
    settings      jsonb NOT NULL DEFAULT '{}'::jsonb,
    perm_epoch    integer NOT NULL DEFAULT 0,
    sso_enabled   boolean NOT NULL DEFAULT false,
    sso_enforced  boolean NOT NULL DEFAULT false,
    deleted_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- Composite-FK anchor: every tenant-plane row will carry org_id
    -- alongside a parent id, and the parent's own (id, org_id) unique pair
    -- is what lets Postgres reject a mismatched org_id at insert time
    -- (task 0.3).
    UNIQUE (id, subdomain)
);

-- A table, not a CHECK or a Go constant — so ops can add a reserved word
-- (DML) without a deploy.
CREATE TABLE reserved_subdomains (
    name   text PRIMARY KEY,
    reason text NOT NULL DEFAULT ''
);

-- Tombstones of deleted org slugs. Slugs are never reused: a stale
-- bookmark, an old invite email, or a hardcoded CI pipeline pushing to
-- "https://acme.lotus.com/ingest/..." must never resolve to a different
-- organization later.
CREATE TABLE retired_slugs (
    slug       text PRIMARY KEY,
    org_id     uuid NOT NULL,
    retired_at timestamptz NOT NULL DEFAULT now()
);

-- Roles are rows, not an enum, so custom per-organization roles (deferred
-- past v1, task 1.4) require zero schema change when they land. org_id NULL
-- marks the 4 fixed system roles seeded below.
CREATE TABLE roles (
    id     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    key    text NOT NULL,
    name   text NOT NULL,
    UNIQUE (org_id, key)
);

CREATE TABLE role_permissions (
    role_id    uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission text NOT NULL,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE memberships (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id    uuid NOT NULL REFERENCES roles (id),
    status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    sso_exempt boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);
CREATE INDEX idx_memberships_user_id ON memberships (user_id);

-- id is sha256(cookie value) in hex, never the raw cookie — the raw value
-- must never be a value any query can select back out.
CREATE TABLE sessions (
    id         text PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id     uuid REFERENCES organizations (id) ON DELETE CASCADE, -- NULL = auth-host session
    host       text NOT NULL,
    perm_epoch integer NOT NULL DEFAULT 0,
    ip         inet,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- Auth-host -> tenant-host handoff (task 1.2): an opaque single-use code,
-- never a JWT, so it can be revoked between mint and exchange and never
-- carries identity in a URL.
CREATE TABLE auth_tickets (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash    text NOT NULL UNIQUE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    verifier_hash text NOT NULL,
    return_path   text NOT NULL,
    expires_at    timestamptz NOT NULL,
    consumed_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE invitations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email      text NOT NULL CHECK (email = lower(email)),
    role_id    uuid NOT NULL REFERENCES roles (id),
    invited_by uuid REFERENCES users (id),
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- Only one live (unaccepted, unrevoked) invitation per org+email at a time.
CREATE UNIQUE INDEX uq_invitations_pending ON invitations (org_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Every organization must always have >=1 active Owner. Enforced here, not
-- just in Go, because app-layer-only rules get broken by admin tooling and
-- one-off data-fix scripts.
-- +goose StatementBegin
CREATE FUNCTION enforce_min_one_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    owner_role_id       uuid;
    was_active_owner    boolean;
    still_active_owner  boolean;
    remaining_owners    integer;
BEGIN
    SELECT id INTO owner_role_id FROM roles WHERE key = 'owner' AND org_id IS NULL;

    was_active_owner := (OLD.role_id = owner_role_id AND OLD.status = 'active');
    IF TG_OP = 'UPDATE' THEN
        still_active_owner := (NEW.role_id = owner_role_id AND NEW.status = 'active');
    ELSE
        still_active_owner := false;
    END IF;

    IF was_active_owner AND NOT still_active_owner THEN
        SELECT count(*) INTO remaining_owners
        FROM memberships
        WHERE org_id = OLD.org_id
          AND role_id = owner_role_id
          AND status = 'active'
          AND id <> OLD.id;

        IF remaining_owners = 0 THEN
            RAISE EXCEPTION 'organization % must retain at least one active Owner', OLD.org_id
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_memberships_min_one_owner
    BEFORE UPDATE OR DELETE ON memberships
    FOR EACH ROW EXECUTE FUNCTION enforce_min_one_owner();

-- Seed: the 4 fixed system roles. Custom per-org roles are schema-ready
-- (org_id column) but have no UI in v1 (task 1.4).
INSERT INTO roles (org_id, key, name) VALUES
    (NULL, 'owner',  'Owner'),
    (NULL, 'admin',  'Admin'),
    (NULL, 'member', 'Member'),
    (NULL, 'viewer', 'Viewer');

-- Seed: minimum reserved-subdomain list from task 0.5.
INSERT INTO reserved_subdomains (name, reason) VALUES
    ('www', 'reserved'), ('api', 'reserved'), ('app', 'reserved'),
    ('auth', 'auth host'), ('admin', 'reserved'), ('static', 'reserved'),
    ('assets', 'reserved'), ('cdn', 'reserved'), ('mail', 'reserved'),
    ('smtp', 'reserved'), ('ftp', 'reserved'), ('ns1', 'reserved'),
    ('ns2', 'reserved'), ('mx', 'reserved'), ('dns', 'reserved'),
    ('blog', 'reserved'), ('docs', 'reserved'), ('help', 'reserved'),
    ('support', 'reserved'), ('status', 'reserved'), ('dashboard', 'reserved'),
    ('login', 'reserved'), ('logout', 'reserved'), ('signin', 'reserved'),
    ('signup', 'reserved'), ('register', 'reserved'), ('account', 'reserved'),
    ('billing', 'reserved'), ('my', 'reserved'), ('me', 'reserved'),
    ('dev', 'reserved'), ('staging', 'reserved'), ('test', 'reserved'),
    ('demo', 'reserved'), ('sandbox', 'reserved'), ('internal', 'reserved'),
    ('secure', 'reserved'), ('vpn', 'reserved'), ('git', 'reserved'),
    ('ci', 'reserved'), ('grafana', 'reserved'), ('prometheus', 'reserved'),
    ('metrics', 'reserved'), ('monitor', 'reserved'), ('alerts', 'reserved'),
    ('lotus', 'reserved'), ('root', 'reserved'), ('system', 'reserved'),
    ('healthz', 'reserved'), ('health', 'reserved'), ('ready', 'reserved'),
    ('live', 'reserved'), ('.well-known', 'reserved');

-- +goose Down
DROP TRIGGER IF EXISTS trg_memberships_min_one_owner ON memberships;
DROP FUNCTION IF EXISTS enforce_min_one_owner();
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS auth_tickets;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS memberships;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS retired_slugs;
DROP TABLE IF EXISTS reserved_subdomains;
DROP TABLE IF EXISTS organizations;
DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS users;
