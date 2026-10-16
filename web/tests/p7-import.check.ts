// Import exit check: a real drag-and-drop of a mixed Mullvad zip
// (dragenter/dragover/drop with a DataTransfer, not setInputFiles) shows per-file results.
// Run with: npm run p7-exit (own config, so `npm run smoke` stays at 1 test). Handles fresh or set-up state.
import { expect, test, type Page } from '@playwright/test';
import { makeZip, mullvadConf } from './fixtures/mkzip';

const PASSWORD = 'smoke-test-password-1';
const ZIP_NAME = 'mullvad_wireguard_linux_p7.zip';
const GOOD = ['p7-nl-ams-wg-101.conf', 'p7-nl-ams-wg-102.conf', 'p7-ch-zrh-wg-103.conf'];
const BAD = 'p7-broken-no-peer.conf';

function mixedZip(): Buffer {
  const goods = GOOD.map((name, i) => ({ name, data: mullvadConf(`203.0.113.${71 + i}`) }));
  // Missing [Peer] section: the server must reject this one file and keep the others.
  const bad = { name: BAD, data: mullvadConf('203.0.113.99').split('[Peer]')[0]! };
  return makeZip([...goods, bad]);
}

async function dropZip(page: Page, zip: Buffer) {
  const zone = page.locator('section[aria-labelledby="dropzone-title"]');
  await expect(zone).toBeVisible();
  const dt = await page.evaluateHandle(
    ({ name, b64 }) => {
      const bin = atob(b64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const d = new DataTransfer();
      d.items.add(new File([bytes], name, { type: 'application/zip' }));
      return d;
    },
    { name: ZIP_NAME, b64: zip.toString('base64') },
  );
  for (const type of ['dragenter', 'dragover', 'drop']) await zone.dispatchEvent(type, { dataTransfer: dt });
}

async function rowsByStatus(page: Page) {
  const rows = page.getByTestId('import-results').locator('tbody tr');
  return rows.evaluateAll((trs) =>
    trs.map((tr) => ({
      file: tr.querySelector('td.file')?.textContent?.trim() ?? '',
      status: ['imported', 'skipped-duplicate', 'error'].find((s) => tr.querySelector(`.status.${s}`)) ?? '?',
      reason: tr.querySelector('.reason')?.textContent?.trim() ?? '',
    })),
  );
}

async function expectRows(page: Page, ok: 'imported' | 'skipped-duplicate') {
  await expect(page.getByTestId('import-results').locator('tbody tr')).toHaveCount(4);
  const rows = await rowsByStatus(page);
  console.log(`IMPORT ROWS (${ok}):`, JSON.stringify(rows));
  for (const name of GOOD) {
    const r = rows.find((x) => x.file.endsWith(name));
    expect(r, `row for ${name}`).toBeDefined();
    expect(r!.status, name).toBe(ok);
  }
  const bad = rows.find((x) => x.file.endsWith(BAD));
  expect(bad, `row for ${BAD}`).toBeDefined();
  expect(bad!.status).toBe('error');
  expect(bad!.reason.length).toBeGreaterThan(0);
  expect(rows.filter((r) => r.status === ok)).toHaveLength(3);
  expect(rows.filter((r) => r.status === 'error')).toHaveLength(1);
}

test('drag-and-drop of a mixed Mullvad zip shows per-file results', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  await page.goto(base + '/#/login');
  const needSetup = await page.evaluate(async () => (await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"password":"x"}' })).status === 409);
  if (needSetup) {
    await page.goto(base + '/#/setup');
    await page.locator('input[autocomplete="new-password"]').first().fill(PASSWORD);
    await page.locator('input[autocomplete="new-password"]').nth(1).fill(PASSWORD);
    await page.keyboard.press('Enter');
  } else {
    await page.locator('#login-password').fill(PASSWORD);
    await page.keyboard.press('Enter');
  }
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();

  await page.goto(base + '/#/endpoints');
  await expect(page.getByRole('heading', { name: 'Endpoints', exact: true }).first()).toBeVisible();
  const wgItems = page.locator('[data-testid="wg-list"] > li');
  await expect(page.getByTestId('wg-list')).toBeVisible(); // --dev-testrig seeds WG endpoints
  const before = await wgItems.count();

  const zip = mixedZip();
  await dropZip(page, zip);
  await expectRows(page, 'imported');
  await expect(wgItems).toHaveCount(before + 3);
  console.log(`WG LIST: ${before} -> ${before + 3}`);

  await dropZip(page, zip); // same bytes again → duplicates skipped, bad file still an error
  await expect(page.getByTestId('import-results').locator('.status.skipped-duplicate')).toHaveCount(3);
  await expectRows(page, 'skipped-duplicate');
  await expect(wgItems).toHaveCount(before + 3);
});
