import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

test('a metric that has not synced for a day is drawn dashed, with a note saying why', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('last-updated-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await withDB(async (client) => {
    const project = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'Payments') RETURNING id`,
      [orgId],
    );
    const fresh = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'a', 'billing-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    const stale = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'b', 'ledger-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    await client.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, jsonb_build_object('gitlab', jsonb_build_object('status', 'pass', 'updated_at', now() - interval '5 minutes')), 'pass', NULL, now() - interval '5 minutes'),
              ($1, $2, $4, jsonb_build_object(
                 'gitlab', jsonb_build_object('status', 'pass', 'updated_at', now() - interval '5 minutes'),
                 'trivy', jsonb_build_object('status', 'pass', 'updated_at', now() - interval '3 days')), 'pass', NULL, now() - interval '5 minutes')`,
      [orgId, project.rows[0].id, fresh.rows[0].id, stale.rows[0].id],
    );
    return project.rows[0].id;
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/projects/p`);

  await expect(page.getByText('LAST UPDATED')).toBeVisible();
  await expect(page.getByText('PERF')).toHaveCount(0);
  await expect(page.getByText('SECURITY')).toHaveCount(0);
  await expect(page.getByText('Clients who see this project')).toHaveCount(0);

  await expect(page.getByText('stale', { exact: true })).toBeVisible(); // legend

  const freshRow = page.locator('.lotus-repo-row', { hasText: 'billing-api' });
  const staleRow = page.locator('.lotus-repo-row', { hasText: 'ledger-api' });
  await expect(freshRow.getByText('5 min ago')).toBeVisible();
  await expect(freshRow.locator('.lotus-badge-stale')).toHaveCount(0);

  // Only the out-of-date integration's own cell is dashed.
  const staleCells = staleRow.locator('.lotus-badge-stale');
  await expect(staleCells).toHaveCount(1);
  await staleCells.first().hover();
  const tip = page.getByRole('tooltip');
  await expect(tip).toBeVisible();
  await expect(tip).toContainText('Trivy (Harbor) has not synced in more than 24 hours');
  await expect(tip).toContainText('Last synced 3 days ago');
  await page.screenshot({ path: 'test-results/project-last-updated.png', fullPage: true });

  await page.mouse.move(0, 0);
  await expect(tip).toBeHidden();
});
