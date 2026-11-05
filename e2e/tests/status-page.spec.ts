import { expect, test, type Page } from '@playwright/test';
import { seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

async function signIn(page: Page, prefix: string) {
  const { sessionToken, orgId, subdomain } = await seedOwnerSession(prefix, 'placeholder');
  const origin = tenantOrigin(subdomain);
  await page.context().addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  return { orgId, origin };
}

async function seedProject(orgId: string, published: boolean): Promise<string> {
  return withDB(async (c) => {
    const r = await c.query<{ id: string }>(
      `INSERT INTO projects (org_id, slug, name, status_page_enabled_at) VALUES ($1, 'pay', 'Payments', $2) RETURNING id`,
      [orgId, published ? new Date() : null],
    );
    return r.rows[0].id;
  });
}

test('status page is off until an owner publishes it', async ({ page, browser }) => {
  const { orgId, origin } = await signIn(page, 'status-publish-e2e');
  await seedProject(orgId, false);

  // A client: no cookie at all.
  const client = await browser.newContext();
  const anon = await client.newPage();
  expect((await anon.goto(`${origin}/status/pay`))?.status()).toBe(404);

  await page.goto(`${origin}/projects/pay`);
  const publish = page.getByTestId('status-publish');
  await expect(publish.getByTestId('status-page-switch')).toHaveAttribute('aria-checked', 'false');
  await expect(publish.getByTestId('status-page-url')).toHaveCount(0);
  await publish.getByTestId('status-page-switch').click();
  await expect(page.getByTestId('status-publish').getByTestId('status-page-switch')).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByTestId('status-page-url')).toHaveValue(`${origin}/status/pay`);
  await expect(page.getByTestId('status-page-copy')).toBeVisible();

  expect((await anon.goto(`${origin}/status/pay`))?.status()).toBe(200);
  await expect(anon.getByRole('heading', { name: 'Payments' })).toBeVisible();
  await expect(anon.getByTestId('status-word')).toHaveText('No data');

  await page.getByTestId('status-page-switch').click();
  await expect(page.getByTestId('status-page-switch')).toHaveAttribute('aria-checked', 'false');
  expect((await anon.goto(`${origin}/status/pay`))?.status()).toBe(404);
  await client.close();
});

test('picking a date shows that day', async ({ browser }) => {
  const { orgId, subdomain } = await seedOwnerSession('status-dates-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const projectId = await seedProject(orgId, true);
  await withDB(async (c) => {
    const day = async (d: string, status: string, score: number, kinds: object) =>
      c.query(
        `INSERT INTO project_status_days (org_id, project_id, day, status, score, components)
         VALUES ($1, $2, $3::date, $4, $5, $6::jsonb)`,
        [orgId, projectId, d, status, score, JSON.stringify([{ name: 'ledger-api', kinds }])],
      );
    await day('2026-10-20', 'fail', 62, { sonarqube: { status: 'fail', score: 62, grade: 'D' } });
    await day('2026-10-25', 'pass', 84, { sonarqube: { status: 'pass', score: 84, grade: 'A' } });
  });

  const client = await browser.newContext();
  const page = await client.newPage();
  await page.goto(`${origin}/status/pay?date=2026-10-22`);
  await expect(page.getByTestId('status-word')).toHaveText('Failed');
  await expect(page.getByTestId('status-score')).toHaveText('62');
  await expect(page.getByTestId('status-components')).toContainText('ledger-api');
  await expect(page.getByRole('link', { name: '22 Oct 2026 · Failed' })).toHaveAttribute('aria-current', 'date');

  // The strip is the 30 days ending on the selected date, so Oct 25 is a
  // cell from a later date, where Oct 25's pass is still carried forward.
  await page.goto(`${origin}/status/pay?date=2026-10-31`);
  await expect(page.getByTestId('status-word')).toHaveText('Passed');
  await page.getByRole('link', { name: '25 Oct 2026 · Passed' }).click();
  await expect(page).toHaveURL(`${origin}/status/pay?date=2026-10-25`);
  await expect(page.getByTestId('status-word')).toHaveText('Passed');
  await expect(page.getByTestId('status-score')).toHaveText('84');
  await page.getByRole('link', { name: '22 Oct 2026 · Failed' }).click();
  await expect(page).toHaveURL(`${origin}/status/pay?date=2026-10-22`);
  await expect(page.getByTestId('status-score')).toHaveText('62');

  await page.getByTestId('status-date').fill('2026-09-01');
  await page.getByRole('button', { name: 'Show' }).click();
  await expect(page).toHaveURL(`${origin}/status/pay?date=2026-09-01`);
  await expect(page.getByTestId('status-word')).toHaveText('No data');
  await expect(page.getByTestId('status-score')).toHaveCount(0);
  await expect(page.getByTestId('status-components')).toHaveCount(0);
  await client.close();
});
