// Rotate exit check: template proxy -> route -> start -> New IP -> confirm -> new ID, slot UP.
// Run with: npm run p8-exit (own config). Runs against the --dev-testrig binary.
import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'smoke-test-password-1';
const NAME = 'rot-proxy';

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

test('New IP rotates the session ID and the slot comes back UP', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  await login(page, base);

  // rig upstream address, as seeded by --dev-testrig
  const rig = await page.evaluate(async () => {
    const r = await (await fetch('/api/endpoints')).json();
    const e = r.endpoints.find((x: { name: string }) => x.name === 'rigSOCKS');
    return { host: e.host as string, port: e.port as number };
  });

  // (1) create the template proxy through the UI
  await page.goto(base + '/#/endpoints');
  await page.getByRole('button', { name: 'Add proxy' }).click();
  await page.getByLabel('Name', { exact: true }).fill(NAME);
  await page.getByLabel('Host', { exact: true }).fill(rig.host);
  await page.getByLabel('Port', { exact: true }).fill(String(rig.port));
  await page.getByLabel('Username (optional)').fill('rig-{session}');
  await page.getByRole('checkbox', { name: 'Rotating session' }).check();
  await page.getByRole('button', { name: 'Add proxy', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Proxy added.' })).toBeVisible();

  // (2) route rigWG2 -> proxy on slot 2, then start
  await page.goto(base + '/#/slots/2');
  await expect(page.getByRole('heading', { name: 'Slot 2' })).toBeVisible();
  await page.getByLabel('Name', { exact: true }).fill('rotate');
  await page.getByLabel('Bind address').fill('127.0.0.1');
  await page.getByLabel('Port', { exact: true }).fill('1082');
  await page.getByLabel('Interval (s)').fill('2');
  await page.getByLabel('Fail threshold').fill('2');
  await page.getByLabel('Probe URL').fill('http://echo.rig.invalid/');
  await page.getByRole('button', { name: 'Add rigWG2 to route', exact: true }).click();
  await page.getByRole('button', { name: `Add ${NAME} to route`, exact: true }).click();
  await expect(page.getByRole('list', { name: 'Route hops' })).toContainText(NAME);
  await page.getByRole('button', { name: 'Save slot' }).click();
  await expect(page.getByRole('status').filter({ hasText: /saved/i })).toBeVisible();

  await page.goto(base + '/#/');
  const card = page.locator('tr[data-slot="2"]');
  await card.getByRole('button', { name: /^Start/ }).click();
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });

  const idOf = async () => {
    await page.goto(base + '/#/endpoints');
    const row = page.locator('ul.list li').filter({ hasText: NAME });
    const m = /session ID (\S+)/.exec((await row.textContent()) ?? '');
    expect(m, 'session ID shown').not.toBeNull();
    return m![1]!;
  };
  const before = await idOf();

  // (3) New IP from the slot card, confirm in the dialog
  await page.goto(base + '/#/');
  await card.getByRole('button', { name: `New IP for ${NAME}` }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText(`Get a new IP for ${NAME}? Slots rotate will reconnect and open connections will drop. Some providers bill per IP.`);
  await dialog.getByRole('button', { name: 'Confirm new IP' }).click();
  await expect(card.getByRole('status')).toContainText('Current session ID:');

  // (4) ID changed; slot back to UP
  const after = await idOf();
  expect(after).not.toBe(before);
  await page.goto(base + '/#/');
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'UP', { timeout: 30_000 });
});
