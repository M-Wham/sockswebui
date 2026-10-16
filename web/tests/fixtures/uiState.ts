// Shared state helpers for the ui-* specs. Safe to rerun against a reused server.
import { expect, type Cookie, type Page } from '@playwright/test';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { mullvadZip } from './mkzip';

export const PASSWORD = 'smoke-test-password-1';

interface Saved {
  base: string;
  csrf: string;
  cookies: Cookie[];
}

// The login limiter allows 5 password attempts a minute per client, so one session is shared by
// every spec file of a run. It is cached in the per-run dir global-setup creates (SMOKE_SESSION_DIR,
// removed at teardown), keyed by the server's base URL, and verified with a GET, which the limiter
// does not count; only a dead session costs a new login. Without the env var nothing is cached.
function cacheFile(): string | null {
  const dir = process.env.SMOKE_SESSION_DIR;
  return dir ? join(dir, 'session.json') : null;
}

async function restoreSession(page: Page, base: string): Promise<boolean> {
  const file = cacheFile();
  if (!file || !existsSync(file)) return false;
  let saved: Saved;
  try {
    saved = JSON.parse(readFileSync(file, 'utf8')) as Saved;
  } catch {
    return false;
  }
  if (saved.base !== base) return false;
  await page.context().addCookies(saved.cookies);
  const ok = await page.request.get(base + '/api/slots');
  if (ok.status() !== 200) return false;
  await page.addInitScript((t) => {
    try {
      sessionStorage.setItem('sockswebui.csrf', t);
    } catch {
      /* storage unavailable */
    }
  }, saved.csrf);
  await page.goto(base + '/#/');
  return page.getByRole('heading', { name: 'Slots', exact: true }).waitFor({ timeout: 8000 }).then(() => true, () => false);
}

async function saveSession(page: Page, base: string): Promise<void> {
  const file = cacheFile();
  if (!file) return;
  const csrf = await page.evaluate(() => sessionStorage.getItem('sockswebui.csrf') ?? '');
  const cookies = await page.context().cookies(base);
  writeFileSync(file, JSON.stringify({ base, csrf, cookies } satisfies Saved), { mode: 0o600 });
}

/**
 * Set up (fresh server, login answers 409) or log in (reused server); ends on the Dashboard.
 * The server allows 5 password attempts a minute per client and the probe counts as one, so
 * specs log in once and share the page. A 429 is waited out and retried.
 */
export async function loginOrSetup(page: Page, base: string): Promise<void> {
  if (await restoreSession(page, base)) return;
  for (let attempt = 0; attempt < 4; attempt++) {
    const probe = await page.request.post(base + '/api/login', { data: { password: 'x' } });
    if (probe.status() === 429) {
      await page.waitForTimeout(Math.min(65, Number(probe.headers()['retry-after'] ?? 15)) * 1000);
      continue;
    }
    const needsSetup = probe.status() === 409;
    await page.goto(base + (needsSetup ? '/#/setup' : '/#/login'));
    if (needsSetup) {
      await page.getByLabel('New admin password', { exact: true }).fill(PASSWORD);
      await page.getByLabel('Confirm password').fill(PASSWORD);
      await page.getByRole('button', { name: 'Set password and log in' }).click();
    } else {
      await page.getByLabel('Admin password', { exact: true }).fill(PASSWORD);
      await page.getByRole('button', { name: 'Log in' }).click();
    }
    const dash = page.getByRole('heading', { name: 'Slots', exact: true });
    if (await dash.waitFor({ timeout: 8000 }).then(() => true, () => false)) {
      await saveSession(page, base);
      return;
    }
    await page.waitForTimeout(20_000); // probably rate limited; let the window drain
  }
  await expect(page.getByRole('heading', { name: 'Slots', exact: true })).toBeVisible();
}

interface ApiResult {
  status: number;
  body: unknown;
}

/** fetch with the page's session cookie and CSRF token, as smoke.spec.ts does. */
export async function apiCall(page: Page, method: string, path: string, json?: unknown): Promise<ApiResult> {
  return page.evaluate(
    async ([m, p, j]) => {
      const r = await fetch(p as string, {
        method: m as string,
        credentials: 'same-origin',
        headers: {
          'X-CSRF-Token': sessionStorage.getItem('sockswebui.csrf') ?? '',
          ...(j === undefined ? {} : { 'Content-Type': 'application/json' }),
        },
        body: j === undefined ? undefined : JSON.stringify(j),
      });
      let body: unknown = null;
      try {
        body = await r.json();
      } catch {
        // no JSON body
      }
      return { status: r.status, body };
    },
    [method, path, json] as [string, string, unknown],
  );
}

interface Ep {
  id: string;
  kind: string;
}

/**
 * Makes sure slot 4 is configured as `inventory` (allow-direct, disabled, port 1084) over a
 * WireGuard endpoint. Imports the Mullvad zip when the server has no WireGuard endpoint yet.
 * Call after loginOrSetup.
 */
export async function ensureInventorySlot(page: Page, base: string): Promise<void> {
  void base;
  const list = async () => ((await apiCall(page, 'GET', '/api/endpoints')).body as { endpoints?: Ep[] } | null)?.endpoints ?? [];
  let wg = (await list()).filter((e) => e.kind === 'wireguard');
  if (wg.length === 0) {
    await page.evaluate(
      async ([b64]) => {
        const bytes = Uint8Array.from(atob(b64 as string), (c) => c.charCodeAt(0));
        const form = new FormData();
        form.append('files', new File([bytes], 'mullvad_wireguard_linux_se.zip', { type: 'application/zip' }));
        await fetch('/api/endpoints/import', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'X-CSRF-Token': sessionStorage.getItem('sockswebui.csrf') ?? '' },
          body: form,
        });
      },
      [mullvadZip().toString('base64')],
    );
    wg = (await list()).filter((e) => e.kind === 'wireguard');
  }
  expect(wg.length, 'a wireguard endpoint exists').toBeGreaterThan(0);

  // A WireGuard endpoint may serve only one slot: prefer one no other slot routes through.
  const slots = ((await apiCall(page, 'GET', '/api/slots')).body as { slots?: { id: number; route?: string[] }[] } | null)?.slots ?? [];
  const used = new Set(slots.filter((s) => s.id !== 4).flatMap((s) => s.route ?? []));
  const pick = wg.find((e) => !used.has(e.id)) ?? wg[0]!;

  const res = await apiCall(page, 'PUT', '/api/slots/4', {
    name: 'inventory',
    enabled: false,
    bind_addr: '127.0.0.1',
    port: 1084,
    socks_auth: null,
    route: [pick.id],
    killswitch: 'allow-direct',
    block_lan_targets: false,
  });
  expect(res.status, JSON.stringify(res.body)).toBeLessThan(300);
}
