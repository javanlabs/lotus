import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

async function seedGitlabBindingWithHistory(orgId: string) {
  return withDB(async (client) => {
    const project = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'Payments') RETURNING id`,
      [orgId],
    );
    const component = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'billing-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    const conn = await client.query<{ id: string }>(
      `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'gitlab.com', 'https://gitlab.com') RETURNING id`,
      [orgId],
    );
    const binding = await client.query<{ id: string }>(
      `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1, $2, $3, 'gitlab', 'alurkerja/billing-api', '{}') RETURNING id`,
      [orgId, component.rows[0].id, conn.rows[0].id],
    );
    await client.query(
      `INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
       VALUES ($1, 'gitlab', 'gitlab.pipeline_success_rate', 'Pipeline success rate', '%', 'number', 'higher_better', true, true, now() - interval '10 days', now())`,
      [orgId],
    );
    for (let d = 6; d >= 0; d--) {
      await client.query(
        `INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
         VALUES ($1, $2, $3, 'gitlab.pipeline_success_rate', $4, '%', now() - ($5 || ' days')::interval)`,
        [orgId, binding.rows[0].id, component.rows[0].id, 80 + d, d],
      );
    }
    return { projectId: project.rows[0].id, componentId: component.rows[0].id, bindingId: binding.rows[0].id };
  });
}

test('metric detail page renders a chart and the range selector swaps in place, not a full reload', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('charts-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedGitlabBindingWithHistory(orgId);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const detailPath = `/projects/p/components/c/metrics/${ids.bindingId}`;
  await page.goto(`${origin}${detailPath}`);

  await expect(page.getByText('Pipeline success rate')).toBeVisible();
  await expect(page.locator('svg path[vector-effect="non-scaling-stroke"]').first()).toBeVisible();
  await expect(page.getByRole('button', { name: '7d' })).toBeVisible();

  await page.getByRole('button', { name: '30d' }).click();
  // A real htmx swap keeps the browser on the same URL — no navigation
  // event, unlike task 4.6's explicit "ganti range nggak reload halaman"
  // acceptance criterion would fail if this were a plain page.goto.
  await expect(page).toHaveURL(`${origin}${detailPath}`);
  await expect(page.getByRole('button', { name: '30d' })).toBeVisible();
});

test('a metric key with no hardcoded template support still appears in the chart grid', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('charts-newkey-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedGitlabBindingWithHistory(orgId);

  await withDB(async (client) => {
    await client.query(
      `INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
       VALUES ($1, 'gitlab', 'gitlab.totally_new_metric', 'Totally New Metric', 'count', 'number', 'neutral', false, true, now(), now())`,
      [orgId],
    );
    await client.query(
      `INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
       VALUES ($1, $2, $3, 'gitlab.totally_new_metric', 7, 'count', now())`,
      [orgId, ids.bindingId, ids.componentId],
    );
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects/p/components/c/metrics/${ids.bindingId}`);
  await expect(page.getByText('Totally New Metric')).toBeVisible();
});

test('inactive metric still renders with a "not collected since" note instead of disappearing', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('charts-inactive-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedGitlabBindingWithHistory(orgId);

  await withDB(async (client) => {
    await client.query(
      `INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
       VALUES ($1, 'gitlab', 'gitlab.branches_stale', 'Branches (stale)', 'count', 'number', 'lower_better', true, false, now() - interval '30 days', now() - interval '10 days')`,
      [orgId],
    );
    await client.query(
      `INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
       VALUES ($1, $2, $3, 'gitlab.branches_stale', 3, 'count', now() - interval '10 days')`,
      [orgId, ids.bindingId, ids.componentId],
    );
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects/p/components/c/metrics/${ids.bindingId}`);
  await expect(page.getByText('Branches (stale)')).toBeVisible();
  await expect(page.getByText('not collected since', { exact: false })).toBeVisible();
});

test('component page shows a lazy-loaded sparkline per binding row', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('charts-sparkline-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedGitlabBindingWithHistory(orgId);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects/p/components/c`);
  // The server sends data attributes; sparklines.js draws the bars in
  // the browser. Asserting on the drawn element therefore also proves the
  // library ran on content htmx swapped in after page load, which its own
  // one-shot DOM scan does not do.
  await expect(page.locator('[data-sparkline="true"] svg rect').first()).toBeVisible({ timeout: 5000 });
});
