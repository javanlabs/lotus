import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// A component with no container image whose Trivy binding scans its
// GitLab repository — what the scheduler creates — not due for a day.
// The connection carries no token, so a scan this server's worker runs
// fails at once: enough to watch the whole round trip in a browser
// without a GitLab to clone from.
async function seedRepoScan(orgId: string): Promise<string> {
  return withDB(async (client) => {
    const one = async (q: string, args: unknown[]) => (await client.query<{ id: string }>(q, args)).rows[0].id;
    const project = await one(`INSERT INTO projects (org_id, slug, name) VALUES ($1, 'alurkerja', 'AlurKerja') RETURNING id`, [orgId]);
    const comp = await one(
      `INSERT INTO components (org_id, project_id, slug, name, kind, language) VALUES ($1, $2, 'camunda', 'Camunda', 'backend', 'java') RETURNING id`,
      [orgId, project],
    );
    const conn = await one(
      `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'GitLab', 'https://gitlab.example') RETURNING id`,
      [orgId],
    );
    return one(
      `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config, sync_interval_seconds, next_sync_at)
       VALUES ($1, $2, $3, 'trivy', 'alurkerja/camunda', '{"source":"repo"}', 86400, now() + interval '1 day') RETURNING id`,
      [orgId, comp, conn],
    );
  });
}

test('Scan now runs a repository scan in the background and the page shows how it went', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('repo-scan', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await seedRepoScan(orgId);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.setViewportSize({ width: 1280, height: 900 });

  await page.goto(`${origin}/projects/alurkerja/components/camunda/metrics/trivy`);
  const bar = page.locator('#trivy-scan');
  await expect(bar).toHaveAttribute('data-scan-state', 'idle');
  await expect(bar).toContainText('Not scanned yet');
  await expect(page.getByText('This repository has not been scanned yet.')).toBeVisible();

  await bar.getByRole('button', { name: 'Scan now' }).click();
  // Queued, then picked up: the bar says so and its button is off.
  await expect(bar).toHaveAttribute('data-scan-state', /waiting|scanning/);
  await expect(bar.getByRole('button')).toBeDisabled();
  await page.screenshot({ path: 'test-results/repo-scan-waiting.png' });

  // The worker finishes the run (here, failing: no token); the bar's next
  // poll reloads the page, which shows the outcome and offers a retry.
  await expect(bar).toHaveAttribute('data-scan-state', 'idle', { timeout: 20_000 });
  await expect(bar).toContainText('Last scan failed');
  await expect(bar.getByRole('button', { name: 'Scan now' })).toBeEnabled();
  await page.screenshot({ path: 'test-results/repo-scan-failed.png' });

  await retireOrg(orgId);
});
