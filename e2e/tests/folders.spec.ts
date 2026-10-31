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
  page.locator('[data-folder="1"]:visible').filter({ has: page.getByText(name, { exact: true }) });
const crumbs = (page: Page) => page.getByRole('navigation', { name: 'Breadcrumb' });
// The organization's name in the breadcrumb is the way back to the top level.
const topLevelCrumb = (page: Page) => crumbs(page).locator('[data-drop-folder=""]');
// Folder-view rows: a <div> whose name is a link stretched over the row.
// Only the rows of the active filter's list are visible.
const projectRow = (page: Page, name: string) =>
  page.locator('.lotus-repo-row[data-kind="project"]:visible').filter({ has: page.getByText(name, { exact: true }) });

test('folders roll up passing projects and open to show what is inside', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await seedProject(orgId, 'Linknet', 'fail', 42);
  await seedProject(orgId, 'Dignas', 'pass', 95);

  await page.goto(`${origin}/projects`);

  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('IT Services');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'IT Services')).toBeVisible();

  // Open IT Services: New folder there creates inside it by default.
  await folderRow(page, 'IT Services').click();
  await expect(page).toHaveURL(/\/projects\?folder=[0-9a-f-]{36}$/);
  const itURL = page.url();
  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('Tribe lead A');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'Tribe lead A')).toBeVisible();

  for (const slug of ['linknet', 'dignas']) {
    await page.goto(`${origin}/projects/${slug}`);
    await page.getByRole('button', { name: 'Move to folder' }).click();
    // The picker lists folders as a tree: the option shows the folder's own
    // name, indented under its parent.
    await chooseInOpenDialog(page, 'Tribe lead A');
    await page.locator('dialog[open]').getByRole('button', { name: 'Move', exact: true }).click();
    await expect(page.getByText('Project moved')).toBeVisible();
  }

  // The top level is flat: the folder, not the projects inside it.
  await page.goto(`${origin}/projects`);
  await expect(folderRow(page, 'IT Services')).toContainText('1 / 2 passing');
  await expect(folderRow(page, 'IT Services')).toContainText('2 projects · 1 failing');
  await expect(projectRow(page, 'Linknet')).toHaveCount(0);

  // "Failing" lists failing projects at any depth, with where they live.
  await page.getByText('Failing', { exact: false }).first().click();
  await expect(projectRow(page, 'Linknet')).toContainText('IT Services / Tribe lead A');
  await expect(projectRow(page, 'Dignas')).toHaveCount(0);
  await page.getByText('All', { exact: false }).first().click();

  // Click through, then back up with the breadcrumb.
  await folderRow(page, 'IT Services').click();
  await folderRow(page, 'Tribe lead A').click();
  await expect(projectRow(page, 'Linknet')).toBeVisible();
  await expect(projectRow(page, 'Dignas')).toBeVisible();
  await expect(crumbs(page)).toContainText(`${subdomain} / IT Services / Tribe lead A`);
  await expect(crumbs(page)).not.toContainText('Projects');
  await crumbs(page).getByRole('link', { name: 'IT Services' }).click();
  await expect(page).toHaveURL(itURL);
  await page.goBack();
  await expect(projectRow(page, 'Linknet')).toBeVisible();

  await retireOrg(orgId);
});

