import { expect, test, type Page } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// Two projects, one component each: Payments (tag PROJECT) with ledger-api
// failing, AlurKerja (tag PRODUCT) with studio-web passing.
async function seedTagged(orgId: string) {
  await withDB(async (c) => {
    const one = async (q: string, a: unknown[]) => (await c.query<{ id: string }>(q, a)).rows[0].id;
    const project = async (slug: string, name: string, comp: string, status: string, tag: string) => {
      const p = await one(`INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3) RETURNING id`, [orgId, slug, name]);
      const cid = await one(
        `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $3, 'backend') RETURNING id`,
        [orgId, p, comp],
      );
      await c.query(
        `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
         VALUES ($1, $2, $3, jsonb_build_object('sonarqube', jsonb_build_object('status', $4::text, 'score', 70, 'updated_at', now())), $4, 70, now())`,
        [orgId, p, cid, status],
      );
      const t = await one(
        `INSERT INTO tags (org_id, name) VALUES ($1, $2)
         ON CONFLICT (org_id, lower(name)) DO UPDATE SET name = tags.name RETURNING id`,
        [orgId, tag],
      );
      await c.query(`INSERT INTO project_tags (org_id, project_id, tag_id) VALUES ($1, $2, $3)`, [orgId, p, t]);
    };
    await project('payments', 'Payments', 'ledger-api', 'fail', 'PROJECT');
    await project('alurkerja', 'AlurKerja', 'studio-web', 'pass', 'PRODUCT');
  });
}

// The tag filter is a dropdown of checkboxes: tick the given names (and
// only those), then apply.
async function chooseTags(page: Page, ...names: string[]) {
  const filter = page.getByTestId('tag-filter');
  await filter.getByTestId('tag-filter-toggle').click();
  await expect(filter.getByRole('group', { name: 'Filter by tag' })).toBeVisible();
  for (const box of await filter.locator('input[type=checkbox]').all()) {
    const want = names.includes((await box.getAttribute('value')) ?? '');
    if ((await box.isChecked()) !== want) await box.setChecked(want);
  }
  await filter.getByRole('button', { name: 'Apply' }).click();
}

// A tag's checkbox, found whether or not the menu is open (getByRole
// would skip it while the menu is closed).
const tagBox = (page: Page, name: string) =>
  page.getByTestId('tag-filter').locator(`input[type=checkbox][value="${name}"]`);

async function signIn(page: Page, prefix: string) {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession(prefix, 'placeholder');
  const origin = tenantOrigin(subdomain);
  await page.context().addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  return { orgId, origin };
}

test('dashboard narrows to the chosen tags', async ({ page }) => {
  const { orgId, origin } = await signIn(page, 'tags-dash-e2e');
  await seedTagged(orgId);

  await page.goto(`${origin}/dashboard`);
  const map = page.getByTestId('health-map');
  const toggle = page.getByTestId('tag-filter-toggle');
  await expect(map.getByRole('link', { name: 'ledger-api' })).toBeVisible();
  await expect(map.getByRole('link', { name: 'studio-web' })).toBeVisible();
  await expect(toggle).toContainText('All tags');
  // Closed until asked: the checkboxes are not on the page's surface.
  await expect(tagBox(page, 'PROJECT')).toBeHidden();

  await chooseTags(page, 'PROJECT');
  await expect(page).toHaveURL(`${origin}/dashboard?tag=PROJECT`);
  await expect(map.getByRole('link', { name: 'ledger-api' })).toBeVisible();
  await expect(map.getByRole('link', { name: 'studio-web' })).toHaveCount(0);
  await expect(toggle).toContainText('PROJECT');
  await expect(tagBox(page, 'PROJECT')).toBeChecked();
  await expect(tagBox(page, 'PRODUCT')).not.toBeChecked();

  // Several at once, in one load: tick both, then click away.
  await toggle.click();
  await tagBox(page, 'PRODUCT').check();
  await page.getByRole('heading', { name: 'Dashboard' }).click();
  await expect(page).toHaveURL(/[?&]tag=PRODUCT/);
  await expect(page).toHaveURL(/[?&]tag=PROJECT/);
  await expect(map.getByRole('link', { name: 'ledger-api' })).toBeVisible();
  await expect(map.getByRole('link', { name: 'studio-web' })).toBeVisible();
  await expect(toggle).toContainText('PRODUCT, PROJECT');
  await expect(tagBox(page, 'PROJECT')).toBeChecked();
  await expect(tagBox(page, 'PRODUCT')).toBeChecked();

  // Opening and closing without a change does not reload.
  await toggle.click();
  await page.keyboard.press('Escape');
  await expect(page).toHaveURL(/[?&]tag=PRODUCT/);

  await toggle.click();
  await page.getByTestId('tag-filter').getByRole('link', { name: 'All tags' }).click();
  await expect(page).toHaveURL(`${origin}/dashboard`);
  await expect(toggle).toContainText('All tags');

  // An old bookmark or a typo is All, not an empty fleet.
  await page.goto(`${origin}/dashboard?tag=nope`);
  await expect(map.getByRole('link', { name: 'ledger-api' })).toBeVisible();
  await expect(map.getByRole('link', { name: 'studio-web' })).toBeVisible();
  await expect(toggle).toContainText('All tags');
});

