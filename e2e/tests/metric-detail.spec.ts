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
// — task 4.4's detail pages are keyed off a binding_id, not a component,
// so every spec below needs at least that much.
async function seedComponentWithBinding(
  orgId: string,
  kind: string,
  provider: string,
  externalKey: string,
): Promise<{ projectId: string; componentId: string; bindingId: string }> {
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
    return { projectId: project.rows[0].id, componentId: component.rows[0].id, bindingId: binding.rows[0].id };
  });
}

function detailPath(ids: { projectId: string; componentId: string; bindingId: string }): string {
  return `/projects/p/components/c/metrics/${ids.bindingId}`;
}

test('GitLab detail page renders from the component page link', async ({ page, context }) => {
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
  await page.goto(`${origin}/projects/p/components/c`);
  await page.getByText('alurkerja/billing-api').click();

  await expect(page).toHaveURL(`${origin}${detailPath(ids)}`);
  await expect(page.getByText('Go', { exact: true })).toBeVisible();
  await expect(page.getByText('main', { exact: true })).toBeVisible();
  await expect(page.getByText('private', { exact: false })).toBeVisible();
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
