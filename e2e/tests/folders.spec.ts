import { expect, test, type Page } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// seedProject inserts a project with one component whose SonarQube
// headline — and so the project's Lotus status — is `status` at `score`.
async function seedProject(orgId: string, name: string, status: 'pass' | 'fail', score: number): Promise<string> {
  return withDB(async (c) => {
    const p = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3) RETURNING id`,
      [orgId, name.toLowerCase(), name]);
    const comp = await c.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'api', $3, 'backend') RETURNING id`,
      [orgId, p.rows[0].id, `${name}-api`]);
    await c.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, $4::jsonb, $5, $6, now())`,
      [orgId, p.rows[0].id, comp.rows[0].id, JSON.stringify({ sonarqube: { status, score } }), status, score]);
    return p.rows[0].id as string;
  });
}

// Pick an option in the dropdown inside the dialog that is currently open.
// Scoped to the open dialog because every folder dialog on the page carries
// its own parent_id picker.
async function chooseInOpenDialog(page: Page, label: string) {
  const dialog = page.locator('dialog[open]');
  await dialog.locator('.lotus-combobox').getByRole('combobox').click();
  await dialog.getByRole('option', { name: label, exact: true }).click();
}

const folderRow = (page: Page, name: string) =>
  page.locator('[data-folder="1"]').filter({ has: page.getByText(name, { exact: true }) });
const projectRow = (page: Page, name: string) =>
  page.locator('a.lotus-repo-row').filter({ has: page.getByText(name, { exact: true }) });

test('folders roll up passing projects and remember being collapsed', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const linknet = await seedProject(orgId, 'Linknet', 'fail', 42);
  const dignas = await seedProject(orgId, 'Dignas', 'pass', 95);

  await page.goto(`${origin}/projects`);

  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('IT Services');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'IT Services')).toBeVisible();

  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('Tribe lead A');
  await chooseInOpenDialog(page, 'IT Services');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'Tribe lead A')).toBeVisible();

  for (const id of [linknet, dignas]) {
    await page.goto(`${origin}/projects/${id}`);
    await page.getByRole('button', { name: 'Move to folder' }).click();
    await chooseInOpenDialog(page, 'IT Services / Tribe lead A');
    await page.locator('dialog[open]').getByRole('button', { name: 'Move', exact: true }).click();
    await expect(page.getByText('Project moved')).toBeVisible();
  }

  await page.goto(`${origin}/projects`);
  await expect(folderRow(page, 'Tribe lead A')).toContainText('1 / 2 passing');
  await expect(folderRow(page, 'Tribe lead A')).toContainText('failing: Linknet 42');
  await expect(folderRow(page, 'IT Services')).toContainText('1 / 2 passing');

  // Collapse Tribe lead A: its projects hide, and stay hidden after a reload.
  await folderRow(page, 'Tribe lead A').getByText('Tribe lead A', { exact: true }).click();
  await expect(projectRow(page, 'Linknet')).toBeHidden();
  await page.reload();
  await expect(projectRow(page, 'Linknet')).toBeHidden();
  await expect(folderRow(page, 'Tribe lead A')).toBeVisible();

  // Expanding again brings them back.
  await folderRow(page, 'Tribe lead A').getByText('Tribe lead A', { exact: true }).click();
  await expect(projectRow(page, 'Linknet')).toBeVisible();

  await retireOrg(orgId);
});

// Review focus (final review, item 1): lotus.js's deliver() hands the
// success toast's text straight to Basecoat, which inserts it with
// innerHTML — so a folder named with markup used to run script in
// whoever's browser saw the "Folder ... deleted" toast (member -> owner
// escalation). deliver() now HTML-escapes at that boundary.
test('a folder name cannot run script through the success toast (stored XSS)', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-xss-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const payload = '<img src=x onerror="window.__x=1">';
  await seedProject(orgId, 'Placeholder', 'pass', 90);

  // hx-confirm on the delete form pops a native confirm() dialog;
  // Playwright dismisses dialogs by default, which would cancel the delete.
  page.on('dialog', (d) => d.accept());

  await page.goto(`${origin}/projects`);
  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill(payload);
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, payload)).toBeVisible();

  await folderRow(page, payload).getByRole('button').click();
  await page.locator('dialog[open]').getByRole('button', { name: 'Delete folder' }).click();

  await expect(page.getByText(`Folder ${payload} deleted`)).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { __x?: number }).__x)).toBeUndefined();

  await retireOrg(orgId);
});

// Review focus: with storage blocked the tree still renders and collapses;
// it only forgets on reload.
test('the tree still collapses when localStorage is unavailable', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-nostore-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const pid = await seedProject(orgId, 'ANRI', 'pass', 90);
  await withDB(async (c) => {
    const f = await c.query(`INSERT INTO folders (org_id, name) VALUES ($1, 'Tribe lead B') RETURNING id`, [orgId]);
    await c.query(`UPDATE projects SET folder_id = $1 WHERE id = $2`, [f.rows[0].id, pid]);
  });

  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() { throw new Error('storage blocked'); },
    });
  });
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto(`${origin}/projects`);
  await expect(projectRow(page, 'ANRI')).toBeVisible();
  await folderRow(page, 'Tribe lead B').getByText('Tribe lead B', { exact: true }).click();
  await expect(projectRow(page, 'ANRI')).toBeHidden();
  expect(errors).toEqual([]);

  await retireOrg(orgId);
});