test('projects list is flat and filters by tag', async ({ page }) => {
  const { orgId, origin } = await signIn(page, 'tags-list-e2e');
  await seedTagged(orgId);
  // Payments sits in a folder, as production's projects do: the list must
  // still show it at the top level.
  await withDB(async (c) => {
    const f = await c.query<{ id: string }>(`INSERT INTO folders (org_id, name) VALUES ($1, 'ITSD') RETURNING id`, [orgId]);
    await c.query(`UPDATE projects SET folder_id = $2 WHERE org_id = $1 AND slug = 'payments'`, [orgId, f.rows[0].id]);
  });

  await page.goto(`${origin}/projects`);
  const payments = page.locator('a.lotus-repo-row', { hasText: 'Payments' });
  const alur = page.locator('a.lotus-repo-row', { hasText: 'AlurKerja' });
  await expect(payments).toBeVisible();
  await expect(alur).toBeVisible();
  await expect(payments.locator('.lotus-tag')).toHaveText('PROJECT');
  await expect(alur.locator('.lotus-tag')).toHaveText('PRODUCT');
  await expect(page.getByText('Move to folder')).toHaveCount(0);
  await expect(page.getByText('New folder')).toHaveCount(0);

  // The filter sits in the header row, beside the title.
  await expect(page.locator('header').getByTestId('tag-filter')).toBeVisible();
  await chooseTags(page, 'PROJECT');
  await expect(page).toHaveURL(`${origin}/projects?tag=PROJECT`);
  await expect(payments).toBeVisible();
  await expect(alur).toHaveCount(0);

  // A new project has no folder to pick and lands in the list.
  await page.goto(`${origin}/projects/new`);
  await expect(page.getByText('Folder', { exact: false })).toHaveCount(0);
  await page.locator('input[name="name"]').fill('Billing');
  await page.locator('input[name="slug"]').fill('billing');
  await page.getByRole('button', { name: 'Create project' }).click();
  await expect(page).toHaveURL(`${origin}/projects/billing`);
  await page.goto(`${origin}/projects`);
  await expect(page.locator('a.lotus-repo-row', { hasText: 'Billing' })).toBeVisible();
});

test('owner tags a project from its page', async ({ page }) => {
  const { orgId, origin } = await signIn(page, 'tags-edit-e2e');
  await seedTagged(orgId);

  await page.goto(`${origin}/projects/payments`);
  // Read-only under the title; the editor waits behind the settings gear.
  const pills = page.getByTestId('project-tag-pills');
  await expect(pills.locator('.lotus-tag', { hasText: 'PROJECT' })).toBeVisible();
  const tags = page.getByTestId('project-tags');
  await expect(tags).toBeHidden();
  await page.getByTestId('project-settings-toggle').click();
  await expect(tags.locator('.lotus-tag', { hasText: 'PROJECT' })).toBeVisible();
  await tags.getByLabel('Tag name').fill('XLSMART');
  await tags.getByRole('button', { name: 'Add' }).click();
  await expect(page.getByTestId('project-tags').locator('.lotus-tag', { hasText: 'XLSMART' })).toBeVisible();
  // The header's pills follow without a reload, and the popover stays open.
  await expect(pills.locator('.lotus-tag', { hasText: 'XLSMART' })).toBeVisible();

  // A blank name is refused inside the panel, not silently dropped.
  await page.getByTestId('project-tags').getByLabel('Tag name').fill('   ');
  await page.getByTestId('project-tags').getByRole('button', { name: 'Add' }).click();
  await expect(page.getByTestId('project-tags').getByRole('alert')).toContainText('1–40 characters');

  await page.goto(`${origin}/dashboard`);
  await expect(tagBox(page, 'XLSMART')).toHaveCount(1);

  // Removing it from its only project takes the chip with it.
  await page.goto(`${origin}/projects/payments`);
  await page.getByTestId('project-settings-toggle').click();
  await page.getByTestId('project-tags').getByRole('button', { name: 'Remove tag XLSMART' }).click();
  await expect(page.getByTestId('project-tags').locator('.lotus-tag', { hasText: 'XLSMART' })).toHaveCount(0);
  await page.goto(`${origin}/dashboard`);
  await expect(tagBox(page, 'PROJECT')).toHaveCount(1);
  await expect(tagBox(page, 'XLSMART')).toHaveCount(0);
});
