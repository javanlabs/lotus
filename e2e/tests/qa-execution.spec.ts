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

test('a run is created, executed from the keyboard, and its failure retested', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-exec-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { projectId, planId } = await seedPlan(orgId);

  try {
    await page.goto(`${origin}/projects/${projectId}/qa/plans/${planId}`);
    await page.getByRole('link', { name: 'New run' }).click();
    await expect(page.locator('#run-preview')).toContainText('2 cases');
    await page.locator('input[name="tester_id"]').first().check();
    await page.getByLabel('One by one, in turn').check();
    await expect(page.locator('#run-preview [data-preview-count="2"]')).toBeVisible();
    await page.getByRole('button', { name: 'Create run' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Cycle 1' })).toBeVisible();

    // Pass the first item with the p key, without leaving the page.
    const rows = page.locator('[data-item-row]');
    await rows.nth(0).getByRole('button').first().click();
    const panel1 = rows.nth(0).locator('[id^="panel-"]');
    await expect(panel1.getByRole('button', { name: 'Record result' })).toBeVisible();
    await panel1.getByRole('radiogroup', { name: 'Result' }).locator('input').last().focus(); // Skip: p must move the choice to Pass
    await page.keyboard.press('p');
    await panel1.getByRole('button', { name: 'Record result' }).click();
    await expect(rows.nth(0).locator('[id^="item-status-"]')).toHaveText('passed');
    await expect(page.locator('#run-progress')).toContainText('1/2 done');

    // Fail the second with a comment and a file.
    await rows.nth(1).getByRole('button').first().click();
    const panel2 = rows.nth(1).locator('[id^="panel-"]');
    await panel2.getByLabel('Fail').check();
    await panel2.getByLabel('Comment').fill('500 on submit');
    await panel2.getByLabel('Attach files').setInputFiles({ name: 'trace.txt', mimeType: 'text/plain', buffer: Buffer.from('stack trace') });
    await panel2.getByRole('button', { name: 'Record result' }).click();
    await expect(rows.nth(1).locator('[id^="item-status-"]')).toHaveText('failed');
    await expect(panel2.getByRole('link', { name: 'trace.txt' })).toBeVisible();

    await page.getByRole('button', { name: 'Retest failed & blocked' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Cycle 1 — retest' })).toBeVisible();
    await expect(page.locator('[data-item-row]')).toHaveCount(1);
    await expect(page.getByText('retest of')).toBeVisible();
  } finally {
    await retireOrg(orgId);
  }
});

test('a file over the limit is refused in the browser with a message, and nothing is recorded', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-exec-big-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { projectId, planId } = await seedPlan(orgId);

  try {
    await page.goto(`${origin}/projects/${projectId}/qa/plans/${planId}/runs/new`);
    await page.getByRole('button', { name: 'Create run' }).click();
    const row = page.locator('[data-item-row]').first();
    await row.getByRole('button').first().click();
    const panel = row.locator('[id^="panel-"]');
    await panel.getByLabel('Pass').check();
    // 26 MB: past the server's 25 MB request limit, so only the browser can say why.
    await panel.getByLabel('Attach files').setInputFiles({ name: 'screen.mp4', mimeType: 'video/mp4', buffer: Buffer.alloc(26 << 20) });
    await panel.getByRole('button', { name: 'Record result' }).click();
    await expect(panel.getByText('screen.mp4 is over 20 MB. Each file can be at most 20 MB.')).toBeVisible();
    await expect(row.locator('[id^="item-status-"]')).toHaveText('untested');
  } finally {
    await retireOrg(orgId);
  }
});
