import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';
import { chooseFromDropdown } from './helpers/dropdown';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// seedComponentWithBinding mirrors bind-wizard.spec.ts's own
// seedProjectAndComponent, plus one metric_bindings row of the given kind
// — task 4.4's detail pages are a binding's, named by its kind (a
// component has at most one live binding per kind), so every spec below
// needs at least that much.
async function seedComponentWithBinding(
  orgId: string,
  kind: string,
  provider: string,
  externalKey: string,
): Promise<{ projectId: string; componentId: string; bindingId: string; kind: string }> {
  return withDB(async (client) => {
    const project = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'Payments') RETURNING id`,
      [orgId],
    );
    const component = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'billing-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    const connection = await client.query<{ id: string }>(
      `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, $2, $2, 'https://example.com') RETURNING id`,
      [orgId, provider],
    );
    const binding = await client.query<{ id: string }>(
      `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1, $2, $3, $4, $5, '{}') RETURNING id`,
      [orgId, component.rows[0].id, connection.rows[0].id, kind, externalKey],
    );
    return { projectId: project.rows[0].id, componentId: component.rows[0].id, bindingId: binding.rows[0].id, kind };
  });
}

function detailPath(ids: { kind: string }): string {
  return `/projects/p/components/c/metrics/${ids.kind}`;
}

test('GitLab detail page is what its component opens on', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('metric-gl-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedComponentWithBinding(orgId, 'gitlab', 'gitlab', 'alurkerja/billing-api');
  await withDB((client) =>
    client.query(
      `INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, primary_language, default_branch, branches_total, last_pipeline_status, visibility)
       VALUES ($1, $2, $3, 'Go', 'main', 12, 'success', 'private')`,
      [orgId, ids.bindingId, ids.componentId],
    ),
  );

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  // The component opens straight on its only bound metric.
  await page.goto(`${origin}/projects/p/components/c`);

  await expect(page).toHaveURL(`${origin}${detailPath(ids)}`);
  await expect(page.getByText('alurkerja/billing-api')).toBeVisible();
  await expect(page.getByText('Go', { exact: true })).toBeVisible();
  await expect(page.getByText('main', { exact: true })).toBeVisible();
  await expect(page.getByText('private', { exact: false })).toBeVisible();
});

test('a component opens on SonarQube, its bound metrics are tabs one click apart, unbound ones are not links', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('metric-tabs-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedComponentWithBinding(orgId, 'gitlab', 'gitlab', 'alurkerja/billing-api');
  const sonar = await withDB(async (client) => {
    const conn = await client.query<{ id: string }>(
      `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'sonarqube', 'sonar', 'https://sonar.example') RETURNING id`,
      [orgId],
    );
    const b = await client.query<{ id: string }>(
      `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1, $2, $3, 'sonarqube', 'billing-api', '{}') RETURNING id`,
      [orgId, ids.componentId, conn.rows[0].id],
    );
    await client.query(
      `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, coverage, bugs, vulnerabilities, code_smells, security_hotspots,
         duplicated_lines_density, ncloc, complexity, test_execution_time_ms, reliability_rating, security_rating, sqale_rating, languages, last_scan_at)
       VALUES ($1, $2, $3, 'OK', 94.2, 0, 0, 42, 3, 2.4, 18250, 1900, 84000, 'A', 'B', 'A', 'go, ts', now() - interval '2 hours')`,
      [orgId, b.rows[0].id, ids.componentId],
    );
    for (const [key, label, unit, base] of [['sonarqube.coverage', 'Coverage', '%', 90], ['sonarqube.code_smells', 'Code smells', 'count', 40]] as const) {
      await client.query(
        `INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
         VALUES ($1, 'sonarqube', $2, $3, $4, 'number', 'neutral', true, true, now() - interval '10 days', now())`,
        [orgId, key, label, unit],
      );
      for (let d = 6; d >= 0; d--) {
        await client.query(
          `INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
           VALUES ($1, $2, $3, $4, $5, $6, now() - ($7 || ' days')::interval)`,
          [orgId, b.rows[0].id, ids.componentId, key, base + d, unit, d],
        );
      }
    }
    await client.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, jsonb_build_object(
         'sonarqube', jsonb_build_object('status', 'pass', 'score', 92, 'updated_at', now()),
         'gitlab', jsonb_build_object('status', 'fail', 'score', 40, 'updated_at', now())), 'fail', 66, now())`,
      [orgId, ids.projectId, ids.componentId],
    );
    return b.rows[0].id;
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`${origin}/projects/p/components/c`);

  // It opens on SonarQube, the first kind in the fixed order.
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/sonarqube`);
  const tabs = page.getByRole('navigation', { name: 'Metrics' });
  const open = tabs.locator('[aria-current="page"]');
  const labels = ['SONARQUBE', 'TRIVY (HARBOR)', 'SENTRY', 'GITLAB'];
  for (const [i, label] of labels.entries()) {
    await expect(tabs.locator('.lotus-mt-tab').nth(i)).toContainText(label);
  }
  await expect(open).toContainText('SONARQUBE');
  await expect(open).toContainText('92');
  // Only the two bound kinds are links.
  await expect(tabs.getByRole('link')).toHaveCount(2);
  await expect(tabs.locator('.lotus-mt-tab', { hasText: 'SENTRY' }).locator('a')).toHaveCount(0);
  await expect(page.getByText('Quality gate passed')).toBeVisible();
  await page.screenshot({ path: 'test-results/component-tabs-sonarqube.png', fullPage: true });

  await tabs.getByRole('link', { name: /GITLAB/ }).click();
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/gitlab`);
  await expect(open).toContainText('GITLAB');
  // The binding's own actions sit on its line.
  for (const name of ['Test', 'Disable', 'Delete']) {
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible();
  }
  await page.screenshot({ path: 'test-results/component-tabs-gitlab.png' });

  // Two views of the binding: Overview, where it opens, and Syncs.
  const views = page.getByRole('navigation', { name: 'GitLab views' });
  await expect(views.locator('[aria-current="page"]')).toHaveText('Overview');
  await views.getByRole('link', { name: 'Syncs' }).click();
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/gitlab/runs`);
  await expect(views.locator('[aria-current="page"]')).toHaveText('Syncs');
  await expect(open).toContainText('GITLAB');
  await page.screenshot({ path: 'test-results/component-tabs-gitlab-syncs.png' });
  await views.getByRole('link', { name: 'Overview' }).click();
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/gitlab`);

  // Disabling keeps the reader on this tab.
  await page.getByRole('button', { name: 'Disable', exact: true }).click();
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/gitlab`);
  await expect(page.getByRole('button', { name: 'Enable', exact: true })).toBeVisible();

  // A link made before the kind URLs — by binding id — lands on the kind.
  await page.goto(`${origin}/projects/p/components/c/metrics/${sonar}/runs`);
  await expect(page).toHaveURL(`${origin}/projects/p/components/c/metrics/sonarqube/runs`);
  await expect(open).toContainText('SONARQUBE');

  // On a phone the strip scrolls inside its card; the page never does.
  await page.setViewportSize({ width: 390, height: 900 });
  for (const kind of ['sonarqube', 'gitlab']) {
    await page.goto(`${origin}/projects/p/components/c/metrics/${kind}`);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  }
});

test('Sonar date picker switches between historical snapshots', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('metric-sonar-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedComponentWithBinding(orgId, 'sonarqube', 'sonarqube', 'alurkerja/billing-api');
  // The picker lists calendar days in the database's timezone, so the
  // date to pick comes from there too, not from UTC.
  let tenDaysAgo = '';
  await withDB(async (client) => {
    const r = await client.query(`SELECT to_char(now() - interval '10 days', 'YYYY-MM-DD') AS d`);
    tenDaysAgo = r.rows[0].d;
    await client.query(
      `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, captured_at, quality_gate, bugs, coverage)
       VALUES ($1, $2, $3, now() - interval '10 days', 'OK', 1, 77.7)`,
      [orgId, ids.bindingId, ids.componentId],
    );
    await client.query(
      `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, bugs, coverage)
       VALUES ($1, $2, $3, 'ERROR', 9, 12.3)`,
      [orgId, ids.bindingId, ids.componentId],
    );
  });

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}${detailPath(ids)}`);

  await expect(page.getByText('12.3%')).toBeVisible();
  await chooseFromDropdown(page, 'date', tenDaysAgo);
  await expect(page.getByText('77.7%')).toBeVisible();
  await expect(page.getByText('12.3%')).not.toBeVisible();
});

