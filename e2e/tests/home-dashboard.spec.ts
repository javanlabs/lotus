import { expect, test, type Page } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// One project: billing-api failing its SonarQube gate, its Sentry not
// synced for three days, and a blocker reported and opened.
async function seed(orgId: string, ownerId: string) {
  await withDB(async (c) => {
    const one = async (q: string, a: unknown[]) => (await c.query<{ id: string }>(q, a)).rows[0].id;
    const p = await one(`INSERT INTO projects (org_id, slug, name) VALUES ($1, 'payments', 'Payments') RETURNING id`, [orgId]);
    const comp = await one(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'billing', 'billing-api', 'backend') RETURNING id`, [orgId, p]);
    for (const [kind, provider] of [['sonarqube', 'sonarqube'], ['sentry', 'sentry']]) {
      const conn = await one(`INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, $2, $2, 'https://x') RETURNING id`, [orgId, provider]);
      await c.query(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1, $2, $3, $4, 'k', '{}')`, [orgId, comp, conn, kind]);
    }
    await c.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, jsonb_build_object(
         'sonarqube', jsonb_build_object('status', 'fail', 'score', 40, 'updated_at', now()),
         'sentry', jsonb_build_object('status', 'pass', 'score', 90, 'updated_at', now() - interval '3 days')), 'fail', 60, now())`,
      [orgId, p, comp],
    );
    const issue = await one(
      `INSERT INTO qa_issues (org_id, project_id, key, title, severity, status, source, reported_by)
       VALUES ($1, $2, 1, 'Checkout crashes', 'blocker', 'new', 'internal', $3) RETURNING id`,
      [orgId, p, ownerId],
    );
    await c.query(
      `INSERT INTO qa_issue_events (org_id, issue_id, kind, body, status_from, status_to, actor_id) VALUES ($1, $2, 'status', '', 'new', 'open', $3)`,
      [orgId, issue, ownerId],
    );
  });
}

async function expectNoPageScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
}

test('the dashboard is where the app opens, and the health map leads to each failure', async ({ page, context }) => {
  const { sessionToken, orgId, userId, subdomain } = await seedOwnerSession('home-dash-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await seed(orgId, userId);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.setViewportSize({ width: 1280, height: 900 });

  await page.goto(`${origin}/`);
  await expect(page).toHaveURL(`${origin}/dashboard`);
  await expect(page.getByRole('link', { name: 'Dashboard', exact: true })).toHaveClass(/is-active/);

  const summary = page.getByTestId('summary');
  await expect(summary.getByText('NEED ATTENTION')).toBeVisible();
  await expect(summary.locator('a', { hasText: 'OPEN ISSUES' })).toContainText('1');

  const insights = page.getByTestId('insights');
  await expect(insights.getByTestId('alerts').getByRole('link', { name: 'Blocker issue #1: Checkout crashes' })).toBeVisible();
  // The old "Needs attention" card that repeated the map is gone; the
  // words now only name the map's red band in its legend.
  await expect(page.getByText('Needs attention', { exact: true })).toHaveCount(0);
  await expect(page.getByTestId('health-map')).toContainText('Needs attention (0–79)');
  await expect(page.getByTestId('recent-activity')).toContainText('marked open');
  await page.screenshot({ path: 'test-results/home-dashboard.png', fullPage: true });

  // On a wide screen health and the map share one column at one width, with
  // trends and where to start beside them, without the map's table being
  // pushed into its own scrollbar; on a narrow one everything stacks. Each
  // column sizes itself, so health is not stretched to match trends.
  const map = page.getByTestId('health-map');
  const health = (await page.getByTestId('health').locator('xpath=ancestor::*[contains(@class,"lotus-dash-column")][1]').boundingBox())!;
  const m = (await map.boundingBox())!;
  const at = (await insights.boundingBox())!;
  const trend = (await page.getByTestId('trends').boundingBox())!;
  expect(m.x).toBe(health.x);
  expect(m.width).toBeLessThanOrEqual(health.width);
  expect(m.width).toBeGreaterThanOrEqual(health.width - 1);
  expect(trend.x).toBeGreaterThanOrEqual(m.x + m.width);
  expect(at.x).toBe(trend.x);
  expect(at.y).toBeGreaterThanOrEqual(trend.y + trend.height);
  const clipped = await map.locator('.lotus-dash-map-scroll').evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(clipped).toBeLessThanOrEqual(0);
  await expect(map.locator('.lotus-dash-cell.is-stale')).toHaveCount(1);
  await expectNoPageScroll(page);

  await page.setViewportSize({ width: 1000, height: 900 });
  const [m2, at2] = [(await map.boundingBox())!, (await insights.boundingBox())!];
  expect(at2.y).toBeGreaterThanOrEqual(m2.y + m2.height);
  expect(at2.x).toBe(m2.x);
  await expectNoPageScroll(page);
  await page.setViewportSize({ width: 1280, height: 900 });

  // The failing component is on the map, and its cell opens that integration.
  await map.getByRole('link', { name: '40', exact: true }).click();
  await expect(page).toHaveURL(`${origin}/projects/payments/components/billing/metrics/sonarqube`);

  // The breadcrumb's logo leads back.
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Lotus' }).click();
  await expect(page).toHaveURL(`${origin}/dashboard`);

  // On a phone the page never scrolls sideways.
  await page.setViewportSize({ width: 390, height: 900 });
  await page.reload();
  await expectNoPageScroll(page);
});
