// Secondary screens and visual polish at 1440 and 360. Runs alone: one login per file.
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

test.describe('secondary screens', () => {
  test('Settings panels use the wide column and h3 is uppercase at 1440', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/settings');
    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
    const panels = page.locator('main .panel');
    await expect(panels.first()).toBeVisible();
    const n = await panels.count();
    expect(n).toBeGreaterThanOrEqual(4);
    for (let i = 0; i < n; i++) {
      const w = (await panels.nth(i).boundingBox())!.width;
      expect(w, `panel ${i}`).toBeGreaterThan(512);
    }
    const h3 = panels.first().locator('h3').first();
    expect(await h3.evaluate((el) => getComputedStyle(el).textTransform)).toBe('uppercase');
  });

  for (const route of ['endpoints', 'logs', 'settings']) {
    test(`${route} has exactly one h2`, async () => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(base() + `/#/${route}`);
      await expect(page.locator('main h2').first()).toBeVisible();
      await expect(page.locator('main h2')).toHaveCount(1);
    });
  }

  test('Endpoints sections are panels with a small heading', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/endpoints');
    await expect(page.locator('main .panel')).toHaveCount(3);
    for (const name of ['Import WireGuard configs', 'WireGuard endpoints', 'Upstream proxies']) {
      await expect(page.locator('main .panel h3', { hasText: name })).toBeVisible();
    }
  });

  for (const route of ['login', 'setup']) {
    test(`${route} card fits at 360`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 360, height: 740 } });
      const p = await ctx.newPage();
      try {
        await p.goto(base() + `/#/${route}`);
        const card = p.locator('.form-card');
        await expect(card).toBeVisible();
        expect(await p.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
        const b = (await card.boundingBox())!;
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(360);
        await expect(p.locator('header h1')).toBeVisible();
      } finally {
        await ctx.close();
      }
    });
  }
});

// Visual polish. Nothing may clip or overflow at 360, native controls stay normal size.
const ROUTES = ['#/', '#/endpoints', '#/slots/4', '#/settings', '#/logs'];

