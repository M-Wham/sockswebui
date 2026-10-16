// Screens must not present cached stream state as live when the stream is down.
// Runs alone: one login per file. Going offline drops the open event stream.
import { expect, test, type Page } from '@playwright/test';
import { apiCall, ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

const base = () => process.env.SMOKE_BASE_URL!;

test.describe.configure({ mode: 'serial' });
let page: Page;

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  await loginOrSetup(page, base());
  await ensureInventorySlot(page, base());
});

test.afterAll(async () => {
  await page?.close();
});

const side = () => page.locator('aside.sidebar');

test('Slots screen opened while the stream is down is not Live', async () => {
  await page.goto(base() + '/#/');
  await expect(page.getByText('Live: updates stream from the device.')).toBeVisible();
  await page.route('**/api/events', (route) => route.abort());
  await page.goto(base() + '/#/logs');
  await page.goto(base() + '/#/'); // joining reconnects, which now fails
  await expect(side().getByText('Service: unreachable')).toBeVisible();
  await expect(page.getByText('Connecting to live updates…')).toBeVisible();
  await page.goto(base() + '/#/logs');
  await page.goto(base() + '/#/'); // cache still holds the old rows
  await expect(page.getByText('Connecting to live updates…')).toBeVisible();
  await expect(page.getByText('Live: updates stream from the device.')).toHaveCount(0);
  await page.unroute('**/api/events');
  await page.goto(base() + '/#/logs');
  await page.goto(base() + '/#/');
  await expect(page.getByText('Live: updates stream from the device.')).toBeVisible({ timeout: 20_000 });
});

test('editor header uses the loaded state, not the cache, when the stream is down', async () => {
  await apiCall(page, 'POST', '/api/slots/4/stop');
  await page.goto(base() + '/#/slots/4');
  const badge = page.locator('[data-state]').first();
  await expect(badge).toHaveAttribute('data-state', 'STOPPED');
  await apiCall(page, 'POST', '/api/slots/4/start'); // the cache now says otherwise
  await expect(badge).not.toHaveAttribute('data-state', 'STOPPED');
  await page.route('**/api/events', (route) => route.abort());
  await page.goto(base() + '/#/');
  await expect(side().getByText('Service: unreachable')).toBeVisible();
  await apiCall(page, 'POST', '/api/slots/4/stop'); // the dead stream never hears this
  await page.goto(base() + '/#/slots/4');
  await expect(page.locator('[data-state]').first()).toHaveAttribute('data-state', 'STOPPED');
  await page.unroute('**/api/events');
});
