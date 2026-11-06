import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

// A failing and a healthy component, with history, in a real browser: the
// dashboard shows health, where to start, the map and a trend.
test('dashboard visualizes health and says where to start', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('dash-viz-e2e', 'placeholder');
  const origin = `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await withDB(async (client) => {
    await client.query(`SELECT set_config('app.org_id', $1, false)`, [orgId]);
    const project = await client.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1,'pay','Payments') RETURNING id`, [orgId]);
    const conn = await client.query(`INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'sonarqube','s','https://s.test') RETURNING id`, [orgId]);
    for (const [slug, status, name] of [['bad', 'fail', 'ledger-api'], ['good', 'pass', 'billing-api']]) {
      const c = await client.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,$3,$4,'backend') RETURNING id`, [orgId, project.rows[0].id, slug, name]);
      // The map reads scores: a failing component scores below 80.
      const score = status === 'pass' ? 92 : 41;
      await client.query(
        `INSERT INTO component_metrics_current (org_id, project_id, component_id, status, score, metrics) VALUES ($1,$2,$3,$4,$5,$6)`,
        [orgId, project.rows[0].id, c.rows[0].id, status, score, JSON.stringify({ sonarqube: { status, score, grade: status === 'pass' ? 'A' : 'D', updated_at: new Date().toISOString() } })],
      );
      const b = await client.query(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'sonarqube',$4,'{}') RETURNING id`, [orgId, c.rows[0].id, conn.rows[0].id, slug]);
      await client.query(
        `INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
         SELECT $1,$2,$3,'sonar.coverage', 60 + n, '%', now() - (10 - n) * interval '1 day' FROM generate_series(0, 10) n`,
        [orgId, b.rows[0].id, c.rows[0].id],
      );
    }
  });

  await page.goto(`${origin}/dashboard`);

  await expect(page.getByTestId('health')).toContainText('50%');
  await expect(page.getByTestId('kind-bars')).toContainText('SONARQUBE');
  await expect(page.getByTestId('insights')).toContainText('SonarQube needs attention most');

  // Worst first, and each cell — the kind's score, not a letter — opens
  // that integration on that component.
  const rows = page.getByTestId('health-map').locator('tbody tr');
  await expect(rows.first()).toContainText('ledger-api');
  await rows.first().getByRole('link', { name: '41', exact: true }).click();
  await expect(page).toHaveURL(new RegExp('/components/bad/metrics/sonarqube'));

  await page.goto(`${origin}/dashboard`);
  await expect(page.getByTestId('trends').locator('.lotus-chart svg').first()).toBeVisible();

  // The page never pushes the body sideways on a phone; the map scrolls.
  await page.setViewportSize({ width: 390, height: 800 });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);

  await retireOrg(orgId);
});
