import { expect, test, type Page } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// seedLibrary inserts a project with one section and the given cases, keys
// 1..n, straight into Postgres (the same shortcut the other specs use).
async function seedLibrary(orgId: string, titles: string[]): Promise<string> {
  return withDB(async (c) => {
    const p = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1, 'portal', 'Portal') RETURNING id`, [orgId]);
    const projectId = p.rows[0].id as string;
    const s = await c.query(`INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1, $2, 'RFS') RETURNING id`, [orgId, projectId]);
    for (const [i, title] of titles.entries()) {
      await c.query(
        `INSERT INTO qa_cases (org_id, project_id, section_id, key, title) VALUES ($1, $2, $3, $4, $5)`,
        [orgId, projectId, s.rows[0].id, i + 1, title]);
    }
    return projectId;
  });
}

// Pick an option in a dropdown inside the dialog that is open.
async function chooseInOpenDialog(page: Page, comboboxIndex: number, label: string) {
  const dialog = page.locator('dialog[open]');
  await dialog.locator('.lotus-combobox').nth(comboboxIndex).getByRole('combobox').click();
  await dialog.getByRole('option', { name: label, exact: true }).click();
}

async function addCases(page: Page, titles: string[], group: string) {
  await page.getByRole('button', { name: 'Add cases…' }).click();
  const dialog = page.locator('dialog[open]');
  for (const t of titles) {
    await dialog.locator('#picker-results label').filter({ hasText: t }).getByRole('checkbox').check();
  }
  await chooseInOpenDialog(page, 1, group);
  await dialog.getByRole('button', { name: 'Add selected' }).click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);
}

test('a UAT scenario is built from the library in two groups', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-scenario-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const projectId = await seedLibrary(orgId, ['Create RFS', 'Approve RFS', 'Reject RFS']);

  try {
    await page.goto(`${origin}/projects/${projectId}/qa`);
    await page.getByRole('navigation', { name: 'QA' }).getByRole('link', { name: 'Scenarios' }).click();

    await page.getByRole('button', { name: 'New scenario' }).click();
    await page.locator('dialog[open] input[name="name"]').fill('UAT alur RFS');
    await chooseInOpenDialog(page, 0, 'UAT');
    await page.getByRole('button', { name: 'Create scenario' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'UAT alur RFS' })).toBeVisible();
    await expect(page.getByText('UAT · 0 cases')).toBeVisible();

    for (const name of ['Day 1', 'Day 2']) {
      await page.getByRole('button', { name: 'New group' }).click();
      await page.locator('dialog[open] input[name="name"]').fill(name);
      await page.getByRole('button', { name: 'Add group' }).click();
      await expect(page.locator(`[data-group] input[value="${name}"]`)).toBeVisible();
    }

    await addCases(page, ['Create RFS', 'Approve RFS'], 'Day 1');
    await addCases(page, ['Reject RFS'], 'Day 2');

    await expect(page.getByText('UAT · 3 cases')).toBeVisible();
    const day1 = page.locator('[data-group]').filter({ has: page.locator('input[value="Day 1"]') });
    const day2 = page.locator('[data-group]').filter({ has: page.locator('input[value="Day 2"]') });
    await expect(day1).toBeVisible();
    await expect(day2).toBeVisible();
    const items = page.locator('[data-scenario-item]');
    await expect(items).toHaveText([/Create RFS/, /Approve RFS/, /Reject RFS/]);
  } finally {
    await retireOrg(orgId);
  }
});