test('several projects move into a folder at once from the tree', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-bulk-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await seedProject(orgId, 'Linknet', 'fail', 42);
  await seedProject(orgId, 'Dignas', 'pass', 95);
  await seedProject(orgId, 'ANRI', 'pass', 88);
  await withDB(async (c) => {
    const it = await c.query(`INSERT INTO folders (org_id, name) VALUES ($1, 'IT Services') RETURNING id`, [orgId]);
    await c.query(`INSERT INTO folders (org_id, parent_id, name) VALUES ($1, $2, 'Tribe lead A')`, [orgId, it.rows[0].id]);
  });

  await page.goto(`${origin}/projects`);

  // The New folder trigger is a normal button, not the primary one.
  await expect(page.getByRole('button', { name: 'New folder' })).toHaveClass(/btn-lotus-secondary/);

  // A row opens its project when clicked anywhere on it.
  await projectRow(page, 'Linknet').click({ position: { x: 400, y: 10 } });
  await expect(page).toHaveURL(`${origin}/projects/linknet`);
  await page.goBack();

  // Checkboxes are hidden until "Select" turns selection on.
  await expect(page.getByRole('checkbox', { name: 'Select Linknet' })).toBeHidden();
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  await page.getByRole('checkbox', { name: 'Select Linknet' }).check();
  await page.getByRole('checkbox', { name: 'Select Dignas' }).check();
  await expect(page.getByText('2 selected')).toBeVisible();

  await page.getByRole('button', { name: 'Move to folder' }).click();
  await chooseInOpenDialog(page, 'Tribe lead A');
  // Once chosen, the box shows the full path, so nested names stay unambiguous.
  await expect(page.locator('dialog[open] .lotus-combobox').getByRole('combobox')).toHaveValue('IT Services / Tribe lead A');
  await page.locator('dialog[open]').getByRole('button', { name: 'Move', exact: true }).click();

  await expect(page.getByText('2 projects moved')).toBeVisible();
  await expect(folderRow(page, 'IT Services')).toContainText('2 projects');
  await expect(folderRow(page, 'IT Services')).toContainText('1 / 2 passing');
  // ANRI was not ticked, so it stays at the top level; the moved two left it.
  await expect(projectRow(page, 'ANRI')).toBeVisible();
  await expect(projectRow(page, 'Dignas')).toHaveCount(0);

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

  // Deleting is only offered from inside the folder, never from its row.
  await folderRow(page, payload).getByRole('button').click();
  await expect(page.locator('dialog[open]').getByRole('button', { name: 'Delete folder' })).toHaveCount(0);
  await page.locator('dialog[open]').getByRole('button', { name: 'Close' }).click();
  await folderRow(page, payload).getByRole('link').first().click();
  await page.getByRole('button', { name: 'Delete folder' }).click();

  await expect(page.getByText(`Folder ${payload} deleted`)).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { __x?: number }).__x)).toBeUndefined();

  await retireOrg(orgId);
});

test('projects and folders can be dragged into a folder and back out', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-dnd-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await seedProject(orgId, 'Linknet', 'fail', 42);
  await withDB(async (c) => {
    await c.query(`INSERT INTO folders (org_id, name) VALUES ($1, 'IT Services')`, [orgId]);
    await c.query(`INSERT INTO folders (org_id, name) VALUES ($1, 'Tribe lead A')`, [orgId]);
  });

  await page.goto(`${origin}/projects`);

  // A project onto a folder row.
  await projectRow(page, 'Linknet').dragTo(folderRow(page, 'IT Services'));
  await expect(page.getByText('Project moved')).toBeVisible();
  await expect(projectRow(page, 'Linknet')).toHaveCount(0);
  await expect(folderRow(page, 'IT Services')).toContainText('1 project');

  // A folder onto a folder row.
  await folderRow(page, 'Tribe lead A').dragTo(folderRow(page, 'IT Services'));
  await expect(page.getByText('Folder Tribe lead A moved')).toBeVisible();
  await expect(folderRow(page, 'Tribe lead A')).toHaveCount(0);

  // Inside the folder, drag the project up to the top level via the breadcrumb.
  await folderRow(page, 'IT Services').click();
  await expect(projectRow(page, 'Linknet')).toBeVisible();
  await projectRow(page, 'Linknet').dragTo(topLevelCrumb(page));
  await expect(page.getByText('Project moved')).toBeVisible();
  await expect(projectRow(page, 'Linknet')).toHaveCount(0);
  await page.goto(`${origin}/projects`);
  await expect(projectRow(page, 'Linknet')).toBeVisible();

  await retireOrg(orgId);
});
