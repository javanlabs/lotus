import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// Switching kind swaps the modal body over htmx. Three things used to go
// wrong on that swap, all of them visible only in a real browser.
test('switching connection kind keeps the modal still and the form usable', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('conn-modal-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/connections`);
  await page.getByRole('button', { name: 'New connection' }).click();

  const dialog = page.locator('dialog.lotus-modal[open]');
  const name = dialog.locator('#connection-name');
  await expect(dialog).toBeVisible();

  // 1. The placeholder belongs to the selected kind. It was hardcoded to
  //    "e.g. gitlab.com", so three of the four tabs suggested a GitLab host.
  await expect(name).toHaveAttribute('placeholder', /sonarqube/i);

  // 2. A name already typed survives the swap. The fragment handler always
  //    accepted a name parameter; the tab buttons never sent one, so the
  //    field silently emptied.
  await name.fill('harbor.example.com');

  // 3. The dialog's top edge does not move. Trivy has three connection
  //    fields where SonarQube has two, and a vertically centred dialog
  //    shares that difference between both edges — so the heading, the
  //    tabs and the name field all slid under the pointer mid-click.
  const topBefore = (await dialog.boundingBox())!.y;

  await dialog.getByRole('button', { name: 'Trivy (Harbor)' }).click();
  await expect(name).toHaveAttribute('placeholder', /harbor/i);
  await expect(name).toHaveValue('harbor.example.com');
  expect((await dialog.boundingBox())!.y).toBe(topBefore);

  // The body really did grow — otherwise the assertion above proves nothing.
  await expect(dialog.locator('input[name="username"]')).toBeVisible();

  await dialog.getByRole('button', { name: 'Sentry' }).click();
  await expect(name).toHaveAttribute('placeholder', /sentry/i);
  expect((await dialog.boundingBox())!.y).toBe(topBefore);

  await dialog.getByRole('button', { name: 'GitLab' }).click();
  await expect(name).toHaveAttribute('placeholder', /gitlab/i);
  await expect(name).toHaveValue('harbor.example.com');
  expect((await dialog.boundingBox())!.y).toBe(topBefore);

  // Leave no live organization behind: a single-mode `make dev` on the
  // same database refuses to boot with more than one.
  await retireOrg(orgId);
});

// The name is not secret, but the credential fields in the same form are,
// and a GET puts whatever it includes into the query string.
test('switching kind does not put credential fields in the URL', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('conn-modal-url-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/connections`);
  await page.getByRole('button', { name: 'New connection' }).click();
  const dialog = page.locator('dialog.lotus-modal[open]');
  await dialog.locator('#connection-name').fill('sonar.example.com');
  await dialog.locator('input[name="token"]').fill('super-secret-token');

  const requests: string[] = [];
  page.on('request', (r) => {
    if (r.url().includes('/settings/connections/form')) requests.push(r.url());
  });
  await dialog.getByRole('button', { name: 'Trivy (Harbor)' }).click();
  await expect(dialog.locator('input[name="username"]')).toBeVisible();

  expect(requests.length).toBeGreaterThan(0);
  for (const url of requests) {
    expect(url).toContain('name=sonar.example.com');
    expect(url).not.toContain('super-secret-token');
    expect(url).not.toContain('token=');
  }

  await retireOrg(orgId);
});
