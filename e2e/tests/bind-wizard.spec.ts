import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';
import { dropdownTrigger } from './helpers/dropdown';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// seedProjectAndComponent mirrors what the "Add component" form would
// have produced — inserted directly since these specs are about the bind
// wizard, not project/component creation (covered in dashboard.spec.ts).
async function seedProjectAndComponent(orgId: string): Promise<string> {
  return withDB(async (client) => {
    const project = await client.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'Payments') RETURNING id`,
      [orgId],
    );
    const component = await client.query<{ id: string }>(
      `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'billing-api', 'backend') RETURNING id`,
      [orgId, project.rows[0].id],
    );
    return component.rows[0].id;
  });
}

test('bind wizard renders a different, descriptor-driven form per kind tab', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('bindwiz-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const componentId = await seedProjectAndComponent(orgId);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  // No ?kind, so the wizard opens on the first of bindableKinds — which is
  // SonarQube, not GitLab: the kind order is fixed product-wide (AGENTS.md)
  // and this test follows it rather than pinning a second copy of it.
  await page.goto(`${origin}/components/${componentId}/bind`);
  await expect(page.getByText('SonarQube project key')).toBeVisible();

  await page.getByRole('link', { name: 'GitLab' }).click();
  await expect(page.getByText('GitLab project path')).toBeVisible();
  await expect(page.locator('input[name="base_url"]')).toBeVisible();

  await page.getByRole('link', { name: 'Sentry' }).click();
  await expect(page.getByText('Sentry project slug')).toBeVisible();
  // The mode field is a dropdown now, not a native select.
  await expect(dropdownTrigger(page, 'mode')).toBeVisible();
  // Switching kinds must not leave GitLab's fields lying around under a
  // stale name — that would silently corrupt the submitted config.
  await expect(page.locator('input[name="default_branch"]')).toHaveCount(0);
});

test('bind wizard creates a connection and a binding together, real browser round-trip', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('bindwiz2-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const componentId = await seedProjectAndComponent(orgId);

  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await page.goto(`${origin}/components/${componentId}/bind?kind=gitlab`);

  await page.locator('input[name="external_key"]').fill('alurkerja/billing-api');
  await page.locator('input[name="new_connection_name"]').fill('gitlab.com');
  await page.locator('input[name="base_url"]').fill('https://gitlab.com');
  await page.locator('input[name="token"]').fill('glpat-faketoken12345');
  await page.getByRole('button', { name: 'Bind metric' }).click();

  await expect(page).toHaveURL(`${origin}/components/${componentId}`);
  await expect(page.getByText('alurkerja/billing-api')).toBeVisible();
  await expect(page.getByText('via gitlab.com')).toBeVisible();
  await expect(page.getByText('No metrics bound yet')).not.toBeVisible();

  // The htmx "Test" button swaps a live result in place, no page
  // navigation — real Chromium executing real JS, which is exactly what
  // this suite exists to catch that a Go httptest cannot (a prior manual
  // QA pass here found Alpine's x-show silently breaking a sibling
  // element's CSS display; this is the same class of regression).
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(page.getByText(/authentication failed|✓|error/i)).toBeVisible({ timeout: 10_000 });

  await withDB(async (client) => {
    const { rows } = await client.query(
      `SELECT count(*) FROM metric_bindings WHERE org_id=$1 AND component_id=$2 AND kind='gitlab' AND external_key='alurkerja/billing-api'`,
      [orgId, componentId],
    );
    expect(Number(rows[0].count)).toBe(1);
  });
});
