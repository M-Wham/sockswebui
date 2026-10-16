// Round-trip exit check: export an encrypted config from instance A,
// import it on instance B (different admin password), Start all, B's own password is unchanged.
// Run with: npm run p9-exit (own config, two --dev-testrig instances).
import { expect, test, type Page } from '@playwright/test';
import { mkdtempSync, rmSync, statSync } from 'node:fs';
import { connect } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PW_A = 'instance-a-password-1';
const PW_B = 'instance-b-password-2';
const PASSPHRASE = 'round-trip-passphrase-12';
const NEW_PROXY = 'p9-proxy';

// Fresh instance: set the password. Reused instance (--repeat-each): log in with it.
async function setup(page: Page, base: string, password: string, reused: boolean) {
  await page.goto(base + '/#/login');
  if (!reused) {  // login attempts are rate limited, so do not probe
    await page.goto(base + '/#/setup');
    await page.getByLabel('New admin password').fill(password);
    await page.getByLabel('Confirm password').fill(password);
    await page.getByRole('button', { name: 'Set password and log in' }).click();
  } else {
    await page.locator('#login-password').fill(password);
    await page.keyboard.press('Enter');
  }
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
}

async function endpointNames(page: Page, base: string): Promise<string[]> {
  await page.goto(base + '/#/endpoints');
  await expect(page.getByRole('heading', { name: 'Endpoints', exact: true }).first()).toBeVisible();
  await expect(page.locator('ul.list li').first()).toBeVisible();
  const names = await page.locator('ul.list li strong').allTextContents();
  return names.map((n) => n.trim()).sort();
}

async function slotsShown(page: Page, base: string): Promise<{ id: string; name: string; port: string }[]> {
  await page.goto(base + '/#/');
  await expect(page.locator('tr[data-slot]').first()).toBeVisible();
  await expect(page.locator('tr[data-slot] [data-state]').first()).toBeVisible();
  // Unconfigured placeholder cards (no port) are not slots; only configured ones are compared.
  const all = await page.locator('tr[data-slot]').evaluateAll((cards) =>
    cards.map((c) => ({
      id: c.getAttribute('data-slot') ?? '',
      name: c.querySelector('.slot-name')?.textContent?.trim() ?? '',
      port: /:(\d{2,5})\b/.exec(c.textContent ?? '')?.[1] ?? '',
    })),
  );
  return all.filter((s) => s.port !== '');
}

// No-persistence: canaries are in no storage, not in the URL, and every password
// input in the page (dialogs included, open or closed) is empty.
async function expectNoPersistence(page: Page, canaries: string[]) {
  const snap = () =>
    page.evaluate(() => ({
      dump: JSON.stringify([{ ...localStorage }, { ...sessionStorage }, location.href, location.hash]),
      inputs: [...document.querySelectorAll('input[type=password]')].map((i) => (i as HTMLInputElement).value),
    }));
  // Svelte clears the bound inputs on the next tick after close/submit: poll until they are empty.
  await expect.poll(async () => (await snap()).inputs.filter((v) => v !== ''), { timeout: 5_000 }).toEqual([]);
  const seen = await snap();
  for (const c of canaries) expect(seen.dump).not.toContain(c);
}

function listening(port: number): Promise<boolean> {
  return new Promise((res) => {
    const s = connect({ host: '127.0.0.1', port });
    s.once('connect', () => { s.destroy(); res(true); });
    s.once('error', () => res(false));
  });
}

