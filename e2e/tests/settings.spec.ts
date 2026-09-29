import { expect, test } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';
import { chooseFromDropdown, dropdownValue } from './helpers/dropdown';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

test('settings sub-nav links to all five screens', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('settings-nav-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/organization`);
  // Scoped to the tab strip: Members is also a sidebar item.
  const tabs = page.getByRole('navigation', { name: 'Settings' });
  await expect(tabs.getByRole('link', { name: 'Connections' })).toBeVisible();
  await tabs.getByRole('link', { name: 'Connections' }).click();
  await expect(page).toHaveURL(`${origin}/settings/connections`);
  await tabs.getByRole('link', { name: 'Members' }).click();
  await expect(page).toHaveURL(`${origin}/settings/members`);
  await tabs.getByRole('link', { name: 'Tokens' }).click();
  await expect(page).toHaveURL(`${origin}/settings/tokens`);
  await tabs.getByRole('link', { name: 'Ingest' }).click();
  await expect(page).toHaveURL(`${origin}/settings/ingest`);
});

test('creating a connection through the settings form appears in the list, real browser round-trip', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('settings-conn-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/connections`);
  // Creating a connection is a modal now, opened from the page header.
  await page.getByRole('button', { name: 'New connection' }).click();
  await page.locator('input[name="name"]').fill('gitlab.com');
  await page.locator('input[name="base_url"]').fill('https://gitlab.com');
  await page.locator('input[name="token"]').fill('glpat-realistictoken');
  await page.getByRole('button', { name: 'Create connection' }).click();

  await expect(page).toHaveURL(`${origin}/settings/connections`);
  // Success closes the modal, refreshes the list and toasts.
  await expect(page.locator('.toaster .toast')).toContainText('Connection created');
  await expect(page.getByText('gitlab.com', { exact: true })).toBeVisible();
  await expect(page.getByText('pending', { exact: true })).toBeVisible();
});

test('inviting a member shows it as pending, and revoke removes it, real browser round-trip', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('settings-member-e2e', 'placeholder');
  // Unique per run: invitations are capped per address per day, and the
  // e2e database outlives a run.
  const invitee = `newteammate-${subdomain}@example.com`;
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/members`);
  await page.getByRole('button', { name: 'Invite a member' }).click();
  await page.locator('input[name="email"]').fill(invitee);
  await chooseFromDropdown(page, 'role', 'Member');
  expect(await dropdownValue(page, 'role')).toBe('member');
  await page.getByRole('button', { name: 'Send invite' }).click();
  // Without mail set up the link is the only way in, so the dialog shows it.
  await expect(page.getByLabel('Invitation link')).toHaveValue(/\/invite\/[A-Za-z0-9_-]{43}$/);
  await page.getByRole('button', { name: 'Done' }).click();

  await expect(page).toHaveURL(`${origin}/settings/members`);
  await expect(page.locator('.toaster .toast')).toContainText(`Invitation sent to ${invitee}`);
  await expect(page.locator('main').getByText(invitee)).toBeVisible();

  page.once('dialog', (d) => d.accept());
  await page.getByRole('button', { name: 'Revoke' }).click();
  await expect(page.locator('main').getByText(invitee)).not.toBeVisible();
});

test('organization danger zone requires typing the exact subdomain to delete', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('settings-org-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/organization`);
  await expect(page.getByText('Danger zone')).toBeVisible();

  page.once('dialog', (d) => d.accept());
  await page.locator('input[name="confirm_subdomain"]').fill('the-wrong-subdomain');
  await page.getByRole('button', { name: 'Delete organization' }).click();

  // A mismatched confirmation string must not delete the org — the page
  // redirects back to the settings screen with an inline error, not to
  // the apex (which is where a successful delete would send the browser).
  await expect(page).toHaveURL(new RegExp(`^${origin}/settings/organization`));
  await expect(page.getByText('type the organization', { exact: false })).toBeVisible();
});

test('creating a personal access token shows the plaintext exactly once', async ({ page, context }) => {
  const { sessionToken, subdomain } = await seedOwnerSession('settings-token-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/tokens`);
  await page.locator('input[name="name"]').first().fill('CI token');
  await page.getByRole('button', { name: 'Create token' }).click();

  await expect(page.getByText("won't be shown again")).toBeVisible();

  await page.reload();
  await expect(page.getByText("won't be shown again")).not.toBeVisible();
  await expect(page.getByText('CI token')).toBeVisible();
});

test('activity feed shows a seeded sync run', async ({ page, context }) => {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession('settings-activity-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await withDB(async (client) => {
    const project = await client.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, [orgId]);
    const component = await client.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, [orgId, project.rows[0].id]);
    const conn = await client.query(`INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','gitlab.com','https://gitlab.com') RETURNING id`, [orgId]);
    const binding = await client.query(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'gitlab','g/b','{}') RETURNING id`, [orgId, component.rows[0].id, conn.rows[0].id]);
    await client.query(
      `INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status, started_at, finished_at) VALUES ($1,$2,$3,'gitlab','manual','ok', now() - interval '5 minutes', now())`,
      [orgId, conn.rows[0].id, binding.rows[0].id],
    );
  });

  await page.goto(`${origin}/activity`);
  await expect(page.getByText('ok', { exact: true })).toBeVisible();
  await expect(page.getByText('manual', { exact: true })).toBeVisible();
});
