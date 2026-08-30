import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

test('activity feed pages through its runs', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('activity-pager-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  // 30 runs => two pages at 25 per page. sync_runs requires a real
  // connection and binding, so the whole chain is seeded first.
  await withDB(async (c) => {
    const project = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, [orgId]);
    const component = await c.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, [orgId, project.rows[0].id]);
    const conn = await c.query(`INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','gitlab.com','https://gitlab.com') RETURNING id`, [orgId]);
    const binding = await c.query(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'gitlab','g/b','{}') RETURNING id`, [orgId, component.rows[0].id, conn.rows[0].id]);
    for (let i = 0; i < 30; i++) {
      await c.query(
        `INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status, started_at, finished_at, created_at)
         VALUES ($1,$2,$3,'gitlab','manual','ok', now(), now(), now() - ($4 || ' minutes')::interval)`,
        [orgId, conn.rows[0].id, binding.rows[0].id, String(i)],
      );
    }
  });

  await page.goto(`${origin}/activity`);
  await expect(page.locator('.lotus-pager')).toContainText('1–25 of 30 runs');
  await expect(page.locator('.lotus-pager')).toContainText('1 / 2');
  await expect(page.locator('.lotus-table-head')).toBeVisible();
  expect(await page.locator('.lotus-table-head').evaluate(e =>
    getComputedStyle(e).backgroundImage.includes('repeating-linear-gradient'))).toBe(true);

  await page.getByRole('link', { name: /Next/ }).click();
  await expect(page).toHaveURL(`${origin}/activity?page=2`);
  await expect(page.locator('.lotus-pager')).toContainText('26–30 of 30 runs');
  await expect(page.getByRole('link', { name: /Next/ })).toHaveCount(0);

  await page.getByRole('link', { name: /Previous/ }).click();
  await expect(page.locator('.lotus-pager')).toContainText('1–25 of 30 runs');

  // A stale bookmark past the end clamps to the last page rather than erroring.
  await page.goto(`${origin}/activity?page=99`);
  await expect(page.locator('.lotus-pager')).toContainText('26–30 of 30 runs');
});
