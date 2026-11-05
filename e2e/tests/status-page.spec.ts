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

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// The WIB (UTC+7) day `offset` days from today, as the status page names
// it: ?date= value, bar label ("2 Nov 2026"), day of the month and month
// marker ("NOV").
function wibDay(offset: number) {
  const now = new Date(Date.now() + 7 * 3600_000);
  const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + offset));
  const month = MONTHS[d.getUTCMonth()];
  return {
    iso: d.toISOString().slice(0, 10),
    label: `${d.getUTCDate()} ${month} ${d.getUTCFullYear()}`,
    dayNum: d.getUTCDate(),
    month: month.toUpperCase(),
  };
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
  // Behind the settings gear, not on the page's surface.
  await expect(page.getByTestId('status-publish')).toBeHidden();
  await page.getByTestId('project-settings-toggle').click();
  const publish = page.getByTestId('status-publish');
  await expect(publish.getByTestId('status-page-switch')).toHaveAttribute('aria-checked', 'false');
  await expect(publish.getByTestId('status-page-url')).toHaveCount(0);
  await publish.getByTestId('status-page-switch').click();
  await expect(page.getByTestId('status-publish').getByTestId('status-page-switch')).toHaveAttribute('aria-checked', 'true');
  // Back on the page with the settings still open, the URL in view.
  await expect(page.getByTestId('status-page-url')).toBeVisible();
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

test('the strip picks a day', async ({ browser }) => {
  const { orgId, subdomain } = await seedOwnerSession('status-dates-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  const projectId = await seedProject(orgId, true);
  // The strip always ends on the server's today (WIB), so the seeded days
  // are placed relative to it: a failure 12 days ago, a pass 7 days ago.
  await withDB(async (c) => {
    const day = async (offset: number, status: string, score: number, kinds: object) =>
      c.query(
        `INSERT INTO project_status_days (org_id, project_id, day, status, score, components)
         VALUES ($1, $2, $3::date, $4, $5, $6::jsonb)`,
        [orgId, projectId, wibDay(offset).iso, status, score, JSON.stringify([{ name: 'ledger-api', kinds }])],
      );
    await day(-12, 'fail', 62, { sonarqube: { status: 'fail', score: 62, grade: 'D' } });
    await day(-7, 'pass', 84, { sonarqube: { status: 'pass', score: 84, grade: 'A' } });
  });
  const bar = (page: Page, offset: number, verdict: string) =>
    page.getByRole('link', { name: `${wibDay(offset).label} · ${verdict}`, exact: true });

  const client = await browser.newContext();
  const page = await client.newPage();
  // Ten days ago: the failure 12 days ago carried forward.
  await page.goto(`${origin}/status/pay?date=${wibDay(-10).iso}`);
  await expect(page.getByTestId('status-word')).toHaveText('Failed');
  await expect(page.getByTestId('status-score')).toHaveText('62');
  await expect(page.getByTestId('status-components')).toContainText('ledger-api');
  await expect(bar(page, -10, 'Failed')).toHaveAttribute('aria-current', 'date');
  await expect(page.getByTestId('status-date')).toHaveCount(0);

  // The strip is still the 30 days ending today, not ending on the
  // selected day: today's bar is there (passing), just not highlighted.
  const strip = page.getByTestId('status-strip');
  await expect(strip.getByRole('link')).toHaveCount(30);
  await expect(bar(page, 0, 'Passed')).not.toHaveAttribute('aria-current', 'date');
  await expect(strip.locator('[aria-current]')).toHaveCount(1);
  // Every bar carries its day of the month inside it; the month is marked
  // on the first bar and on each 1st.
  const days = Array.from({ length: 30 }, (_, i) => wibDay(i - 29));
  await expect(strip.getByTestId('status-strip-day')).toHaveText(days.map((d) => String(d.dayNum)));
  await expect(strip.getByTestId('status-strip-month')).toHaveText(
    days.filter((d, i) => i === 0 || d.dayNum === 1).map((d) => d.month),
  );
  await expect(bar(page, -29, 'No data').getByTestId('status-strip-month')).toHaveText(days[0].month);

  // Clicking a bar selects its day and leaves the strip where it is.
  await bar(page, -7, 'Passed').click();
  await expect(page).toHaveURL(`${origin}/status/pay?date=${wibDay(-7).iso}`);
  await expect(page.getByTestId('status-word')).toHaveText('Passed');
  await expect(page.getByTestId('status-score')).toHaveText('84');
  await expect(bar(page, -7, 'Passed')).toHaveAttribute('aria-current', 'date');
  await expect(strip.getByTestId('status-strip-day').first()).toHaveText(String(days[0].dayNum));
  await expect(strip.getByTestId('status-strip-day').last()).toHaveText(String(days[29].dayNum));
  await bar(page, -10, 'Failed').click();
  await expect(page).toHaveURL(`${origin}/status/pay?date=${wibDay(-10).iso}`);
  await expect(page.getByTestId('status-score')).toHaveText('62');

  // A day older than the strip is its first day; one in the future is today.
  await page.goto(`${origin}/status/pay?date=2020-01-01`);
  await expect(bar(page, -29, 'No data')).toHaveAttribute('aria-current', 'date');
  await expect(page.getByTestId('status-word')).toHaveText('No data');
  await expect(page.getByTestId('status-score')).toHaveCount(0);
  await expect(page.getByTestId('status-components')).toHaveCount(0);
  await page.goto(`${origin}/status/pay?date=2099-01-01`);
  await expect(bar(page, 0, 'Passed')).toHaveAttribute('aria-current', 'date');
  await expect(page.getByTestId('status-score')).toHaveText('84');
  await client.close();
});
