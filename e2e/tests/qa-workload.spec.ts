import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';
import { seedPlan } from './helpers/qa';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

test('the QC workload recaps the month per person and per project, with details', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId, userId } = await seedOwnerSession('qa-workload-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { projectId, planId } = await seedPlan(orgId);
  await withDB(async (c) => {
    const run = await c.query(`INSERT INTO qa_runs (org_id, plan_id, name) VALUES ($1, $2, 'Cycle 1') RETURNING id`, [orgId, planId]);
    await c.query(
      `INSERT INTO qa_run_items (org_id, run_id, case_id, title, assignee_id, status)
       SELECT $1, $2, id, title, $3, CASE WHEN key = 1 THEN 'passed' ELSE 'failed' END FROM qa_cases WHERE project_id = $4`,
      [orgId, run.rows[0].id, userId, projectId]);
    await c.query(`INSERT INTO qa_issues (org_id, project_id, key, title, severity, reported_by) VALUES ($1, $2, 1, 'Login button misaligned', 'major', $3)`,
      [orgId, projectId, userId]);
    // Eleven more members: two pages of people.
    for (let i = 1; i <= 11; i++) {
      const u = await c.query(`INSERT INTO users (email, name, email_verified_at) VALUES ($1, $2, now()) RETURNING id`,
        [`tester${i}-${subdomain}@example.com`, `Tester ${String(i).padStart(2, '0')}`]);
      await c.query(`INSERT INTO memberships (org_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE key = 'member' AND org_id IS NULL`,
        [orgId, u.rows[0].id]);
    }
  });

  try {
    await page.goto(`${origin}/qa`);
    await page.getByRole('link', { name: 'QC workload' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'QC workload' })).toBeVisible();

    const peoplePanel = page.locator('[data-wl-panel="people"]');
    const people = peoplePanel.locator('[data-wl-person]');
    await expect(people).toHaveCount(10);
    await expect(peoplePanel.getByText('Showing 1–10 of 12')).toBeVisible();
    await peoplePanel.getByRole('button', { name: 'Next →' }).click();
    await expect(people).toHaveCount(2);

    // Searching goes back to the first page.
    await page.getByLabel('Search names').fill('e2e owner');
    await expect(people).toHaveCount(1);
    await expect(peoplePanel.getByText('Showing 1–1 of 1')).toBeVisible();

    // Sorting by Total TC puts the busiest first.
    await page.getByLabel('Search names').fill('');
    await expect(people).toHaveCount(10);
    await peoplePanel.getByRole('button', { name: /^Name/ }).click(); // Z–A
    await expect(people.first()).toContainText('Tester 11');
    await peoplePanel.getByRole('button', { name: /^Total TC/ }).click();
    await expect(people.first()).toContainText('E2E Owner');

    await page.getByRole('button', { name: 'E2E Owner' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('link', { name: 'Cycle 1' })).toBeVisible();
    await expect(dialog.getByText('BUG-1')).toBeVisible();
    await expect(dialog.getByText('Login button misaligned')).toBeVisible();
    await page.screenshot({ path: test.info().outputPath('person-modal.png'), fullPage: true });
    await dialog.getByRole('button', { name: 'Close' }).click();

    const projects = page.locator('[data-wl-panel="projects"] [data-wl-project]');
    await expect(projects).toHaveCount(1);
    await projects.getByRole('button', { name: 'Portal' }).click();
    const projectDialog = page.getByRole('dialog');
    await expect(projectDialog.getByRole('link', { name: 'Cycle 1' })).toBeVisible();
    await expect(projectDialog.getByText('E2E Owner')).toBeVisible();
    await page.screenshot({ path: test.info().outputPath('project-modal.png'), fullPage: true });
  } finally {
    await retireOrg(orgId);
  }
});
