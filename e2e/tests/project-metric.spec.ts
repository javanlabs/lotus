import { expect, test, type Page } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// A project with three components: billing-api and ledger-api bound to all
// four integrations (billing passing, ledger failing), docs-site bound to
// none — so every KPI card reads 50% and every kind page has an unbound
// footer.
async function seedProject(orgId: string): Promise<Record<string, string>> {
  return withDB(async (client) => {
    const one = async (q: string, args: unknown[]) => (await client.query<{ id: string }>(q, args)).rows[0].id;
    const project = await one(`INSERT INTO projects (org_id, slug, name) VALUES ($1, 'payments', 'Payments') RETURNING id`, [orgId]);
    const comp = (slug: string, name: string) =>
      one(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $4, 'backend') RETURNING id`, [orgId, project, slug, name]);
    const billing = await comp('billing', 'billing-api');
    const ledger = await comp('ledger', 'ledger-api');
    await comp('docs', 'docs-site');

    const bindings: Record<string, string> = {};
    for (const [kind, provider] of [['sonarqube', 'sonarqube'], ['trivy', 'harbor_trivy'], ['sentry', 'sentry'], ['gitlab', 'gitlab']]) {
      const conn = await one(
        `INSERT INTO integration_connections (org_id, provider, name, base_url, config) VALUES ($1, $2, $2, 'https://' || $2 || '.example', '{"org":"acme"}') RETURNING id`,
        [orgId, provider],
      );
      for (const [comp, key] of [[billing, 'grp/billing-api'], [ledger, 'grp/ledger-api']]) {
        bindings[`${kind}:${comp}`] = await one(
          `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1, $2, $3, $4, $5, '{}') RETURNING id`,
          [orgId, comp, conn, kind, key],
        );
      }
    }
    for (const [comp, status, ago] of [[billing, 'pass', '5 minutes'], [ledger, 'fail', '3 days']]) {
      await client.query(
        `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, updated_at)
         SELECT $1, $2, $3, jsonb_object_agg(k, jsonb_build_object('status', $4::text, 'grade', 'B', 'score', 84, 'updated_at', now() - $5::interval)), $4, now()
         FROM unnest(ARRAY['sonarqube','trivy','sentry','gitlab']) k`,
        [orgId, project, comp, status, ago],
      );
    }
    for (const [comp, gate, cov, bugs] of [[billing, 'OK', 94.2, 0], [ledger, 'ERROR', 61.5, 4]] as const) {
      await client.query(
        `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, coverage, bugs, vulnerabilities, code_smells, security_hotspots,
           duplicated_lines_density, ncloc, test_execution_time_ms, reliability_rating, security_rating, sqale_rating, last_scan_at)
         VALUES ($1, $2, $3, $4, $5, $6, 0, 42, 3, 2.4, 18250, 84000, 'A', 'B', 'A', now() - interval '2 hours')`,
        [orgId, bindings[`sonarqube:${comp}`], comp, gate, cov, bugs],
      );
      await client.query(
        `INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, primary_language, default_branch, branches_total, branches_active, branches_stale,
           last_pipeline_status, last_pipeline_web_url, last_pipeline_duration_sec, last_pipeline_finished_at, pipeline_success_rate, pipeline_sample_size,
           last_activity_at, last_committer_name, commits_30d, commits_30d_capped, default_branch_protected, visibility, archived)
         VALUES ($1, $2, $3, 'Go', 'main', 14, 4, 3, $4, 'https://gitlab.example/p/1', 133, now() - interval '1 hour', .9, 20,
           now() - interval '3 hours', 'Rizky Hartono', 37, false, $5, 'private', false)`,
        [orgId, bindings[`gitlab:${comp}`], comp, gate === 'OK' ? 'success' : 'failed', gate === 'OK'],
      );
      await client.query(
        `INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, tags, severity, total, critical, high, medium, low, fixable, pulls, scan_finished_at)
         VALUES ($1, $2, $3, 'registry', '{v1.8.2,latest}', $4, $5, $6, $6, 3, 5, 2, 1204, now() - interval '6 hours')`,
        [orgId, bindings[`trivy:${comp}`], comp, gate === 'OK' ? 'Medium' : 'Critical', gate === 'OK' ? 8 : 14, gate === 'OK' ? 0 : 3],
      );
      await client.query(
        `INSERT INTO sentry_snapshots (org_id, binding_id, component_id, environment, project_slug, sentry_project_id, sentry_org, status_filter, total_issues, score, grade)
         VALUES ($1, $2, $3, 'production', 'web', 42, 'acme', 'is:unresolved', $4, 84, 'B')`,
        [orgId, bindings[`sentry:${comp}`], comp, gate === 'OK' ? 2 : 11],
      );
    }
    return { billing, ledger };
  });
}

async function expectNoPageScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
}

test('a metric tab opens its integration across the project, and a row opens its binding', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('project-metric-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await seedProject(orgId);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`${origin}/projects/payments`);

  // The info button still explains the number rather than navigating.
  const tab = page.locator('.lotus-mt-tab', { hasText: 'SONAR GATE' });
  await tab.getByRole('button', { name: 'How this number is calculated' }).click();
  await expect(page.getByText('Share of components whose quality gate passes').first()).toBeVisible();
  await expect(page).toHaveURL(`${origin}/projects/payments`);
  await page.keyboard.press('Escape');

  await tab.hover();
  await page.screenshot({ path: 'test-results/project-kpi-hover.png' });
  // Anywhere else on the tab opens the project's SonarQube page.
  await tab.click();
  await expect(page).toHaveURL(`${origin}/projects/payments/metrics/sonarqube`);
  await expect(page.getByRole('heading', { name: 'Payments › SonarQube' })).toBeVisible();
  await expect(page.locator('[data-headline]')).toHaveText('50');
  // Its other numbers sit directly under the open tab.
  await expect(page.locator('.lotus-mt-stats')).toContainText('AVG COVERAGE');
  await page.screenshot({ path: 'test-results/project-metric-tabs.png' });
  await expect(page.locator('[data-unbound]')).toContainText('1 component has no SonarQube binding: docs-site');

  // ledger's SonarQube last synced three days ago: its gate is dashed and
  // says why on hover.
  const ledgerRow = page.locator('.lotus-pm-row', { hasText: 'ledger-api' });
  await ledgerRow.locator('.lotus-badge-stale').hover();
  await expect(page.getByRole('tooltip')).toContainText('SonarQube has not synced in more than 24 hours');
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'test-results/project-metric-stale-hover.png', fullPage: true });

  // Sorting by bugs puts the most bugs first.
  await page.locator('.lotus-pm-grid.lotus-table-head').getByText('BUGS').click();
  await expect(page.locator('.lotus-pm-row').first()).toContainText('ledger-api');

  await page.locator('.lotus-pm-row', { hasText: 'billing-api' }).getByRole('link', { name: 'billing-api', exact: true }).click();
  await expect(page).toHaveURL(`${origin}/projects/payments/components/billing/metrics/sonarqube`);
  await expect(page.getByRole('heading', { name: /SonarQube/ })).toBeVisible();

  // The project's SonarQube page stays in the trail, above the component
  // — going down only added the component — and leads back.
  const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' });
  await expect(crumbs).toHaveText(/Payments\s*\/\s*SonarQube\s*\/\s*billing-api\s*$/);
  await expect(crumbs.locator('[aria-current="page"]')).toHaveText('billing-api');
  await crumbs.getByRole('link', { name: 'SonarQube', exact: true }).click();
  await expect(page).toHaveURL(`${origin}/projects/payments/metrics/sonarqube`);
});

test('the metric tabs stay put while switching between the project and its integrations', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('project-metric-tabs', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await seedProject(orgId);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.setViewportSize({ width: 1280, height: 900 });

  const tabs = page.getByRole('navigation', { name: 'Metrics' });
  const open = tabs.locator('[aria-current="page"]');
  const labels = ['LOTUS SCORE', 'SONAR GATE', 'TRIVY CLEAN', 'SENTRY HEALTH', 'GIT HYGIENE'];
  const urls = ['', '/metrics/sonarqube', '/metrics/trivy', '/metrics/sentry', '/metrics/gitlab'];

  // Every page of the project has all five tabs, in the same order, with
  // only the open one changing.
  await page.goto(`${origin}/projects/payments`);
  for (let i = 0; i < labels.length; i++) {
    await expect(tabs.getByRole('link')).toHaveCount(labels.length);
    await expect(tabs.getByRole('link').nth(i)).toContainText(labels[i]);
    await expect(open).toHaveCount(1);
    await expect(open).toContainText(labels[i]);
    if (i + 1 < labels.length) {
      await tabs.getByRole('link').nth(i + 1).click();
      await expect(page).toHaveURL(`${origin}/projects/payments${urls[i + 1]}`);
    }
  }

  // And back to the project from the last one.
  await tabs.getByRole('link').first().click();
  await expect(page).toHaveURL(`${origin}/projects/payments`);
  await expect(open).toContainText('LOTUS SCORE');
});

test('every kind page fits a phone and a laptop without the page scrolling sideways', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('project-metric-layout', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await seedProject(orgId);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  for (const kind of ['sonarqube', 'trivy', 'sentry', 'gitlab']) {
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`${origin}/projects/payments/metrics/${kind}`);
      await expect(page.locator('[data-headline]')).toHaveText('50');
      await page.screenshot({ path: `test-results/project-metric-${kind}-${width}.png`, fullPage: true });
      // The table scrolls inside its card; the page itself never does.
      await expectNoPageScroll(page);
    }
  }
});
