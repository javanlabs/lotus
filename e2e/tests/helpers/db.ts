import { createHash, randomUUID } from 'node:crypto';
import { Client } from 'pg';

// Same default as internal/config.Load()'s DatabaseURL default — local dev
// always points at the docker-compose Postgres from `make db-up`; CI's e2e
// job overrides DATABASE_URL to its Postgres service (see .gitlab-ci.yml).
const DATABASE_URL = process.env.DATABASE_URL ?? 'postgres://postgres@localhost:5418/lotus?sslmode=disable';

function sha256Hex(raw: string): string {
  return createHash('sha256').update(raw).digest('hex');
}

export interface SeededOrg {
  orgId: string;
  userId: string;
  /** Actual subdomain the org was created with — includes a random suffix appended to the caller's prefix, so callers must use this, not their original prefix, for the tenant host. */
  subdomain: string;
  /** Raw session cookie value — sha256'd server-side to match sessions.id, same as internal/authn's session lookup. */
  sessionToken: string;
}

// seedOwnerSession creates an organization, an owner user, a membership,
// and a live tenant session row directly against Postgres — the same
// shortcut internal/web's Go tests use (see seedMember/seedSession in
// internal/web/projects_test.go) to skip the OAuth flow, which isn't
// configured in dev/CI. Returns everything a test needs to set the
// `lotus_session` cookie and act as that owner.
export async function seedOwnerSession(subdomainPrefix: string, host: string): Promise<SeededOrg> {
  const client = new Client({ connectionString: DATABASE_URL });
  await client.connect();
  try {
    const subdomain = `${subdomainPrefix}-${randomUUID().slice(0, 8)}`;
    const orgRes = await client.query<{ id: string }>(
      `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`,
      [subdomain],
    );
    const orgId = orgRes.rows[0].id;

    const userRes = await client.query<{ id: string }>(
      `INSERT INTO users (email, name, email_verified_at) VALUES ($1, $2, now()) RETURNING id`,
      [`${subdomain}@example.com`, 'E2E Owner'],
    );
    const userId = userRes.rows[0].id;

    await client.query(
      `INSERT INTO memberships (org_id, user_id, role_id)
       SELECT $1, $2, r.id FROM roles r WHERE r.key = 'owner' AND r.org_id IS NULL`,
      [orgId, userId],
    );

    const sessionToken = randomUUID();
    await client.query(
      `INSERT INTO sessions (id, user_id, org_id, host, expires_at) VALUES ($1, $2, $3, $4, now() + interval '1 day')`,
      [sha256Hex(sessionToken), userId, orgId, host],
    );

    return { orgId, userId, subdomain, sessionToken };
  } finally {
    await client.end();
  }
}

// query runs a one-off statement against the same database the app is
// using — for seeding a project/component/metrics row beyond what
// seedOwnerSession covers (identical role to internal/web tests' withOrgTx,
// minus RLS since these tests connect as the unrestricted `postgres` role).
export async function withDB<T>(fn: (client: Client) => Promise<T>): Promise<T> {
  const client = new Client({ connectionString: DATABASE_URL });
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
  }
}
