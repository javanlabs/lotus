import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';
const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

test('a dense series draws a sparkline with no console errors', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('spark-dense-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  let componentId = '';
  await withDB(async (c) => {
    const pr = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, [orgId]);
    const comp = await c.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, [orgId, pr.rows[0].id]);
    componentId = comp.rows[0].id;
    const conn = await c.query(`INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','gitlab.com','https://gitlab.com') RETURNING id`, [orgId]);
    const b = await c.query(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'gitlab','g/b','{}') RETURNING id`, [orgId, componentId, conn.rows[0].id]);
    await c.query(`INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
                   VALUES ($1,'gitlab','gitlab.pipeline_success_rate','Pipeline success rate','%','number','higher_better',true,true, now() - interval '8 days', now())`, [orgId]);
    // ~290 readings across 7 days — the raw tier, i.e. what production has.
    for (let i = 0; i < 290; i++) {
      await c.query(`INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
                     VALUES ($1,$2,$3,'gitlab.pipeline_success_rate',$4,'%', now() - ($5 || ' minutes')::interval)`,
        [orgId, b.rows[0].id, componentId, 60 + Math.round(30 * Math.sin(i / 9)), String(i * 34)]);
    }
  });
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  const errors: string[] = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto(`${origin}/components/${componentId}`);
  const bars = page.locator('[data-sparkline="true"] svg rect');
  await expect(bars.first()).toBeVisible({ timeout: 5000 });

  const count = await bars.count();
  expect(count).toBeGreaterThan(1);
  expect(count).toBeLessThanOrEqual(24);
  // Every bar must have a real, positive width.
  for (const w of await bars.evaluateAll((ns) => ns.map((n) => parseFloat(n.getAttribute('width') || '0')))) {
    expect(w).toBeGreaterThan(0);
  }
  expect(errors, `console errors:\n${errors.slice(0, 5).join('\n')}`).toHaveLength(0);
});
