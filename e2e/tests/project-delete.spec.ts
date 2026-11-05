import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

test('a project is deleted from its own page, not from the list', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('proj-del-e2e', 'placeholder');
  const origin = `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  await withDB((c) => c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1,'pay','Payments')`, [orgId]));

  await page.goto(`${origin}/projects`);
  await expect(page.getByRole('button', { name: 'Delete project' })).toHaveCount(0);

  await page.goto(`${origin}/projects/pay`);
  await expect(page.getByRole('button', { name: 'Delete project' })).toBeHidden();
  await page.getByTestId('project-settings-toggle').click();
  await page.getByRole('button', { name: 'Delete project' }).click();
  const dialog = page.locator('dialog[open]');
  await dialog.locator('input[name="confirm_name"]').fill('nope');
  await dialog.getByRole('button', { name: 'Delete project' }).click();
  await expect(dialog.getByText("Type the project's name")).toBeVisible();

  await dialog.locator('input[name="confirm_name"]').fill('Payments');
  await dialog.getByRole('button', { name: 'Delete project' }).click();
  await expect(page).toHaveURL(`${origin}/projects`);
  await expect(page.getByText('Project Payments deleted')).toBeVisible();

  const res = await page.goto(`${origin}/projects/pay`);
  expect(res?.status()).toBe(404);
  await retireOrg(orgId);
});
