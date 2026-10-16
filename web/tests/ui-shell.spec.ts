// Console shell checks (sidebar, top bar, Restart all, service line). Runs alone: one login per file.
import { expect, test, type Page } from '@playwright/test';
import { ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

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

test('sidebar at 1440', async () => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(base() + '/#/');
  const side = page.locator('aside.sidebar');
  await expect(side).toBeVisible();
  const sb = (await side.boundingBox())!;
  expect(sb.x).toBe(0);
  expect(sb.width).toBeGreaterThanOrEqual(170);
  expect(sb.width).toBeLessThanOrEqual(240);
  expect(sb.height).toBeGreaterThanOrEqual(880);
  const mb = (await page.locator('main').boundingBox())!;
  expect(mb.x).toBeGreaterThanOrEqual(sb.width);
  const slots = page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Slots', exact: true });
  await expect(slots).toHaveAttribute('aria-current', 'page');
  const bw = await slots.evaluate((el) => parseFloat(getComputedStyle(el).borderLeftWidth));
  expect(bw).toBeGreaterThanOrEqual(2);
  await expect(page.getByTestId('system-status')).toHaveText(/Service: running/);
  await expect(page.getByTestId('app-version')).toHaveText(/^(dev|v\d\S*)$/);
});

for (const [w, h] of [
  [800, 900],
  [360, 740],
]) {
  test(`top bar under 900 at ${w}`, async () => {
    await page.setViewportSize({ width: w, height: h });
    await page.goto(base() + '/#/');
    const side = page.locator('aside.sidebar');
    await expect(side).toBeVisible();
    const sb = (await side.boundingBox())!;
    expect(sb.width).toBe(w);
    expect(sb.y).toBe(0);
    expect(sb.height).toBeLessThan(260);
    const nav = page.getByRole('navigation', { name: 'Main' });
    const targets = [...(await nav.getByRole('link').all()), page.getByRole('button', { name: 'Log out' })];
    expect(targets.length).toBe(5);
    for (const t of targets) {
      await expect(t).toBeVisible();
      const b = (await t.boundingBox())!;
      expect(b.x + b.width).toBeLessThanOrEqual(w);
    }
    const mb = (await page.locator('main').boundingBox())!;
    expect(mb.y).toBeGreaterThanOrEqual(sb.y + sb.height - 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(w);
  });
}

test.describe('restart all', () => {
  test('cancel, then restart enabled slots, then a start failure', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    const out = await page.evaluate(async () => (await fetch('/api/slots', { credentials: 'same-origin' })).json());
    const enabled: number[] = (out.slots as { id: number; configured?: boolean; enabled?: boolean }[])
      .filter((s) => s.configured && s.enabled)
      .map((s) => s.id);
    let stops = 0;
    let starts = 0;
    let failFirstStart = false;
    await page.route('**/api/slots/*/stop', async (route) => {
      stops++;
      const id = Number(route.request().url().match(/slots\/(\d+)\/stop/)![1]);
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id, state: 'STOPPED' }) });
    });
    await page.route('**/api/slots/*/start', async (route) => {
      starts++;
      const id = Number(route.request().url().match(/slots\/(\d+)\/start/)![1]);
      if (failFirstStart && id === enabled[0]) {
        await route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"boom"}' });
        return;
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id, state: 'STARTING' }) });
    });

    const trigger = page.getByRole('button', { name: 'Restart all' });
    const group = page.getByRole('group', { name: 'Confirm restart all' });
    await trigger.click();
    await expect(group).toBeVisible();
    await expect(page.getByRole('button', { name: 'Yes, restart all' })).toBeFocused();
    await group.getByRole('button', { name: 'Cancel' }).click();
    await expect(group).toBeHidden();
    await expect(trigger).toBeFocused();

    await trigger.click();
    await group.getByRole('button', { name: 'Yes, restart all' }).click();
    await expect(page.getByRole('status').filter({ hasText: /Restarted \d+ slots?\.|No enabled slots to restart\./ })).toBeVisible();
    expect(stops).toBe(enabled.length);
    expect(starts).toBe(enabled.length);

    if (enabled.length > 0) {
      failFirstStart = true;
      await trigger.click();
      await group.getByRole('button', { name: 'Yes, restart all' }).click();
      await expect(page.getByRole('alert').filter({ hasText: 'start failed: boom (slot left stopped and disabled)' })).toBeVisible();
    }
  });
});


test('sidebar service line leaves "running" when the event stream drops', async () => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(base() + '/#/');
  const foot = page.locator('aside.sidebar');
  await expect(foot.getByText('Service: running')).toBeVisible();
  await page.route('**/api/events', (route) => route.abort());
  await page.reload();
  await expect(foot.getByText('Service: unreachable')).toBeVisible();
  await expect(foot.getByText('Service: running')).toHaveCount(0);
  await page.unroute('**/api/events');
  await page.reload();
  await expect(foot.getByText('Service: running')).toBeVisible();
});
