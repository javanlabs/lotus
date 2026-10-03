import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession } from './helpers/db';
import { seedGuest, seedPlan } from './helpers/qa';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

test('a guest lands on its own work, sees only its project and signs the plan off', async ({ page, context }) => {
  const { subdomain, orgId } = await seedOwnerSession('qa-guest-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const { projectId, planId } = await seedPlan(orgId);
  const { sessionToken } = await seedGuest(orgId, projectId, 'placeholder');
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  try {
    await page.goto(`${origin}/projects`);
    await expect(page).toHaveURL(/\/qa\/my$/);
    const sidebar = page.locator('aside');
    await expect(sidebar.getByRole('link', { name: 'My work' })).toBeVisible();
    await expect(sidebar.getByRole('link', { name: 'Integrations' })).toHaveCount(0);
    await sidebar.getByRole('link', { name: 'Portal' }).click();
    await expect(page).toHaveURL(new RegExp(`/projects/portal/qa$`));

    await page.goto(`${origin}/projects/portal/qa/plans/${planId}`);
    await page.getByRole('button', { name: 'Sign off…' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Rejected').check();
    await dialog.getByRole('button', { name: 'Sign off' }).click();
    await expect(dialog.getByText('Say what is wrong in the note.')).toBeVisible();
    await dialog.getByRole('textbox', { name: 'Note' }).fill('Login invalid shows no message');
    await dialog.getByRole('button', { name: 'Sign off' }).click();

    const row = page.locator('[data-signoff-scope="Whole plan"]');
    await expect(row.getByText('Rejected')).toBeVisible();
    await expect(row.getByText('Login invalid shows no message')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create link…' })).toHaveCount(0);

    const settings = await page.goto(`${origin}/settings/members`);
    expect(settings?.status()).toBe(404);
  } finally {
    await retireOrg(orgId);
  }
});

test('a share link opens the plan report without signing in, until it is revoked', async ({ page, context, browser }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('qa-share-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const { planId } = await seedPlan(orgId);
  const anonymous = await browser.newContext();

  try {
    await page.goto(`${origin}/projects/portal/qa/plans/${planId}`);
    await page.getByRole('button', { name: 'Create link…' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Create link' }).click();
    const link = await page.getByLabel('Share link').inputValue();
    expect(link).toMatch(/\/s\/qa\/[0-9a-f]{64}$/);

    const visitor = await anonymous.newPage();
    await visitor.goto(link);
    await expect(visitor.getByRole('heading', { level: 1, name: 'Regression October' })).toBeVisible();
    await expect(visitor.getByText('Regression core').first()).toBeVisible();
    await expect(visitor.locator('form')).toHaveCount(0);

    await page.getByRole('link', { name: 'Done' }).click();
    await page.getByRole('button', { name: 'Revoke' }).click();
    await expect(page.getByRole('button', { name: 'Revoke' })).toHaveCount(0);
    const gone = await visitor.goto(link);
    expect(gone?.status()).toBe(404);
  } finally {
    await anonymous.close();
    await retireOrg(orgId);
  }
});
