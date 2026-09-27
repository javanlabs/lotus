import { expect, test } from '@playwright/test';
import { retireOrg, seedOwnerSession } from './helpers/db';
import { chooseFromDropdown, dropdownTrigger, dropdownValue, dropdownPanel } from './helpers/dropdown';

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
  const invitee = `dropdown-invitee-${subdomain}@example.com`; // invitations are capped per address per day
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/members`);
  // The invite form is a modal now, so the dropdown only exists on screen
  // once it is open.
  await page.getByRole('button', { name: 'Invite a member' }).click();

  // Untouched, it carries the default — the same "the first option is
  // selected" contract a native select has, which forms here rely on.
  expect(await dropdownValue(page, 'role')).toBe('member');

  await chooseFromDropdown(page, 'role', 'Admin');
  expect(await dropdownValue(page, 'role')).toBe('admin');
  await expect(dropdownTrigger(page, 'role')).toHaveValue(/Admin/);

  await page.locator('input[name="email"]').fill(invitee);
  await page.getByRole('button', { name: 'Send invite' }).click();
  await page.getByRole('button', { name: 'Done' }).click();

  // The value chosen in the dropdown is what reached the server: the
  // pending invite is for an Admin, not the default Member.
  // Scoped to <main>: the toast sits outside it and repeats the address,
  // so an unscoped text locator would match the toast too.
  const row = page.locator('main').locator('div', { hasText: invitee }).last();
  await expect(row).toContainText(/admin/i);

  await retireOrg(orgId);
});

test('a dropdown opens, moves and selects from the keyboard alone', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('dropdown-keys-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);

  await page.goto(`${origin}/settings/members`);
  await page.getByRole('button', { name: 'Invite a member' }).click();
  const trigger = dropdownTrigger(page, 'role');
  await trigger.focus();

  // ArrowDown opens, which is the ARIA combobox convention. The dropdown
  // used to be a button-triggered select that opened on Enter; it is an
  // input-first combobox now, where Enter selects the highlighted option
  // and does nothing while the list is closed.
  // The opening ArrowDown also advances the highlight, so this lands on
  // Admin — the point being that the value chosen is not the default.
  await trigger.press('ArrowDown');
  await expect(dropdownPanel(page, 'role')).toBeVisible();
  await expect(trigger).toHaveAttribute('aria-expanded', 'true');

  await trigger.press('Enter');
  expect(await dropdownValue(page, 'role')).toBe('admin');
  await expect(dropdownPanel(page, 'role')).toBeHidden();

  // Escape closes without choosing. The value surviving a dismissed panel
  // is exactly where a hand-rolled dropdown usually goes wrong.
  await trigger.press('ArrowDown');
  await trigger.press('ArrowDown');
  await trigger.press('Escape');
  await expect(dropdownPanel(page, 'role')).toBeHidden();
  expect(await dropdownValue(page, 'role')).toBe('admin');

  await retireOrg(orgId);
});
