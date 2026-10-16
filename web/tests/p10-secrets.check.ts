// Secrets exit check: export without secrets, import it, Start is refused
// (409 secrets missing), re-enter the WireGuard private key in the Endpoints editor, Start works.
// The key is the rig peer's real client key, read from an encrypted export of the same
// instance (decrypted here); it is a freshly generated testrig key, never a real one.
// Run with: npm run p10-secrets-exit (own config, one --dev-testrig instance).
import { expect, test, type Page } from '@playwright/test';
import { argon2Sync, createDecipheriv } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PASSWORD = 'smoke-test-password-1';
const PASSPHRASE = 'p10-export-passphrase-12';

async function login(page: Page, base: string) {
  await page.goto(base + '/#/login');
  const needSetup = await page.evaluate(async () => (await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"password":"x"}' })).status === 409);
  if (needSetup) {
    await page.goto(base + '/#/setup');
    await page.getByLabel('New admin password').fill(PASSWORD);
    await page.getByLabel('Confirm password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Set password and log in' }).click();
  } else {
    await page.locator('#login-password').fill(PASSWORD);
    await page.keyboard.press('Enter');
  }
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
}

async function exportConfig(page: Page, base: string, mode: 'encrypted' | 'no-secrets', file: string) {
  await page.goto(base + '/#/settings');
  await page.getByTestId('export-config').click();
  if (mode === 'no-secrets') {
    await page.getByLabel('Without secrets').check();
  } else {
    await page.getByTestId('export-passphrase').fill(PASSPHRASE);
    await page.getByLabel('Confirm passphrase').fill(PASSPHRASE);
  }
  await page.locator('#exp-pw').fill(PASSWORD);
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByTestId('export-submit').click()]);
  await download.saveAs(file);
}

// Export format: argon2id t=3 m=16384 p=2, AES-256-GCM, AAD from the outer fields
function decrypt(file: string): { endpoints: { id: string; private_key?: string; preshared_key?: string }[] } {
  const o = JSON.parse(readFileSync(file, 'utf8'));
  const aad = ['sockswebui-config', o.format_version, o.mode, o.kdf.alg, o.kdf.t, o.kdf.m_kib, o.kdf.p, o.kdf.salt, o.cipher.alg, o.cipher.nonce].join('\x00');
  const key = argon2Sync('argon2id', {
    message: Buffer.from(PASSPHRASE), nonce: Buffer.from(o.kdf.salt, 'base64'), memory: o.kdf.m_kib, passes: o.kdf.t, parallelism: o.kdf.p, tagLength: 32,
  });
  const ct = Buffer.from(o.payload, 'base64');
  const d = createDecipheriv('aes-256-gcm', key, Buffer.from(o.cipher.nonce, 'base64'));
  d.setAAD(Buffer.from(aad));
  d.setAuthTag(ct.subarray(ct.length - 16));
  return JSON.parse(Buffer.concat([d.update(ct.subarray(0, ct.length - 16)), d.final()]).toString('utf8'));
}

test('re-enter a WireGuard private key after a without-secrets import; Start then works', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  const tmp = mkdtempSync(join(tmpdir(), 'p10-'));
  try {
    await login(page, base);
    const rigId = await page.evaluate(async () => (await (await fetch('/api/endpoints')).json()).endpoints.find((e: { name: string }) => e.name === 'rigWG').id as string);

    // the rig's real client key, from an encrypted export of this instance
    const encFile = join(tmp, 'enc.json');
    await exportConfig(page, base, 'encrypted', encFile);
    const rig = decrypt(encFile).endpoints.find((e) => e.id === rigId)!;
    const KEY = rig.private_key!;
    expect(KEY.length).toBeGreaterThan(20);

    // (1) export without secrets, import it
    const plain = join(tmp, 'plain.json');
    await exportConfig(page, base, 'no-secrets', plain);
    expect(readFileSync(plain, 'utf8')).not.toContain(KEY);
    await page.goto(base + '/#/settings');
    await page.getByTestId('import-config').click();
    await page.getByTestId('import-file').setInputFiles(plain);
    await page.getByTestId('import-password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Review' }).click();
    await page.getByTestId('import-confirm').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg.getByText('Secrets to re-enter')).toBeVisible({ timeout: 30_000 });
    const href = await dlg.locator('a', { hasText: `Endpoint ${rigId}` }).getAttribute('href');
    expect(href).toBe(`#/endpoints/${rigId}`);
    await dlg.getByRole('button', { name: 'Close' }).click();

    // (2) Start through the UI is refused with 409 "secrets missing"
    await page.goto(base + '/#/');
    const card = page.locator('tr[data-slot="1"]');
    await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'STOPPED');
    const [refused] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/slots/1/start') && r.request().method() === 'POST'),
      card.getByRole('button', { name: /^Start/ }).click(),
    ]);
    expect(refused.status()).toBe(409);
    expect(await refused.text()).toContain('secrets missing');
    await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'STOPPED');

    // (3) follow the list link: the WireGuard editor opens; the row says the key is missing
    await page.goto(base + '/' + href);
    const form = page.getByTestId('wg-secrets-form');
    await expect(form).toBeVisible();
    await expect(page.getByTestId('wg-list')).toContainText('private key missing');
    const priv = form.getByLabel('Private key');
    await expect(priv).toHaveAttribute('type', 'password');
    await expect(priv).toHaveAttribute('autocomplete', 'off');
    await expect(priv).toHaveValue('');
    await priv.fill(KEY);
    await form.getByRole('button', { name: 'Save keys' }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Keys saved' })).toBeVisible();
    await expect(form).toBeHidden();
    await expect(page.getByTestId('wg-list')).not.toContainText(`${rigId} private key missing`);

    // (4) the key text is nowhere: DOM, inputs, storage, URL, API responses
    const seen = await page.evaluate(async () => ({
      html: document.documentElement.outerHTML,
      inputs: [...document.querySelectorAll('input')].map((i) => i.value),
      store: JSON.stringify([{ ...localStorage }, { ...sessionStorage }, location.href]),
      api: await (await fetch('/api/endpoints')).text(),
    }));
    for (const v of [seen.html, seen.store, seen.api, ...seen.inputs]) expect(v).not.toContain(KEY);
    expect(await page.content()).not.toContain(KEY);
    expect(JSON.parse(seen.api).endpoints.find((e: { id: string }) => e.id === rigId).private_key_set).toBe(true);

    // (5) the same Start now works and the slot reaches UP against the rig
    await page.goto(base + '/#/');
    const [started] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/slots/1/start') && r.request().method() === 'POST'),
      card.getByRole('button', { name: /^Start/ }).click(),
    ]);
    expect(started.status()).not.toBe(409);
    await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
});
