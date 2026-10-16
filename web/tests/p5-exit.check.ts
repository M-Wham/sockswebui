// Exit check: keyboard-only flows and 360x740 checks, driven through page.keyboard.
// Run with: npm run p5-exit (own config, so `npm run smoke` stays at 1 test). Handles fresh or set-up state.
import { expect, test, type Page } from '@playwright/test';
import { mullvadZip } from './fixtures/mkzip';

const PASSWORD = 'smoke-test-password-1';
const NEW_PASSWORD = 'changed-password-2-xyz';
const MAX_TABS = 80;

async function tabTo(page: Page, sel: string, label: string) {
  for (let i = 0; i < MAX_TABS; i++) {
    const hit = await page.evaluate((s) => document.activeElement?.matches(s) ?? false, sel);
    if (hit) return;
    await page.keyboard.press('Tab');
  }
  throw new Error(`keyboard cannot reach: ${label} (${sel})`);
}
const done = (flow: string) => console.log(`KEYBOARD OK: ${flow}`);

test('keyboard-only flows and 360px layout', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  await page.setViewportSize({ width: 360, height: 740 });
  const overflow = async (screen: string) => {
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    console.log(`360PX ${screen}: scrollWidth=${sw}`);
    expect(sw, screen).toBeLessThanOrEqual(360);
  };

  // setup (fresh server) or login
  await page.goto(base + '/#/login');
  const needSetup = await page.evaluate(async () => (await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"password":"x"}' })).status === 409);
  if (needSetup) {
    await page.goto(base + '/#/setup');
    await overflow('setup');
    await tabTo(page, 'input[autocomplete="new-password"]', 'setup password');
    await page.keyboard.type(PASSWORD);
    await page.keyboard.press('Tab');
    await page.keyboard.type(PASSWORD);
    await page.keyboard.press('Enter');
    await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
    done('setup (type, Tab, type, Enter)');
    await page.getByRole('button', { name: 'Log out' }).focus();
    await page.keyboard.press('Enter');
  } else {
    console.log('setup: already completed by smoke; setup form checked only for overflow');
    await page.goto(base + '/#/setup');
    await overflow('setup');
  }

  // login
  await page.goto(base + '/#/login');
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  await overflow('login');
  await tabTo(page, '#login-password', 'login password');
  await page.keyboard.type(PASSWORD);
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
  done('login (Tab to field, type, Enter)');
  await overflow('dashboard');

  // nav by keyboard to each screen
  const navTo = async (name: string, heading: string) => {
    await tabTo(page, `nav a[href="#/${name === 'Endpoints' ? 'endpoints' : name.toLowerCase()}"]`, `nav ${name}`);
    await page.keyboard.press('Enter');
    await expect(page.getByRole('heading', { name: heading, exact: true }).first()).toBeVisible();
  };
  await page.focus('h1 a');

  // import via file picker (keyboard focus on the input, then file chosen)
  await navTo('Endpoints', 'Endpoints');
  await tabTo(page, '[data-testid="import-input"]', 'file picker');
  const chooser = page.waitForEvent('filechooser');
  await page.keyboard.press('Space');
  (await chooser).setFiles({ name: 'mullvad_wireguard_linux_se.zip', mimeType: 'application/zip', buffer: mullvadZip() });
  await expect(page.getByTestId('import-results')).toBeVisible();
  done('import via file picker (focus input, Space opens chooser)');
  await overflow('endpoints');

  // slot editor: route add / move / remove, allow-direct confirm
  await page.goto(base + '/#/slots/3');
  await expect(page.getByRole('heading', { name: 'Slot 3' })).toBeVisible();
  await overflow('slot editor (empty route)');
  const addBtns = page.getByRole('button', { name: /^Add .* to route$/ });
  await expect.poll(() => addBtns.count()).toBeGreaterThanOrEqual(2);
  const addNth = async (n: number) => {
    const b = addBtns.nth(n);
    await b.focus();
    await page.keyboard.press('Enter');
  };
  await addNth(0);
  await addNth(1);
  const hops = page.getByRole('list', { name: 'Route hops' }).locator('li:not(.drop-end)');
  await expect(hops).toHaveCount(2);
  const before = await hops.allInnerTexts();
  done('route add (Enter on Add buttons)');
  await page.getByRole('button', { name: /^Move .* down$/ }).first().focus();
  await page.keyboard.press('Enter');
  const after = await hops.allInnerTexts();
  expect(after[0].split('\n')[0]).not.toBe(before[0].split('\n')[0]);
  done('route move down (Enter)');
  await page.getByRole('button', { name: /^Move .* up$/ }).nth(1).focus();
  await page.keyboard.press('Space');
  done('route move up (Space)');
  await page.getByRole('button', { name: /^Remove / }).last().focus();
  await page.keyboard.press('Enter');
  await expect(hops).toHaveCount(1);
  done('route remove (Enter)');
  await overflow('slot editor (with route)');

  await page.getByRole('radio', { name: /Allow direct/ }).focus();
  await page.keyboard.press('Space');
  const dlg = page.getByRole('alertdialog');
  await expect(dlg).toBeVisible();
  await expect(dlg).toBeFocused();
  await overflow('slot editor (allow-direct confirm)');
  await tabTo(page, 'button.danger', 'confirm button');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('allow-direct-warning')).toBeVisible();
  done('allow-direct confirm (Space on radio, Tab to red confirm, Enter)');
  // revert to block via keyboard, then save
  await page.getByRole('radio', { name: /^Block/ }).focus();
  await page.keyboard.press('Space');
  await expect(page.getByTestId('allow-direct-warning')).toHaveCount(0);
  await page.getByLabel('Name', { exact: true }).focus();
  await page.keyboard.type('kb');
  await page.getByRole('button', { name: 'Save slot' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('status').filter({ hasText: /saved/i })).toBeVisible();
  done('slot save (Enter)');

  // start / stop (slot 1 is seeded and UP by --dev-testrig)
  await page.goto(base + '/#/');
  const card = page.locator('tr[data-slot="1"]');
  await expect(card.locator('[data-state]')).toHaveAttribute('data-state', /UP|STOPPED|DEGRADED/); // smoke leaves the shared rig paused
  await overflow('dashboard (slot cards)');
  if (await card.getByRole('button', { name: /^Stop/ }).count()) {
    await card.getByRole('button', { name: /^Stop/ }).focus();
    await page.keyboard.press('Enter');
    await expect(card.locator('[data-state]')).toHaveAttribute('data-state', 'STOPPED', { timeout: 30_000 });
  }
  await card.getByRole('button', { name: /^Start/ }).focus();
  await page.keyboard.press('Enter');
  await expect(card.locator('[data-state]')).not.toHaveAttribute('data-state', 'STOPPED', { timeout: 30_000 });
  console.log('state after start (from server):', await card.locator('[data-state]').getAttribute('data-state'));
  done('start/stop (Enter on buttons; STOPPED, then a server-reported state)');

  // logs
  await page.focus('h1 a');
  await navTo('Logs', 'Logs');
  await overflow('logs');
  await tabTo(page, '#log-slot', 'log slot select');
  await page.keyboard.press('ArrowDown');
  await tabTo(page, 'button.secondary', 'Refresh');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300); // let the reload finish; focus must stay on Refresh
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeFocused();
  await tabTo(page, 'pre.log', 'log pane');
  await page.keyboard.press('ArrowDown');
  done('logs (select slot with arrows, Refresh, focus log pane)');

  // settings + change password
  await page.focus('h1 a');
  await navTo('Settings', 'Settings');
  await expect(page.locator('#pw-current')).toBeVisible();
  await overflow('settings (incl. change-password form)');
  await tabTo(page, '#bind-addr', 'bind address');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Enter'); // Save
  await expect(page.getByText(/Saved\./)).toBeVisible();
  await tabTo(page, '#pw-current', 'current password');
  await page.keyboard.type(PASSWORD);
  await page.keyboard.press('Tab');
  await page.keyboard.type(NEW_PASSWORD);
  await page.keyboard.press('Tab');
  await page.keyboard.type(NEW_PASSWORD);
  await page.keyboard.press('Enter');
  await expect(page.getByRole('status').filter({ hasText: /password/i }).first()).toBeVisible();
  done('settings (bind Save) and change-password (type x3, Enter)');

  // logout, then log in with the new password
  await tabTo(page, '[data-testid="logout"]', 'Log out');
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  done('logout (Enter on nav button)');
  await tabTo(page, '#login-password', 'login password');
  await page.keyboard.type(NEW_PASSWORD);
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
  done('login with new password');
});
