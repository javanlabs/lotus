import { expect, test } from '@playwright/test';

// A failed sign-in ends on the landing page with a sentence saying why,
// instead of the bare "invalid or missing oauth state" page it used to be.
// In saas mode (how this suite runs) that landing page is the apex; the
// redirect itself is covered by internal/authn's handler tests, which can
// drive a fake identity provider end to end.

test('a sign-in notice is shown on the landing page', async ({ page }) => {
  await page.goto('/?notice=login_expired');

  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible();
  await expect(alert).toContainText('expired or was already used');
  await expect(alert).toContainText('Please sign in again');
});

test('an unknown notice code shows nothing and echoes nothing', async ({ page }) => {
  // The code is looked up server-side, never rendered, so a crafted link
  // cannot put text of its own on the page.
  await page.goto('/?notice=' + encodeURIComponent('Your account is locked, call +00 000'));

  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.locator('body')).not.toContainText('Your account is locked');
});
