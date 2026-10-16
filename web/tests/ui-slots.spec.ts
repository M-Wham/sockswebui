// Slots screen and slot editor panel layout. Runs alone: one login per file.
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

test.describe('slots header, stats and chart', () => {
  test('stats panel and chart', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
    const stats = page.getByTestId('stats');
    await expect(stats).toBeVisible();
    for (const l of ['Slots running', 'Degraded', 'Total down', 'Total up']) {
      await expect(stats.locator('dt', { hasText: l })).toBeVisible();
    }
    await expect(stats.locator('dt', { hasText: 'Slots running' }).locator('+ dd')).toHaveText(/^\d\/4$/);
    const fig = page.getByTestId('throughput-chart');
    await expect(fig).toBeVisible();
    await expect(fig.locator('rect.down')).toHaveCount(30);
    await expect(fig.locator('rect.up')).toHaveCount(30);
    await expect(fig.getByText('Down', { exact: true })).toBeVisible();
    await expect(fig.getByText('Up', { exact: true })).toBeVisible();
    const fb = (await fig.boundingBox())!;
    const sb = (await fig.locator('svg[role="img"]').boundingBox())!;
    expect(sb.x).toBeGreaterThanOrEqual(fb.x);
    expect(sb.x + sb.width).toBeLessThanOrEqual(fb.x + fb.width + 1);
    expect(sb.y + sb.height).toBeLessThanOrEqual(fb.y + fb.height + 1);
  });

  test('Import configs link focuses the drop zone input', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    await page.getByRole('link', { name: 'Import configs' }).click();
    await expect(page).toHaveURL(/#\/import$/);
    await expect(page.getByRole('heading', { name: 'Endpoints', exact: true })).toBeVisible();
    await expect(page.getByTestId('import-input')).toBeFocused();
  });

  test('panels stack at 360', async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(base() + '/#/');
    const s = (await page.getByTestId('stats').boundingBox())!;
    const c = (await page.getByTestId('throughput-chart').boundingBox())!;
    expect(c.y).toBeGreaterThan(s.y + s.height - 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  });
});

test.describe('proxy slots table and endpoints summary', () => {
  test('table at 1440', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    await page.reload();
    const rows = page.locator('tr[data-slot]');
    await expect(rows).toHaveCount(4);
    for (const r of await rows.all()) {
      expect(await r.evaluate((el) => getComputedStyle(el).display)).toBe('table-row');
    }
    const table = page.getByRole('region', { name: 'Proxy slots' }).or(page.locator('section[aria-labelledby="slots-title"]'));
    for (const h of ['Slot', 'State', 'Route', 'Exit', 'Latency', 'Down / Up']) {
      await expect(table.getByRole('columnheader', { name: h, exact: true })).toBeVisible();
    }
    const row4 = page.locator('tr[data-slot="4"]');
    await expect(row4.getByText('127.0.0.1:1084')).toBeVisible();
    await expect(row4.locator('[data-killswitch="allow-direct"]')).toBeVisible();
  });

  test('rows stack at 360', async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(base() + '/#/');
    await page.reload();
    const rows = page.locator('tr[data-slot]');
    await expect(rows).toHaveCount(4);
    for (const r of await rows.all()) {
      expect(await r.evaluate((el) => getComputedStyle(el).display)).not.toBe('table-row');
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  });

  test('endpoints summary', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    const sec = page.getByTestId('endpoints-summary');
    await expect(sec).toBeVisible();
    await expect(sec.getByRole('heading', { name: 'Endpoints', exact: true })).toBeVisible();
    for (const h of ['Name', 'Type', 'Location', 'Used by']) {
      await expect(sec.getByRole('columnheader', { name: h, exact: true })).toBeVisible();
    }
    const n = await sec.locator('tbody tr').count();
    expect(n).toBeGreaterThan(0);
    expect(n).toBeLessThanOrEqual(8);
  });
});

test.describe('slot editor panels', () => {
  test('header, panels, add a hop, kill switch cards at 1440', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/slots/4');
    const h = page.getByRole('heading', { name: 'Edit slot 4', exact: true });
    await expect(h).toBeVisible();
    const top = (await h.locator('xpath=..').boundingBox())!.y;
    for (const loc of [page.getByRole('link', { name: 'Cancel', exact: true }), page.getByRole('button', { name: 'Save slot' })]) {
      await expect(loc).toBeVisible();
      expect(Math.abs((await loc.boundingBox())!.y - top)).toBeLessThanOrEqual(40);
    }
    await expect(page.getByRole('link', { name: 'Back to slots' })).toHaveAttribute('href', '#/');

    const heads = await page.locator('form h3').allTextContents();
    const norm = heads.map((t) => t.trim());
    const idx = ['General', 'Route', 'Kill switch'].map((n) => norm.indexOf(n));
    expect(idx.every((i) => i >= 0)).toBe(true);
    expect(idx[0]).toBeLessThan(idx[1]);
    expect(idx[1]).toBeLessThan(idx[2]);

    const pick = page.getByLabel('Add a hop', { exact: true });
    await expect(pick.locator('option').first()).toHaveText('Choose an endpoint…');
    const add = page.getByRole('button', { name: 'Add hop', exact: true });
    await expect(add).toBeDisabled();
    const hops = page.getByRole('list', { name: 'Route hops' }).locator('li:not(.drop-end)');
    const before = await hops.count();
    await pick.selectOption({ index: 1 });
    await expect(add).toBeEnabled();
    await add.click();
    await expect(hops).toHaveCount(before + 1);
    await expect(page.getByRole('list', { name: 'Route hops' }).locator('xpath=following-sibling::*[1]')).toContainText('Internet');

    const card = page.locator('label.opt', { has: page.locator('input[type=radio]:checked') });
    await expect(card).toHaveCount(1);
    expect(await card.evaluate((el) => getComputedStyle(el).borderTopWidth)).toBe('2px');
    await page.reload();
  });

  test('editor fits at 360', async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(base() + '/#/slots/4');
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Edit slot 4', exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  });
});