test('Trivy false-positive annotation saves and deletes, real browser round-trip', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('metric-trivy-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedComponentWithBinding(orgId, 'trivy', 'harbor_trivy', 'library/billing-api');
  const rawJSON = JSON.stringify({
    severity: 'Critical',
    vulnerabilities: [
      { id: 'CVE-2024-7777', package: 'openssl', version: '1.1', fix_version: '1.2', severity: 'Critical', description: 'a real CVE', links: [], cwe_ids: [] },
    ],
  });
  await withDB((client) =>
    client.query(
      `INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, total, critical, raw_json)
       VALUES ($1, $2, $3, 'registry', 1, 1, $4::jsonb)`,
      [orgId, ids.bindingId, ids.componentId, rawJSON],
    ),
  );

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}${detailPath(ids)}`);

  await expect(page.getByText('CVE-2024-7777')).toBeVisible();
  await page.getByTitle('Annotate').click();
  await page.locator('textarea[name="comment"]').fill('not reachable from our entrypoint');
  await page.getByRole('button', { name: 'Save' }).click();

  await expect(page).toHaveURL(`${origin}${detailPath(ids)}`);
  await expect(page.getByText('FP', { exact: true })).toBeVisible();

  await withDB(async (client) => {
    const { rows } = await client.query(
      `SELECT count(*) FROM trivy_finding_notes WHERE binding_id = $1 AND cve_id = 'CVE-2024-7777'`,
      [ids.bindingId],
    );
    expect(Number(rows[0].count)).toBe(1);
  });

  await page.getByTitle('Annotate').click();
  await page.getByRole('button', { name: 'Remove' }).click();
  await expect(page).toHaveURL(`${origin}${detailPath(ids)}`);
  await expect(page.getByText('FP', { exact: true })).not.toBeVisible();
});

test('Sentry detail page shows the score explainer panel and filters issues by status', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('metric-sentry-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const ids = await seedComponentWithBinding(orgId, 'sentry', 'sentry', 'billing-api');
  const snapshotId = await withDB(async (client) => {
    const snap = await client.query<{ id: string }>(
      `INSERT INTO sentry_snapshots (org_id, binding_id, component_id, environment, project_slug, total_issues, score, grade)
       VALUES ($1, $2, $3, 'production', 'billing-api', 2, 82, 'B') RETURNING id`,
      [orgId, ids.bindingId, ids.componentId],
    );
    await client.query(
      `INSERT INTO sentry_issues (org_id, snapshot_id, snapshot_captured_at, issue_id, short_id, title, status, substatus, event_count, permalink)
       SELECT $1, id, captured_at, 'ISSUE-1', 'BILL-1', 'NullPointerException in checkout', 'unresolved', 'new', 12, 'https://sentry.io/issues/1'
       FROM sentry_snapshots WHERE id = $2`,
      [orgId, snap.rows[0].id],
    );
    return snap.rows[0].id;
  });
  void snapshotId;

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}${detailPath(ids)}`);

  await expect(page.getByText('HOW THIS SCORE IS CALCULATED')).toBeVisible();
  await expect(page.getByText('NullPointerException in checkout')).toBeVisible();
  await page.getByRole('button', { name: /new \(1\)/ }).click();
  await expect(page.getByText('NullPointerException in checkout')).toBeVisible();
});
