import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

test('dashboard on a tenant host with no session does not show dashboard content', async ({ page }) => {
  const res = await page.goto(`${tenantOrigin('no-session-e2e')}/dashboard`);
  expect(res?.status()).toBeGreaterThanOrEqual(400);
  await expect(page.getByRole('heading', { name: 'Software health' })).not.toBeVisible();
});

test('dashboard shows the app shell and an empty state for a fresh org', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('empty-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  // The organization's root lands on the dashboard, as sign-in does.
  await page.goto(`${origin}/`);
  await expect(page).toHaveURL(`${origin}/dashboard`);
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
  await expect(page.getByTestId('alerts')).toHaveCount(0);

  // The app shell (task 4.1): sidebar with the signed-in user, the
  // Dashboard and Projects entries, the Lotus wordmark — proves the
  // design-system layout rendered, not just raw content.
  await expect(page.getByText('E2E Owner')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Dashboard', exact: true })).toBeVisible();
  await page.getByRole('link', { name: /^Projects/ }).click();
  await expect(page.getByRole('heading', { name: 'Software health' })).toBeVisible();
  await expect(page.getByText('No projects yet')).toBeVisible();

  // The user-box dropdown is real Alpine.js interactivity, not a static
  // mockup — clicking it must actually reveal Log out.
  await expect(page.getByRole('button', { name: 'Log out' })).not.toBeVisible();
  await page.getByText('E2E Owner').click();
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible();
});

test('creating a project through the real form lands on the new project\'s own page', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('newproj-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects/new`);

  await page.getByLabel('PROJECT NAME').fill('E2E Payments');
  await page.getByLabel('SLUG').fill('e2e-payments');
  await page.getByRole('button', { name: 'Create project' }).click();

  // Redirects to the new project's own page (task 4.3), by its slug, not the org-wide
  // dashboard — there's now somewhere more specific to land.
  await expect(page).toHaveURL(`${origin}/projects/e2e-payments`);
  await expect(page.getByRole('heading', { name: 'E2E Payments' })).toBeVisible();
  await expect(page.getByText('No components yet')).toBeVisible();

  // Final review, item 4: a componentless project must still get a row in
  // the tree (deviation 4), not the "No repositories yet" empty state —
  // that state only makes sense with no way to see the project at all.
  await page.goto(`${origin}/projects`);
  await expect(page.getByText('E2E Payments', { exact: true })).toBeVisible();
  await expect(page.getByText('No repositories yet')).not.toBeVisible();
  await expect(page.getByText('No projects yet')).not.toBeVisible();

  await withDB(async (client) => {
    const { rows } = await client.query('SELECT count(*) FROM projects WHERE org_id = $1 AND slug = $2', [orgId, 'e2e-payments']);
    expect(Number(rows[0].count)).toBe(1);
  });
});

test('the projects list renders one row per project, with its rolled-up metrics', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('full-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);

  await withDB(async (client) => {
    const project = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'Payments') RETURNING id`,
      [orgId],
    );
    const component = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'billing-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    await client.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, $4::jsonb, 'fail', 30, now())`,
      [
        orgId,
        project.rows[0].id,
        component.rows[0].id,
        JSON.stringify({ gitlab: { status: 'fail', count_value: 5, count_label: 'stale branch' } }),
      ],
    );

    // A passing project too: with the "warn" tier gone the filter is
    // All / Failing, so hiding can only be asserted against a row that
    // is not failing.
    const healthy = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'h', 'Identity') RETURNING id`,
      [orgId],
    );
    const healthyComponent = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'hc', 'identity-api', 'backend') RETURNING id`,
      [orgId, healthy.rows[0].id],
    );
    await client.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, $4::jsonb, 'pass', 95, now())`,
      [
        orgId,
        healthy.rows[0].id,
        healthyComponent.rows[0].id,
        JSON.stringify({ gitlab: { status: 'pass', count_value: 0, count_label: 'stale branch' } }),
      ],
    );
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects`);

  // A failing project is rendered twice — in the level's "All" list and in
  // the "Failing" list — and only the active filter's copy is visible, so
  // every lookup below is scoped to visible elements.

  // Rows are projects, not components: the project is the outermost unit
  // and the thing that owns a Lotus score. Its one component is behind
  // the row, on the project's own page.
  await expect(page.getByText('Payments', { exact: true }).filter({ visible: true })).toBeVisible();
  await expect(page.getByText('billing-api')).not.toBeVisible();
  // Scoped to the row: there are two single-component projects now.
  await expect(page.locator('.lotus-repo-row:visible', { hasText: 'Payments' })
    .getByText('1 component', { exact: true })).toBeVisible();

  await page.getByText('Payments', { exact: true }).filter({ visible: true }).click();
  await expect(page.getByText('billing-api')).toBeVisible();
  // A kind with a status but no score keeps the collector's own phrasing
  // in the cell — the score-only column would otherwise render an empty
  // box for a component that was measured and did badly.
  await expect(page.getByText('5 stale branches')).toBeVisible();
  await page.goBack();

  // Client-side filtering (Alpine x-show) — a real regression this
  // feature hit in manual QA: combining an inline `display:grid` with
  // x-show made Alpine collapse the row to block layout on toggle. This
  // asserts the row is both present and still grid-laid-out after a
  // filter round-trip, not just present in the DOM.
  // .first(): "Failing" also appears in the Lotus Score card's "N failing"
  // summary further down the page — the segmented-control button is the
  // first match in DOM order.
  await page.getByText('Failing', { exact: false }).first().click();
  await expect(page.getByText('Payments', { exact: true }).filter({ visible: true })).toBeVisible();
  await expect(page.getByText('Identity', { exact: true }).filter({ visible: true })).not.toBeVisible();
  await page.getByText('All', { exact: false }).first().click();
  await expect(page.getByText('Payments', { exact: true }).filter({ visible: true })).toBeVisible();
  await expect(page.getByText('Identity', { exact: true }).filter({ visible: true })).toBeVisible();
  await expect(page.locator('.lotus-repo-row:visible', { hasText: 'Payments' })).toHaveCSS('display', 'grid');
});
