import { createHash, randomUUID } from 'node:crypto';
import { withDB } from './db';

// seedPlan inserts a project with two cases, a scenario holding both and a
// plan over that scenario, straight into Postgres.
export async function seedPlan(orgId: string): Promise<{ projectId: string; planId: string }> {
  return withDB(async (c) => {
    const p = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1, 'portal', 'Portal') RETURNING id`, [orgId]);
    const projectId = p.rows[0].id as string;
    const sec = await c.query(`INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1, $2, 'Auth') RETURNING id`, [orgId, projectId]);
    const sc = await c.query(`INSERT INTO qa_scenarios (org_id, project_id, name, phase) VALUES ($1, $2, 'Regression core', 'regression') RETURNING id`, [orgId, projectId]);
    for (const [i, title] of ['Login valid', 'Login invalid'].entries()) {
      const cs = await c.query(
        `INSERT INTO qa_cases (org_id, project_id, section_id, key, title, steps) VALUES ($1, $2, $3, $4, $5, $6::jsonb) RETURNING id`,
        [orgId, projectId, sec.rows[0].id, i + 1, title, JSON.stringify([{ action: 'Open the portal', expected: 'Login page' }])]);
      await c.query(`INSERT INTO qa_scenario_items (org_id, scenario_id, case_id, position) VALUES ($1, $2, $3, $4)`,
        [orgId, sc.rows[0].id, cs.rows[0].id, i + 1]);
    }
    const plan = await c.query(`INSERT INTO qa_plans (org_id, project_id, name, phase) VALUES ($1, $2, 'Regression October', 'regression') RETURNING id`, [orgId, projectId]);
    await c.query(`INSERT INTO qa_plan_scenarios (org_id, plan_id, scenario_id) VALUES ($1, $2, $3)`, [orgId, plan.rows[0].id, sc.rows[0].id]);
    return { projectId, planId: plan.rows[0].id as string };
  });
}

// seedGuest adds a guest to orgId, lists it on projectId and returns a
// session cookie value for it.
export async function seedGuest(orgId: string, projectId: string, host: string): Promise<{ userId: string; sessionToken: string }> {
  return withDB(async (c) => {
    const u = await c.query(`INSERT INTO users (email, name, email_verified_at) VALUES ($1, 'Client Reviewer', now()) RETURNING id`,
      [`client-${randomUUID().slice(0, 8)}@bank.example`]);
    const userId = u.rows[0].id as string;
    await c.query(`INSERT INTO memberships (org_id, user_id, role_id) SELECT $1, $2, r.id FROM roles r WHERE r.key = 'guest' AND r.org_id IS NULL`, [orgId, userId]);
    await c.query(`INSERT INTO project_members (org_id, project_id, user_id) VALUES ($1, $2, $3)`, [orgId, projectId, userId]);
    const sessionToken = randomUUID();
    await c.query(`INSERT INTO sessions (id, user_id, org_id, host, expires_at) VALUES ($1, $2, $3, $4, now() + interval '1 day')`,
      [createHash('sha256').update(sessionToken).digest('hex'), userId, orgId, host]);
    return { userId, sessionToken };
  });
}
