// README screenshots against the hermetic testrig (npm run readme-shots).
// Demo data only: rig endpoints get relay-style names, an imported zip adds
// Mullvad-style configs with fake keys and documentation-range IPs.
import { spawn, type ChildProcess } from 'node:child_process';
import { resolve } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { makeZip, mullvadConf } from './fixtures/mkzip';

const PASSWORD = 'readme-shots-password-1';
const OUT = resolve(import.meta.dirname, '../../docs/images');
const base = () => process.env.SMOKE_BASE_URL!;

const RIG_NAMES: Record<string, { name: string; country: string }> = {
  rigWG: { name: 'nl-ams-wg-004', country: 'NL' },
  rigWG2: { name: 'ch-zrh-wg-202', country: 'CH' },
  rigWG3: { name: 'se-got-wg-101', country: 'SE' },
  rigWG4: { name: 'de-fra-wg-007', country: 'DE' },
};
const IMPORTED = ['gb-lon-wg-001', 'us-nyc-wg-301', 'jp-tyo-wg-001', 'ca-tor-wg-107'];

async function api(page: Page, csrf: string, method: string, path: string, data?: unknown) {
  const r = await page.request.fetch(base() + path, {
    method,
    data,
    headers: { 'X-CSRF-Token': csrf },
  });
  expect(r.ok(), `${method} ${path}: ${r.status()} ${await r.text()}`).toBeTruthy();
  return r.status() === 204 ? null : r.json();
}

async function slot(page: Page, csrf: string, id: number, body: Record<string, unknown>) {
  const cur = await api(page, csrf, 'GET', `/api/slots/${id}`);
  await api(page, csrf, 'PUT', `/api/slots/${id}`, {
    name: cur.name, enabled: cur.enabled, bind_addr: cur.bind_addr, port: cur.port,
    route: cur.route, killswitch: cur.killswitch, block_lan_targets: false, health: cur.health,
    ...body,
  });
}

test.use({ deviceScaleFactor: 2 });

test('readme screenshots', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const setup = await page.request.post(base() + '/api/setup', { data: { password: PASSWORD } });
  expect(setup.ok()).toBeTruthy();
  const login = await page.request.post(base() + '/api/login', { data: { password: PASSWORD } });
  expect(login.ok()).toBeTruthy();
  const csrf = (await login.json()).csrf_token as string;

  // Rename the rig endpoints to relay-style demo names.
  const { endpoints } = await api(page, csrf, 'GET', '/api/endpoints');
  const id: Record<string, string> = {}; // rig name -> endpoint id
  for (const e of endpoints) {
    id[e.name] = e.id;
    const n = RIG_NAMES[e.name];
    if (n) await api(page, csrf, 'PUT', `/api/endpoints/${e.id}`, { ...e, name: n.name, mullvad_relay: n.name, country: n.country });
    if (e.name === 'rigSOCKS') await api(page, csrf, 'PUT', `/api/endpoints/${e.id}`, { ...e, name: 'rotating-residential' });
  }

  // Four named slots: one 2-hop WireGuard + proxy route, one allow-direct, one stopped.
  await slot(page, csrf, 1, { name: 'Browser', route: [id.rigWG!] });
  await slot(page, csrf, 2, { name: 'Shopping', route: [id.rigWG2!, id.rigSOCKS!] });
  await slot(page, csrf, 3, { name: 'Research', route: [id.rigWG3!], killswitch: 'allow-direct' });
  await slot(page, csrf, 4, { name: 'Spare', route: [id.rigWG4!], enabled: false });
  await api(page, csrf, 'POST', '/api/slots/4/stop');

  // Import a Mullvad-style zip through the drop zone's file input.
  await page.goto(base() + '/#/login');
  await page.getByLabel('Admin password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Endpoints' }).click();
  const zip = makeZip(IMPORTED.map((n, i) => ({ name: `${n}.conf`, data: mullvadConf(`198.51.100.${20 + i}`) })));
  await page.getByTestId('import-input').setInputFiles({ name: 'mullvad_wireguard_linux_all.zip', mimeType: 'application/zip', buffer: zip });
  await expect(page.getByText(IMPORTED[0]!).first()).toBeVisible();

  // Keep a little traffic flowing so the dashboard shows throughput.
  const curls: ChildProcess[] = [1080, 1081, 1082].map((port) =>
    spawn('sh', ['-c', `while :; do curl -s -o /dev/null --socks5-hostname 127.0.0.1:${port} http://echo.rig.invalid/; done`], { stdio: 'ignore' }),
  );
  try {
    await page.goto(base() + '/#/');
    await page.mouse.move(0, 0);
    await expect(page.locator('tr[data-slot="3"]').getByText('UP', { exact: true })).toBeVisible({ timeout: 60_000 });
    await expect(page.locator('tr[data-slot="2"]').getByText('UP', { exact: true })).toBeVisible({ timeout: 60_000 });
    // Let the throughput chart collect samples, then wait for it to drop its empty hint.
    await page.waitForTimeout(15_000);
    await expect(page.getByTestId('throughput-chart').getByText('No data yet')).toHaveCount(0, { timeout: 30_000 });
    await page.screenshot({ path: `${OUT}/dashboard.png` });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(1000);
    await page.screenshot({ path: `${OUT}/dashboard-mobile.png` });
    await page.setViewportSize({ width: 1280, height: 900 });
  } finally {
    for (const c of curls) c.kill('SIGKILL');
  }

  await page.goto(base() + '/#/slots/2');
  const route = page.locator('fieldset.rb');
  await expect(route).toBeVisible();
  await route.screenshot({ path: `${OUT}/route-builder.png` });
});
