import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession } from './helpers/db';
import { chooseFromDropdown, dropdownTrigger, dropdownValue } from './helpers/dropdown';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';

function tenantOrigin(subdomain: string): string {
  return `http://${subdomain}.${BASE_DOMAIN}:${PORT}`;
}

// Every native <select> is gone, replaced by the styled lotus_select — so
// the component now sits on the path of every form that had one: inviting a
// member, creating a component, binding a metric, filtering a snapshot.
// These cover what a native select gave for free and this has to implement.
//
// Auto-submit (a choice that posts its form with no Save button) is covered
// where it actually ships, on the snapshot-date filter in
// metric-detail.spec.ts.

test('a dropdown submits the value a native select would have submitted', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('dropdown-value-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/members`);

  // Untouched, it carries the default — the same "the first option is
  // selected" contract a native select has, which forms here rely on.
  expect(await dropdownValue(page, 'role')).toBe('member');

  await chooseFromDropdown(page, 'role', 'Admin');
  expect(await dropdownValue(page, 'role')).toBe('admin');
  await expect(dropdownTrigger(page, 'role')).toHaveText(/Admin/);

  await page.locator('input[name="email"]').fill('dropdown-invitee@example.com');
  await page.getByRole('button', { name: 'Send invite' }).click();

  // The value chosen in the dropdown is what reached the server: the
  // pending invite is for an Admin, not the default Member.
  const row = page.locator('div', { hasText: 'dropdown-invitee@example.com' }).last();
  await expect(row).toContainText(/admin/i);

  await retireOrg(orgId);
});

test('a dropdown opens, moves and selects from the keyboard alone', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('dropdown-keys-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/members`);
  const trigger = dropdownTrigger(page, 'role');
  await trigger.focus();

  await trigger.press('Enter');
  await expect(page.locator('.lotus-select-panel').first()).toBeVisible();
  await expect(trigger).toHaveAttribute('aria-expanded', 'true');

  await trigger.press('ArrowDown');
  await trigger.press('Enter');
  expect(await dropdownValue(page, 'role')).toBe('admin');
  await expect(page.locator('.lotus-select-panel').first()).toBeHidden();

  // Escape closes without choosing. The value surviving a dismissed panel
  // is exactly where a hand-rolled dropdown usually goes wrong.
  await trigger.press('Enter');
  await trigger.press('ArrowDown');
  await trigger.press('Escape');
  await expect(page.locator('.lotus-select-panel').first()).toBeHidden();
  expect(await dropdownValue(page, 'role')).toBe('admin');

  await retireOrg(orgId);
});
