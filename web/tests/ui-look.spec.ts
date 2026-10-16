// Look checks for the forest theme. Each task appends its own tests.
import { expect, test } from '@playwright/test';
import { mullvadZip } from './fixtures/mkzip';
import { ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

const base = () => process.env.SMOKE_BASE_URL!;

test.describe('header at 360 px', () => {
  test.use({ viewport: { width: 360, height: 740 } });

  test('nav fits, targets are tall, main is capped', async ({ page }) => {
    await loginOrSetup(page, base());
    const nav = page.getByRole('navigation', { name: 'Main' });
    const targets = [...(await nav.getByRole('link').all()), page.getByRole('button', { name: 'Log out' })];
    expect(targets.length).toBe(5);
    for (const t of targets) {
      await expect(t).toBeVisible();
      const box = (await t.boundingBox())!;
      expect(box.x + box.width).toBeLessThanOrEqual(360);
      expect(box.height).toBeGreaterThanOrEqual(44);
    }
    const maxWidth = await page.locator('main').evaluate((el) => getComputedStyle(el).maxWidth);
    expect(maxWidth).not.toBe('none');
  });
});

async function dashboardWithSlot4(page: import('@playwright/test').Page) {
  await loginOrSetup(page, base());
  await ensureInventorySlot(page, base());
  await page.reload();
  await expect(page.locator('tr[data-slot="4"]')).toBeVisible();
}

test.describe('state badges as tags', () => {
  test.use({ viewport: { width: 360, height: 740 } });

  test('allow-direct is loud, neutral is dashed outline', async ({ page }) => {
    await dashboardWithSlot4(page);
    const card = page.locator('tr[data-slot="4"]');
    const loud = card.locator('[data-killswitch="allow-direct"]');
    await expect(loud).toBeVisible();
    const dangerBg = await page.evaluate(() => {
      const p = document.createElement('div');
      p.style.background = 'var(--danger-bg)';
      document.body.appendChild(p);
      const c = getComputedStyle(p).backgroundColor;
      p.remove();
      return c;
    });
    const s = await loud.evaluate((el) => {
      const c = getComputedStyle(el);
      return { bg: c.backgroundColor, bw: parseFloat(c.borderTopWidth) };
    });
    expect(s.bg).toBe(dangerBg);
    expect(s.bw).toBeGreaterThanOrEqual(2);
    await expect(
      card.getByText('When degraded, traffic leaves through this device’s own connection instead of the tunnel.'),
    ).toBeVisible();

    const neutral = card.locator('[data-state="STOPPED"]');
    await expect(neutral).toBeVisible();
    const n = await neutral.evaluate((el) => {
      const c = getComputedStyle(el);
      return { bg: c.backgroundColor, bs: c.borderTopStyle };
    });
    expect(n.bg).toBe('rgba(0, 0, 0, 0)');
    expect(n.bs).toBe('dashed');
  });

  test('an ok badge, when present, is a solid outline', async ({ page }) => {
    await dashboardWithSlot4(page);
    const ok = page.locator('.badge.ok').first();
    if ((await ok.count()) === 0) return;
    const s = await ok.evaluate((el) => {
      const c = getComputedStyle(el);
      return { bg: c.backgroundColor, bs: c.borderTopStyle };
    });
    expect(s.bs).toBe('solid');
    expect(s.bg).toBe('rgba(0, 0, 0, 0)');
  });
});

test.describe('forms, dialogs and tables at 360 px', () => {
  test.use({ viewport: { width: 360, height: 740 } });

  test('export dialog fits and has a themed backdrop', async ({ page }) => {
    await loginOrSetup(page, base());
    await page.goto(base() + '/#/settings');
    await page.getByTestId('export-config').click();
    const dlg = page.locator('dialog[open]');
    await expect(dlg).toBeVisible();
    const box = (await dlg.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(360);
    const alpha = await dlg.evaluate((el) => {
      const m = getComputedStyle(el, '::backdrop').backgroundColor.match(/[\d.]+/g)!.map(Number);
      return m.length > 3 ? m[3]! : 1;
    });
    expect(alpha).toBeGreaterThan(0.3);
  });

  test('import results scroll inside their wrapper', async ({ page }) => {
    await loginOrSetup(page, base());
    await page.goto(base() + '/#/endpoints');
    await page.getByTestId('import-input').setInputFiles({
      name: 'mullvad_wireguard_linux_se.zip',
      mimeType: 'application/zip',
      buffer: mullvadZip(),
    });
    const table = page.getByTestId('import-results');
    await expect(table).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
    const wrapOverflow = await table.evaluate((el) => getComputedStyle(el.parentElement!).overflowX);
    expect(wrapOverflow).toBe('auto');
    const fileFont = await table.locator('td.file').first().evaluate((el) => getComputedStyle(el).fontFamily);
    expect(fileFont).toContain('IBM Plex Mono');
  });

  test('log pane is mono', async ({ page }) => {
    await loginOrSetup(page, base());
    await page.goto(base() + '/#/logs');
    const log = page.locator('pre.log');
    await expect(log).toBeVisible();
    expect(await log.evaluate((el) => getComputedStyle(el).fontFamily)).toContain('IBM Plex Mono');
  });
});

test.describe('route builder drop target', () => {
  test('drop-end keeps its own look over the hop item rules', async ({ page }) => {
    await loginOrSetup(page, base());
    await ensureInventorySlot(page, base());
    await page.goto(base() + '/#/slots/4');
    const end = page.locator('li.drop-end');
    await expect(end).toBeVisible();
    const s = await end.evaluate((el) => {
      const c = getComputedStyle(el);
      return { jc: c.justifyContent, bs: c.borderTopStyle, bg: c.backgroundColor, cursor: c.cursor };
    });
    expect(s).toEqual({ jc: 'center', bs: 'dashed', bg: 'rgba(0, 0, 0, 0)', cursor: 'default' });
  });
});
