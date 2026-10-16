// Mobile guard: no screen may scroll horizontally at 360 px. Written against the current UI.
import { expect, test, type Page } from '@playwright/test';
import { mullvadZip } from './fixtures/mkzip';
import { ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

const base = () => process.env.SMOKE_BASE_URL!;

test.use({ viewport: { width: 360, height: 740 } });

async function fits(page: Page, what: string): Promise<void> {
  await page.waitForTimeout(200);
  const sw = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(sw, what).toBeLessThanOrEqual(360);
}

test('login fits 360', async ({ page }) => {
  await page.goto(base() + '/#/login');
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  await fits(page, 'login');
});

test('screens fit 360', async ({ page }) => {
  await loginOrSetup(page, base());
  await ensureInventorySlot(page, base());

  await page.goto(base() + '/#/');
  await expect(page.locator('tr[data-slot]')).toHaveCount(4);
  await fits(page, 'dashboard');

  await page.goto(base() + '/#/endpoints');
  await expect(page.getByTestId('wg-list')).toBeVisible();
  await fits(page, 'endpoints');
  await page.getByRole('button', { name: 'Add proxy' }).click();
  await expect(page.locator('form.proxy-form')).toBeVisible();
  await page.getByTestId('import-input').setInputFiles({
    name: 'mullvad_wireguard_linux_se.zip',
    mimeType: 'application/zip',
    buffer: mullvadZip(),
  });
  await expect(page.getByTestId('import-results')).toBeVisible();
  await fits(page, 'endpoints with proxy form and import results');

  await page.goto(base() + '/#/slots/4');
  await expect(page.getByRole('heading', { name: 'Slot 4' })).toBeVisible();
  await fits(page, 'slot editor');

  await page.goto(base() + '/#/logs');
  await expect(page.getByRole('heading', { name: 'Logs', exact: true })).toBeVisible();
  await fits(page, 'logs');

  await page.goto(base() + '/#/settings');
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
  await fits(page, 'settings');
  await page.getByTestId('export-config').click();
  await expect(page.locator('dialog[open]')).toBeVisible();
  await fits(page, 'settings with export dialog');
});
