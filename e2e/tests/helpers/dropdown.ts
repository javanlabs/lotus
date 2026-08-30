import { expect, type Page } from '@playwright/test';

// Lotus has no native <select> left: every dropdown is the styled
// lotus_select component (web/templates/components/select.html), because a
// browser will not let a design system style the native popup. Playwright's
// selectOption() therefore no longer applies — these helpers drive the
// component the way a person does.
//
// The component is Basecoat's combobox with a Lotus skin. It replaced a
// hand-rolled Alpine dropdown whose panel was teleported to <body> to
// escape the cards' backdrop-filter; Basecoat keeps its popover inside the
// component, so the panel is now looked up within the root rather than
// page-wide.

/** The root element of the dropdown submitting `name`. */
export function dropdown(page: Page, name: string) {
  return page
    .locator('.lotus-combobox')
    .filter({ has: page.locator(`input[type="hidden"][name="${name}"]`) });
}

/** The value the form will actually submit for `name` — the component's hidden input. */
export async function dropdownValue(page: Page, name: string): Promise<string> {
  return page.locator(`.lotus-combobox input[type="hidden"][name="${name}"]`).inputValue();
}

/** The combobox input for `name` — what a person focuses and types into. */
export function dropdownTrigger(page: Page, name: string) {
  return dropdown(page, name).getByRole('combobox');
}

/** The popover holding the options for `name`. */
export function dropdownPanel(page: Page, name: string) {
  return dropdown(page, name).locator('[data-popover]');
}

// chooseFromDropdown opens the dropdown for form field `name` and clicks
// the option with the given visible label.
export async function chooseFromDropdown(page: Page, name: string, optionLabel: string): Promise<void> {
  await dropdownTrigger(page, name).click();
  const panel = dropdownPanel(page, name);
  await expect(panel).toBeVisible();
  await panel.getByRole('option', { name: optionLabel, exact: true }).click();
}
