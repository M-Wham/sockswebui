import { expect, test } from '@playwright/test';
import { mullvadZip } from './fixtures/mkzip';
import { loginOrSetup } from './fixtures/uiState';

test('setup, login, import, route, start, UP, hop killed, DEGRADED', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  // Any failed or >= 400 response is a bug (a real-hardware run once saw a console 404). Nothing is allowlisted.
  const bad: string[] = [];
  page.on('response', (r) => {
    if (r.status() >= 400) bad.push(`${r.status()} ${r.url()}`);
  });
  // EventSource /api/events is aborted by page.goto navigations; that abort is expected, not a bug.
  page.on('requestfailed', (r) => !r.url().endsWith('/api/events') && bad.push(`FAILED ${r.url()} ${r.failure()?.errorText ?? ''}`));
  // Set up or log in; csp.spec.ts runs first on a fresh server and the session is shared.
  await loginOrSetup(page, base);
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();

  // Headless chromium never fetches the favicon, a real browser does (the same 404). Fetch it explicitly.
  const iconHref = await page.locator('link[rel~="icon"]').first().getAttribute('href');
  expect(iconHref, 'index.html declares a favicon').toBeTruthy();
  expect((await page.request.get(new URL(iconHref!, page.url()).toString())).status()).toBe(200);

  // import a runtime-generated Mullvad-style zip
  await page.getByRole('link', { name: 'Endpoints' }).click();
  await page.getByTestId('import-input').setInputFiles({
    name: 'mullvad_wireguard_linux_se.zip',
    mimeType: 'application/zip',
    buffer: mullvadZip(),
  });
  const results = page.getByTestId('import-results');
  await expect(results.getByText('imported', { exact: true })).toHaveCount(2);
  await expect(page.getByTestId('wg-list')).toContainText(/Sweden|\bSE\b/i);

  // slot 2 editor: build the route with buttons/keyboard from rigWG2
  await page.goto(base + '/#/slots/2');
  await expect(page.getByRole('heading', { name: 'Slot 2' })).toBeVisible();
  await page.getByLabel('Name', { exact: true }).fill('smoke');
  await page.getByLabel('Bind address').fill('127.0.0.1');
  await page.getByLabel('Port', { exact: true }).fill('1081');
  await page.getByLabel('Interval (s)').fill('2');
  await page.getByLabel('Fail threshold').fill('2');
  await page.getByLabel('Probe URL').fill('http://echo.rig.invalid/');
  const add = page.getByRole('button', { name: 'Add rigWG2 to route', exact: true });
  await add.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('list', { name: 'Route hops' })).toContainText('rigWG2');
  await page.getByRole('button', { name: 'Save slot' }).click();
  await expect(page.getByRole('status').filter({ hasText: /saved/i })).toBeVisible();

  // start from the dashboard; badge must come from the server
  await page.goto(base + '/#/');
  const card = page.locator('tr[data-slot="2"]');
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'STOPPED');
  await card.getByRole('button', { name: /^Start/ }).click();
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });

  expect(bad, 'failed requests during dashboard flow').toEqual([]);

  // kill the hop with the page's session + CSRF
  const status = await page.evaluate(async () => {
    const r = await fetch('/api/dev/rig/pause', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'X-CSRF-Token': sessionStorage.getItem('sockswebui.csrf') ?? '' },
    });
    return r.status;
  });
  expect(status).toBe(204);
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'DEGRADED', { timeout: 90_000 });

  // mobile layout (review note): no horizontal overflow at 360px
  await page.setViewportSize({ width: 360, height: 700 });
  for (const hash of ['#/logs', '#/settings']) {
    await page.goto(base + '/' + hash);
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw, hash).toBeLessThanOrEqual(360);
  }
});
