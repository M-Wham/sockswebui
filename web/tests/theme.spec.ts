import { expect, test } from '@playwright/test';
import { loginOrSetup } from './fixtures/uiState';

test.use({ colorScheme: 'light' });

const base = () => process.env.SMOKE_BASE_URL!;
const bg = () => document.documentElement.ownerDocument.defaultView!.getComputedStyle(document.documentElement).getPropertyValue('--bg').trim();

test('theme: picker, persistence and boot fallback', async ({ page }) => {
  await loginOrSetup(page, base());
  const html = page.locator('html');
  await expect(html).toHaveAttribute('data-theme', 'forest');
  expect(await page.evaluate(bg)).toBe('#082019');

  await page.goto(base() + '/#/settings');
  const picker = page.getByLabel('Theme', { exact: true });
  await picker.selectOption('tide');
  await expect(html).toHaveAttribute('data-theme', 'tide');
  await page.reload();
  await expect(html).toHaveAttribute('data-theme', 'tide');
  await expect(page.getByLabel('Theme', { exact: true })).toHaveValue('tide');

  await page.getByLabel('Theme', { exact: true }).selectOption('forest-light');
  await expect(html).toHaveAttribute('data-theme', 'forest-light');
  const hex = await page.evaluate(bg);
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const lin = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  expect(0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)).toBeGreaterThan(0.5);
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe('light');
});

test('theme: an unknown stored value boots as forest', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('sockswebui.theme', 'neon'));
  await page.goto(base() + '/#/login');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'forest');
});

test('fonts: self-hosted families load from the same origin', async ({ page }) => {
  const urls: string[] = [];
  const fontResponses: { url: string; status: number }[] = [];
  page.on('request', (r) => urls.push(r.url()));
  page.on('response', (r) => {
    if (/\/fonts\/.*\.woff2/.test(r.url())) fontResponses.push({ url: r.url(), status: r.status() });
  });
  await loginOrSetup(page, base());
  await page.getByRole('heading', { name: 'Slots', exact: true }).waitFor();
  const families = await page.evaluate(async () => {
    await document.fonts.ready;
    return [...document.fonts].filter((f) => f.status === 'loaded').map((f) => f.family.replace(/["']/g, ''));
  });
  expect([...new Set(families)]).toEqual(['IBM Plex Mono']);
  for (const sel of ['body', 'h2', 'button']) {
    const ff = await page.locator(sel).first().evaluate((el) => getComputedStyle(el).fontFamily);
    expect(ff, sel).toMatch(/^["']?IBM Plex Mono/);
  }
  expect(fontResponses.length).toBeGreaterThan(0);
  for (const r of fontResponses) expect(r.status, r.url).toBe(200);
  const origin = new URL(base()).origin;
  for (const u of urls) {
    if (u.startsWith('data:') || u.startsWith('blob:')) continue;
    expect(new URL(u).origin, u).toBe(origin);
  }
});
