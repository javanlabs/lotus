import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// seedPlan inserts a project with two cases, a scenario holding both and a
// plan over that scenario, straight into Postgres.
async function seedPlan(orgId: string): Promise<{ projectId: string; planId: string }> {
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

test('a failure is reported as an issue from its run item and triaged', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-issue-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { projectId, planId } = await seedPlan(orgId);

  try {
    await page.goto(`${origin}/projects/${projectId}/qa/plans/${planId}/runs/new`);
    await page.getByRole('button', { name: 'Create run' }).click();
    const row = page.locator('[data-item-row]').first();
    await row.getByRole('button').first().click();
    const panel = row.locator('[id^="panel-"]');
    await panel.getByLabel('Fail').check();
    await panel.getByLabel('Comment').fill('500 on submit');
    await panel.getByRole('button', { name: 'Record result' }).click();
    await expect(row.locator('[id^="item-status-"]')).toHaveText('failed');

    await panel.getByRole('link', { name: 'Report issue…' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Report issue' })).toBeVisible();
    await expect(page.locator('input[name="title"]')).toHaveValue('Fail: Login valid');
    await expect(page.locator('textarea[name="steps_to_reproduce"]')).toHaveValue('1. Open the portal → Login page');
    await page.locator('#lotus-combobox-issue-severity').getByRole('combobox').click();
    await page.getByRole('option', { name: 'critical', exact: true }).click();
    await page.getByRole('button', { name: 'Report issue' }).click();

    await expect(page.getByRole('heading', { level: 1, name: /BUG-1 Fail: Login valid/ })).toBeVisible();
    await expect(page.getByText('Cycle 1')).toBeVisible(); // found in
    await page.locator('#lotus-combobox-issue-status').getByRole('combobox').click();
    await page.getByRole('option', { name: 'open', exact: true }).click();
    await page.getByLabel('Note').fill('Confirmed on staging');
    await page.getByRole('button', { name: 'Change status' }).click();
    await expect(page.getByText('new → open')).toBeVisible();
    await expect(page.getByText('Confirmed on staging')).toBeVisible();

    await page.getByLabel('Comment').fill('Fixed in 1.4.2, please retest');
    await page.getByRole('button', { name: 'Comment', exact: true }).click();
    await expect(page.getByText('Fixed in 1.4.2, please retest')).toBeVisible();
  } finally {
    await retireOrg(orgId);
  }
});

test('an issue report with files over the limits is stopped in the browser, keeping what was typed', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-issue-big-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { projectId } = await seedPlan(orgId);

  try {
    await page.goto(`${origin}/projects/${projectId}/qa/issues/new`);
    await page.locator('input[name="title"]').fill('Upload freezes the page');
    await page.getByLabel('Attach files').setInputFiles({ name: 'screen.mp4', mimeType: 'video/mp4', buffer: Buffer.alloc(26 << 20) });
    await page.getByRole('button', { name: 'Report issue' }).click();
    await expect(page.getByText('screen.mp4 is over 20 MB. Each file can be at most 20 MB.')).toBeVisible();
    await expect(page.locator('input[name="title"]')).toHaveValue('Upload freezes the page');

    // Each under 20 MB, together over what one request can carry.
    await page.getByLabel('Attach files').setInputFiles([
      { name: 'a.png', mimeType: 'image/png', buffer: Buffer.alloc(15 << 20) },
      { name: 'b.png', mimeType: 'image/png', buffer: Buffer.alloc(15 << 20) },
    ]);
    await page.getByRole('button', { name: 'Report issue' }).click();
    await expect(page.getByText('The files add up to more than 24 MB. Attach fewer, or send them in a comment afterwards.')).toBeVisible();
    await expect(page).toHaveURL(/\/qa\/issues\/new$/);
  } finally {
    await retireOrg(orgId);
  }
});
