import { expect, type Page } from '@playwright/test';

// Lotus has no native <select> left: every dropdown is the styled
// lotus_select component (web/templates/components/select.html), because a
// browser will not let a design system style the native popup. Playwright's
// selectOption() therefore no longer applies — these helpers drive the
// component the way a person does.

/** The value the form will actually submit for `name` — the component's hidden input. */
export async function dropdownValue(page: Page, name: string): Promise<string> {
  return page.locator(`.lotus-select input[type="hidden"][name="${name}"]`).inputValue();
}

/** The label currently shown on the closed trigger for `name`. */
export function dropdownTrigger(page: Page, name: string) {
  return page
    .locator('.lotus-select')
    .filter({ has: page.locator(`input[type="hidden"][name="${name}"]`) })
    .getByRole('combobox');
}

// chooseFromDropdown opens the dropdown for form field `name` and clicks
// the option with the given visible label. The panel is teleported to
// <body> (it must escape the cards' backdrop-filter to position itself),
// so the option is looked up page-wide rather than inside the select.
export async function chooseFromDropdown(page: Page, name: string, optionLabel: string): Promise<void> {
  await dropdownTrigger(page, name).click();
  const panel = page.locator('.lotus-select-panel:visible');
  await expect(panel).toBeVisible();
  await panel.getByRole('option', { name: optionLabel, exact: true }).click();
}
