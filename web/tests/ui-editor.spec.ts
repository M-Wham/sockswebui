// Slot editor header checks. Runs alone: one login per file.
import { expect, test, type Page } from '@playwright/test';
import { apiCall, ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

const base = () => process.env.SMOKE_BASE_URL!;

let page: Page;

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  await loginOrSetup(page, base());
  await ensureInventorySlot(page, base());
});

test.afterAll(async () => {
  await page?.close();
});

test('header state follows the live stream without a reload', async () => {
  await apiCall(page, 'POST', '/api/slots/4/stop');
  await page.goto(base() + '/#/slots/4');
  const badge = page.locator('[data-state]').first();
  await expect(badge).toHaveAttribute('data-state', 'STOPPED');
  // Starting drives the slot to STARTING/UP/ERROR; the open editor must show it.
  await apiCall(page, 'POST', '/api/slots/4/start');
  await expect(badge).not.toHaveAttribute('data-state', 'STOPPED');
  await apiCall(page, 'POST', '/api/slots/4/stop');
  await expect(badge).toHaveAttribute('data-state', 'STOPPED');
});

test('header badge follows the saved kill switch, not the unsaved radio', async () => {
  // Slot 4 is saved as allow-direct; report it DEGRADED (the hermetic server cannot make it so).
  await page.route('**/api/slots/4', async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    const body = (await res.json()) as { status?: Record<string, unknown> };
    body.status = { ...(body.status ?? {}), state: 'DEGRADED' };
    await route.fulfill({ response: res, json: body });
  });
  // The live stream wins over the loaded state, so report DEGRADED there too.
  const frame = `event: snapshot\ndata: ${JSON.stringify({ slots: [{ id: 4, name: 'inventory', state: 'DEGRADED' }] })}\n\n`;
  await page.route('**/api/events', (route) =>
    route.fulfill({ status: 200, contentType: 'text/event-stream', body: frame }),
  );
  await page.goto(base() + '/#/slots/4');
  await page.reload(); // reconnect the stream through the mock
  const text = /device route \(allow-direct\)/;
  await expect(page.getByText(text).first()).toBeVisible();

  await page.getByRole('radio', { name: /^Block/ }).check();
  await expect(page.getByText(text).first()).toBeVisible();
  await expect(page.getByText(/kill switch active, new connections are refused/)).toHaveCount(0);
});