test('encrypted export on A imports on B; Start all brings every slot UP; B password unchanged', async ({ browser }, testInfo) => {
  const reused = testInfo.repeatEachIndex > 0;
  const baseA = process.env.P9_BASE_A!;
  const baseB = process.env.P9_BASE_B!;
  expect(baseA).not.toBe(baseB);
  const tmp = mkdtempSync(join(tmpdir(), 'p9-dl-'));
  const ctxA = await browser.newContext({ acceptDownloads: true });
  const ctxB = await browser.newContext();
  try {
    // (1) instance A: set up, add a proxy endpoint through the UI, record what is shown
    const a = await ctxA.newPage();
    await setup(a, baseA, PW_A, reused);
    const rig = await a.evaluate(async () => {
      const r = await (await fetch('/api/endpoints')).json();
      const e = r.endpoints.find((x: { name: string }) => x.name === 'rigSOCKS');
      return { host: e.host as string, port: e.port as number };
    });
    await a.goto(baseA + '/#/endpoints');
    await expect(a.getByRole('heading', { name: 'Endpoints', exact: true }).first()).toBeVisible();
    const present = await a.evaluate(async (n) => (await (await fetch('/api/endpoints')).json()).endpoints.some((e: { name: string }) => e.name === n), NEW_PROXY);
    if (!present) {
    await a.getByRole('button', { name: 'Add proxy' }).click();
    await a.getByLabel('Name', { exact: true }).fill(NEW_PROXY);
    await a.getByLabel('Host', { exact: true }).fill(rig.host);
    await a.getByLabel('Port', { exact: true }).fill(String(rig.port));
    await a.getByRole('button', { name: 'Add proxy', exact: true }).click();
    await expect(a.getByRole('status').filter({ hasText: 'Proxy added.' })).toBeVisible();
    }

    const namesA = await endpointNames(a, baseA);
    expect(namesA).toContain(NEW_PROXY);
    expect(namesA).toHaveLength(4); // rigWG, rigWG2, rigSOCKS + the new proxy
    // The dev rig starts its seeded slot: stop it so port 1080 and the rig WG endpoint are free for B.
    await a.goto(baseA + '/#/');
    const cardA = a.locator('tr[data-slot="1"]');
    if ((await cardA.locator('[data-state]').getAttribute('data-state')) !== 'STOPPED') {
      await expect(cardA.locator('[data-state]')).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });
      await cardA.getByRole('button', { name: /^Stop/ }).click();
    }
    await expect(cardA.locator('[data-state]')).toHaveAttribute('data-state', 'STOPPED');
    const slotsA = await slotsShown(a, baseA);
    expect(slotsA).toHaveLength(1);
    expect(slotsA[0]!.port).toMatch(/^\d+$/);
    for (const s of slotsA) await expect(a.locator(`tr[data-slot="${s.id}"] [data-state]`)).toHaveAttribute('data-state', 'STOPPED');

    // (2) export: encrypted, passphrase, A's admin password; capture the download
    await a.goto(baseA + '/#/settings');
    await a.getByTestId('export-config').click();
    await a.getByTestId('export-passphrase').fill(PASSPHRASE);
    await a.getByLabel('Confirm passphrase').fill(PASSPHRASE);
    await a.locator('#exp-pw').fill(PW_A);
    const [download] = await Promise.all([a.waitForEvent('download'), a.getByTestId('export-submit').click()]);
    const file = join(tmp, 'export.json');
    await download.saveAs(file);
    expect(statSync(file).size).toBeGreaterThan(100);
    await expect(a.getByTestId('export-passphrase')).toBeHidden();
    await expectNoPersistence(a, [PASSPHRASE, PW_A]);
    // Escape close of the real export dialog clears the form too.
    await a.getByTestId('export-config').click();
    await a.getByTestId('export-passphrase').fill(PASSPHRASE);
    await a.locator('#exp-pw').fill(PW_A);
    await a.keyboard.press('Escape');
    await expectNoPersistence(a, [PASSPHRASE, PW_A]);
    await a.getByTestId('export-config').click();
    await expect(a.getByTestId('export-passphrase')).toHaveValue('');
    await expect(a.locator('#exp-pw')).toHaveValue('');
    await a.keyboard.press('Escape');

    // (3) instance B: own password; import the file
    // Login, export and import share one limit of 5 per minute per instance. A reused
    // instance (--repeat-each) still has the previous run's attempts in its window: let them expire.
    if (reused) await a.waitForTimeout(61_000);
    const b = await ctxB.newPage();
    await setup(b, baseB, PW_B, reused);
    await b.goto(baseB + '/#/settings');
    // Failure path: a wrong passphrase is rejected (400) and the real dialog wipes its fields.
    const BAD = 'wrong-canary-passphrase-77';
    await b.getByTestId('import-config').click();
    await b.getByTestId('import-file').setInputFiles(file);
    await b.getByTestId('import-passphrase').fill(BAD);
    await b.getByTestId('import-password').fill(PW_B);
    await b.getByRole('button', { name: 'Review' }).click();
    await b.getByTestId('import-confirm').click();
    await expect(b.locator('.form-error')).toBeVisible();
    await expectNoPersistence(b, [BAD, PW_B]);
    await b.keyboard.press('Escape');
    await expectNoPersistence(b, [BAD, PW_B, PASSPHRASE]);
    await b.getByTestId('import-config').click();
    await b.getByTestId('import-file').setInputFiles(file);
    await b.getByTestId('import-passphrase').fill(PASSPHRASE);
    await b.getByTestId('import-password').fill(PW_B);
    await b.getByRole('button', { name: 'Review' }).click();
    await b.getByTestId('import-confirm').click();
    await expect(b.getByRole('status').filter({ hasText: /^\s*Imported 4 endpoints and 1 slot\./ })).toBeVisible({ timeout: 30_000 });

    // (4) B shows the same endpoints and slots, all STOPPED, nothing listening
    const dlg = b.getByRole('dialog');
    await expect(dlg.getByTestId('import-start-all')).toBeVisible();
    expect(await listening(Number(slotsA[0]!.port))).toBe(false);
    await expectNoPersistence(b, [PASSPHRASE, PW_B]);
    await dlg.getByRole('button', { name: 'Close' }).click();
    await expectNoPersistence(b, [PASSPHRASE, PW_B]);
    expect(await endpointNames(b, baseB)).toEqual(namesA);
    const slotsB = await slotsShown(b, baseB);
    expect(slotsB).toEqual(slotsA);
    for (const s of slotsB) await expect(b.locator(`tr[data-slot="${s.id}"] [data-state]`)).toHaveAttribute('data-state', 'STOPPED');
    expect(await listening(Number(slotsB[0]!.port))).toBe(false);

    // (5) Start all (from the import result dialog) brings every slot UP
    await b.goto(baseB + '/#/settings');
    await b.getByTestId('import-config').click();
    await b.getByTestId('import-file').setInputFiles(file);
    await b.getByTestId('import-passphrase').fill(PASSPHRASE);
    await b.getByTestId('import-password').fill(PW_B);
    await b.getByRole('button', { name: 'Review' }).click();
    await b.getByTestId('import-confirm').click();
    await b.getByTestId('import-start-all').click();
    await b.getByRole('dialog').getByRole('button', { name: 'Close' }).click();
    await b.goto(baseB + '/#/');
    for (const s of slotsB) {
      await expect(b.locator(`tr[data-slot="${s.id}"] [data-state]`)).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });
    }

    // The failure-path import above used one of the 5 attempts per minute: let the window expire.
    await b.waitForTimeout(61_000);
    // (6) B's own password still works; A's is rejected
    await b.getByRole('button', { name: 'Log out' }).click();
    await b.goto(baseB + '/#/login');
    await b.locator('#login-password').fill(PW_A);
    await b.keyboard.press('Enter');
    await expect(b.getByRole('heading', { name: 'Slots', exact: true })).toBeHidden();
    await expect(b.getByText('wrong password')).toBeVisible();
    await b.locator('#login-password').fill(PW_B);
    await b.keyboard.press('Enter');
    await expect(b.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
  } finally {
    await ctxA.close();
    await ctxB.close();
    rmSync(tmp, { recursive: true, force: true });
  }
});
