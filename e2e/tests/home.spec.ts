import { expect, test } from '@playwright/test';

test('healthz reports ok', async ({ request }) => {
  const res = await request.get('/healthz');
  expect(res.status()).toBe(200);
  expect(await res.json()).toEqual({ ok: true });
});

test('home page renders with Tailwind styling and htmx wired up', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle(/Lotus/);
  await expect(page.getByRole('heading', { name: 'Lotus' })).toBeVisible();

  const button = page.getByRole('link', { name: 'Check /healthz' });
  await expect(button).toBeVisible();
  // Tailwind's utility classes actually applied (not just present as
  // literal class-attribute text) — a broken build (missing
  // app.built.css) would leave this element with the browser's unstyled
  // default (transparent background), regardless of which color space
  // Tailwind renders in.
  await expect(button).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)');

  await button.click();
  await expect(page.locator('#healthz-result')).toHaveText('{"ok":true}');
});
