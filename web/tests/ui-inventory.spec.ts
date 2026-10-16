// Feature inventory: every control the UI offers today must stay present and visible, whatever
// the styling. Written against the unstyled UI; later theme tasks must not edit it.
import { expect, test, type Page } from '@playwright/test';
import { mullvadZip } from './fixtures/mkzip';
import { ensureInventorySlot, loginOrSetup } from './fixtures/uiState';

const base = () => process.env.SMOKE_BASE_URL!;

// One login for the whole file (the server rate-limits password attempts); logout is the last test.
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

test('header: brand, Main navigation, Log out, Restart all, footer', async () => {
  await expect(page.locator('h1 a')).toHaveText('sockswebui');
  const nav = page.getByRole('navigation', { name: 'Main' });
  for (const n of ['Slots', 'Endpoints', 'Logs', 'Settings']) {
    await expect(nav.getByRole('link', { name: n, exact: true })).toBeVisible();
  }
  const side = page.locator('aside.sidebar');
  await expect(side.getByRole('button', { name: 'Log out' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Restart all' })).toBeVisible();
  await expect(page.getByTestId('system-status')).toBeVisible();
  await expect(page.getByTestId('app-version')).toBeVisible();
});

test('dashboard: slots table, badges, terms, actions', async () => {
  await page.goto(base() + '/#/');
  await page.reload(); // the dashboard loaded before ensureInventorySlot configured slot 4
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
  await expect(page.getByText(/Live:|Connecting/)).toBeVisible();
  await expect(page.locator('tr[data-slot]')).toHaveCount(4);

  const row = page.locator('tr[data-slot="4"]');
  await expect(row.locator('[data-state]')).toBeVisible();
  const ks = row.locator('[data-killswitch="allow-direct"]');
  await expect(ks).toBeVisible();
  await expect(ks).toHaveText('allow-direct: falls back to device route when degraded');
  await expect(row.getByText('When degraded, traffic leaves through this device’s own connection instead of the tunnel.', { exact: true }).last()).toBeVisible();
  await expect(row.getByText('127.0.0.1:1084')).toBeVisible();
  for (const t of ['Exit', 'Latency', 'Down / Up']) await expect(page.getByRole('columnheader', { name: t, exact: true })).toBeVisible();
  await expect(row.getByRole('link', { name: 'Edit' })).toBeVisible();
  await expect(row.getByRole('button', { name: /^Start/ })).toBeVisible();

  const unconfigured = page.locator('tr[data-slot]').filter({ hasText: 'Not configured' }).first();
  await expect(unconfigured).toBeVisible();
  await expect(unconfigured.getByRole('link', { name: 'Configure' })).toBeVisible();

  await expect(page.getByTestId('stats')).toBeVisible();
  await expect(page.getByTestId('throughput-chart')).toBeVisible();
  await expect(page.getByTestId('endpoints-summary')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Import configs' })).toBeVisible();
});

test('endpoints: import, lists, proxy form, keys form, results', async () => {
  await page.goto(base() + '/#/endpoints');
  await expect(page.getByRole('heading', { name: 'Endpoints', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Import WireGuard configs' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'WireGuard endpoints' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Upstream proxies' })).toBeVisible();
  await expect(page.getByTestId('import-input')).toBeAttached();
  await expect(page.getByLabel('Country')).toBeVisible();

  const list = page.getByTestId('wg-list');
  await expect(list).toBeVisible();
  await expect(list.getByRole('button', { name: /^Edit keys for / }).first()).toBeVisible();
  await expect(list.getByRole('button', { name: /^Delete / }).first()).toBeVisible();

  await page.getByRole('button', { name: 'Add proxy' }).click();
  for (const l of ['Name', 'Type', 'Host', 'Port', 'Username (optional)']) {
    await expect(page.getByLabel(l, { exact: true })).toBeVisible();
  }
  const form = page.locator('form.proxy-form');
  await expect(form.getByRole('button', { name: 'Add proxy' })).toBeVisible();
  await expect(form.getByRole('button', { name: 'Cancel' })).toBeVisible();

  await list.getByRole('button', { name: /^Edit keys for / }).first().click();
  const keys = page.getByTestId('wg-secrets-form');
  await expect(keys).toBeVisible();
  await expect(keys.getByRole('button', { name: 'Save keys' })).toBeVisible();
  await expect(keys.getByRole('button', { name: 'Cancel' })).toBeVisible();

  await page.getByTestId('import-input').setInputFiles({
    name: 'mullvad_wireguard_linux_se.zip',
    mimeType: 'application/zip',
    buffer: mullvadZip(),
  });
  const results = page.getByTestId('import-results');
  await expect(results).toBeVisible();
  await expect(results.locator('caption')).toContainText('Import results:');
  for (const h of ['File', 'Result', 'Details']) {
    await expect(results.getByRole('columnheader', { name: h, exact: true })).toBeVisible();
  }
});

test('slot editor: fields, route list, warning, save', async () => {
  await page.goto(base() + '/#/slots/4');
  await expect(page.getByRole('heading', { name: 'Edit slot 4', exact: true })).toBeVisible();
  for (const l of ['Add a hop']) await expect(page.getByLabel(l, { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add hop', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Cancel', exact: true })).toBeVisible();
  await expect(page.getByRole('form', { name: 'Edit slot 4' })).toBeVisible();
  for (const l of ['Name', 'Bind address', 'Port']) await expect(page.getByLabel(l, { exact: true })).toBeVisible();
  await expect(page.getByLabel('Require username and password')).toBeVisible();
  await expect(page.getByTestId('block-lan-toggle')).toBeVisible();
  for (const l of ['Interval (s)', 'Fail threshold', 'Probe URL']) await expect(page.getByLabel(l)).toBeVisible();
  await expect(page.getByRole('list', { name: 'Route hops' })).toBeVisible();
  await expect(page.getByTestId('allow-direct-warning')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save slot' })).toBeVisible();
});

test('logs: slot picker, refresh, log pane', async () => {
  await page.goto(base() + '/#/logs');
  await expect(page.getByRole('heading', { name: 'Logs', exact: true })).toBeVisible();
  await expect(page.locator('#log-slot')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
  await expect(page.locator('pre.log')).toBeVisible();
  await expect(page.getByText('Last 500 lines')).toBeVisible();
});

test('settings: bind, health, export and import dialogs, password', async () => {
  await page.goto(base() + '/#/settings');
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
  await expect(page.locator('#bind-addr')).toBeVisible();
  await expect(page.locator('#start-on-enable')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Health defaults' })).toBeVisible();
  await expect(page.getByTestId('export-config')).toBeVisible();
  await expect(page.getByTestId('import-config')).toBeVisible();

  await page.getByTestId('export-config').click();
  const exp = page.locator('dialog[open]');
  await expect(exp.getByText('Mode', { exact: true })).toBeVisible();
  await expect(exp.getByRole('radio')).toHaveCount(2);
  for (const r of await exp.getByRole('radio').all()) await expect(r).toBeVisible();
  await expect(exp.getByTestId('export-passphrase')).toBeVisible();
  await expect(exp.getByTestId('export-confirm')).toBeVisible();
  await expect(exp.getByTestId('export-password')).toBeVisible();
  await expect(exp.getByTestId('export-submit')).toBeVisible();
  await exp.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);

  await page.getByTestId('import-config').click();
  const imp = page.locator('dialog[open]');
  await expect(imp.getByTestId('import-file')).toBeAttached();
  await expect(imp.getByTestId('import-password')).toBeVisible();
  await expect(imp.getByRole('button', { name: 'Review' })).toBeVisible();
  await expect(imp.getByRole('button', { name: 'Cancel' })).toBeVisible();
  await imp.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);

  for (const id of ['#pw-current', '#pw-new', '#pw-confirm']) await expect(page.locator(id)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Change password' })).toBeVisible();
});

test('login screen after logout', async () => {
  await page.goto(base() + '/#/');
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  await expect(page.getByLabel('Admin password', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();
});