test.describe('polish', () => {
  for (const route of ROUTES) {
    test(`no clipped content at 360 on ${route}`, async () => {
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto(base() + '/' + route);
      await page.reload();
      await expect(page.locator('main h2').first()).toBeVisible();
      await page.waitForTimeout(400);
      const bad = await page.evaluate(() => {
        const out: string[] = [];
        const name = (el: Element) => `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.getAttribute('class') ?? '').split(' ')[0]}`;
        for (const el of Array.from(document.querySelectorAll('main *'))) {
          if (el.closest('.visually-hidden, thead, svg')) continue;
          const r = el.getBoundingClientRect();
          if (r.width === 0 && r.height === 0) continue;
          if (r.right > 360.5) out.push(`${name(el)} right=${Math.round(r.right)}`);
        }
        for (const el of Array.from(document.querySelectorAll('main .panel, main table, main .wrap, main form, main section, main li'))) {
          if (el.scrollWidth > el.clientWidth + 1) out.push(`${name(el)} scrollWidth=${el.scrollWidth} clientWidth=${el.clientWidth}`);
        }
        return out;
      });
      expect(bad).toEqual([]);
    });
  }

  for (const [w, h] of [
    [1440, 900],
    [360, 740],
  ]) {
    for (const route of ROUTES) {
      test(`checkboxes and radios are normal size and inline at ${w} on ${route}`, async () => {
        await page.setViewportSize({ width: w, height: h });
        await page.goto(base() + '/' + route);
        await page.reload();
        await expect(page.locator('main h2').first()).toBeVisible();
        const bad = await page.evaluate(() => {
          const out: string[] = [];
          for (const el of Array.from(document.querySelectorAll<HTMLInputElement>('input[type=checkbox], input[type=radio]'))) {
            const r = el.getBoundingClientRect();
            if (r.width === 0 && r.height === 0) continue;
            const label = el.closest('label') ?? (el.id ? document.querySelector(`label[for="${el.id}"]`) : null);
            const lt = label ? label.getBoundingClientRect().top : NaN;
            if (r.width > 24 || r.height > 24) out.push(`${el.id || el.name} ${Math.round(r.width)}x${Math.round(r.height)}`);
            if (!label || Math.abs(lt - r.top) >= 16) out.push(`${el.id || el.name} label offset ${Math.round(lt - r.top)}`);
          }
          return out;
        });
        expect(bad).toEqual([]);
      });
    }
  }

  test('1440 widths: settings panel, stats and chart share the row', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/settings');
    await page.reload();
    const panel = page.locator('main .panel').first();
    await expect(panel).toBeVisible();
    expect((await panel.boundingBox())!.width).toBeGreaterThanOrEqual(900);
    await page.goto(base() + '/#/');
    await page.reload();
    const mb = (await page.locator('main').boundingBox())!;
    const cb = (await page.getByTestId('throughput-chart').boundingBox())!;
    const sb = (await page.getByTestId('stats').boundingBox())!;
    expect(cb.x + cb.width).toBeGreaterThanOrEqual(mb.x + mb.width - 32 - 1);
    expect(sb.width / cb.width).toBeGreaterThan(0.6);
    expect(sb.width / cb.width).toBeLessThan(0.75);
  });

  test('1440 density: rows near 40px', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    await page.reload();
    const unconf = page.locator('tr[data-slot]').filter({ hasText: 'Not configured' }).first();
    await expect(unconf).toBeVisible();
    expect((await unconf.boundingBox())!.height).toBeLessThanOrEqual(48);
    for (const r of await page.getByTestId('endpoints-summary').locator('tbody tr').all()) {
      expect((await r.boundingBox())!.height).toBeLessThanOrEqual(48);
    }
  });

  test('configured slot row is one border and has no repeated error text', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/');
    await page.reload();
    const row = page.locator('tr[data-slot="4"]');
    await expect(row).toBeVisible();
    expect(await row.locator('.msg').count()).toBeLessThanOrEqual(1);
    const cells = await row.locator('> *').evaluateAll((els) => els.map((e) => ({ d: getComputedStyle(e).display })));
    for (const c of cells) expect(c.d).toBe('table-cell');
    const ys = await row.locator('> *').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().bottom)));
    expect(new Set(ys).size).toBe(1);
    await expect(row.locator('[data-killswitch="allow-direct"]')).toBeVisible();
  });

  test('360 top bar: nav tabs share one row', async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(base() + '/#/');
    const nav = page.getByRole('navigation', { name: 'Main' });
    const tops = new Set<number>();
    for (const l of await nav.getByRole('link').all()) {
      await expect(l).toBeVisible();
      const b = (await l.boundingBox())!;
      expect(b.x + b.width).toBeLessThanOrEqual(360);
      tops.add(Math.round(b.y));
    }
    expect(tops.size).toBe(1);
  });

  test('360 unconfigured slot is one short line', async () => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(base() + '/#/');
    await page.reload();
    const unconf = page.locator('tr[data-slot]').filter({ hasText: 'Not configured' }).first();
    await expect(unconf).toBeVisible();
    expect((await unconf.boundingBox())!.height).toBeLessThan(80);
  });

  for (const w of [1440, 360]) {
    test(`editor back link is a labelled 32px target at ${w}`, async () => {
      await page.setViewportSize({ width: w, height: 900 });
      await page.goto(base() + '/#/slots/4');
      await page.reload();
      const back = page.locator('a.back');
      await expect(back).toBeVisible();
      await expect(back).toContainText('Back');
      const b = (await back.boundingBox())!;
      expect(b.height).toBeGreaterThanOrEqual(32);
      expect(b.width).toBeGreaterThanOrEqual(32);
    });

    test(`editor Port is aligned with Name and Bind address at ${w}`, async () => {
      await page.setViewportSize({ width: w, height: 900 });
      await page.goto(base() + '/#/slots/4');
      await page.reload();
      const bind = (await page.getByLabel('Bind address').boundingBox())!;
      const port = (await page.getByLabel('Port', { exact: true }).boundingBox())!;
      if (w >= 900) expect(Math.abs(port.y - bind.y)).toBeLessThanOrEqual(1);
      expect(Math.abs(port.height - bind.height)).toBeLessThanOrEqual(1);
      const name = (await page.getByLabel('Name', { exact: true }).boundingBox())!;
      expect(Math.abs(name.height - port.height)).toBeLessThanOrEqual(1);
    });
  }

  test('settings checkbox label is plain body text inline with its box', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base() + '/#/settings');
    await page.reload();
    const st = await page.locator('label[for=start-on-enable]').evaluate((el) => {
      const c = getComputedStyle(el);
      return { tt: c.textTransform, fs: parseFloat(c.fontSize), ls: c.letterSpacing };
    });
    expect(st.tt).toBe('none');
    expect(st.fs).toBeGreaterThanOrEqual(13);
    expect(st.ls).toBe('normal');
  });
});

