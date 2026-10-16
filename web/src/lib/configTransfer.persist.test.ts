// Network-boundary no-persistence tests. The real api.exportConfig /
// api.importConfig run with global fetch mocked; the dialogs' submit flow (build request,
// withClearedForm around the api call) is replayed. The real dialogs (bind:value, close(), the
// passphrase branch) are driven in a browser by tests/p9-roundtrip.check.ts. There is no live
// Svelte store holding config data (slotsStore is pure reducers), so none is asserted here.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { blankTransferForm, buildExportRequest, withClearedForm, type TransferForm } from './configTransfer';
import * as session from './session';

const PASS = 'CANARY-passphrase-7731';
const PW = 'CANARY-admin-9942';

const mkStorage = (): Storage => {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; }, clear: () => m.clear(), key: (i) => [...m.keys()][i] ?? null,
    getItem: (k) => m.get(k) ?? null, removeItem: (k) => void m.delete(k), setItem: (k, v) => void m.set(k, v),
  };
};
const entries = (s: Storage) => Object.fromEntries(Array.from({ length: s.length }, (_, i) => [s.key(i)!, s.getItem(s.key(i)!)]));

let calls: { url: string; body: unknown }[];
let loc: { href: string; hash: string };

function mockFetch(res: () => Response) {
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    calls.push({ url: String(url), body: init.body });
    return res();
  });
}

const stateDump = () =>
  JSON.stringify([
    entries(localStorage), entries(sessionStorage), session.session.token(), loc.href, loc.hash,
  ]);

const secrets = (): TransferForm => ({ mode: 'encrypted', passphrase: PASS, confirm: PASS, current_password: PW });
const empty = (f: TransferForm) => [f.passphrase, f.confirm, f.current_password];

async function bodyText(b: unknown): Promise<string> {
  if (typeof b === 'string') return b;
  if (b instanceof FormData) return [...b.entries()].map(([k, v]) => `${k}=${typeof v === 'string' ? v : v.name}`).join('&');
  return '';
}

function expectClean(f: TransferForm) {
  expect(empty(f)).toEqual(['', '', '']);
  expect(stateDump()).not.toContain('CANARY');
  for (const c of calls) expect(c.url).not.toContain('CANARY');
}

// Replays ExportDialog.submit / ImportDialog.run around the real api calls.
async function runExport(f: TransferForm) {
  const built = buildExportRequest(f);
  if (!built.ok) throw new Error('form invalid');
  return withClearedForm(f, () => api.exportConfig(built.body));
}
async function runImport(f: TransferForm, file: File) {
  return withClearedForm(f, () => api.importConfig(file, f.current_password, f.passphrase));
}

describe('no persistence (real api calls, fetch mocked)', () => {
  beforeEach(() => {
    calls = [];
    loc = { href: 'http://x/#/settings', hash: '#/settings' };
    vi.stubGlobal('localStorage', mkStorage());
    vi.stubGlobal('sessionStorage', mkStorage());
    vi.stubGlobal('location', loc);
    session.session.setToken('csrf-tok');
  });
  afterEach(() => {
    session.session.clear();
    vi.unstubAllGlobals();
  });

  it('exportConfig success: canaries only in request body, form wiped', async () => {
    mockFetch(() => new Response('{"mode":"encrypted"}', { status: 200, headers: { 'Content-Disposition': 'attachment; filename="c.json"' } }));
    const f = secrets();
    const out = await runExport(f);
    expect(out.filename).toBe('c.json');
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/config/export');
    expect(await bodyText(calls[0].body)).toContain(PASS);
    expectClean(f);
  });

  it('exportConfig non-2xx: rejects with ApiError, form wiped', async () => {
    mockFetch(() => new Response('{"error":"wrong password"}', { status: 403 }));
    const f = secrets();
    await expect(runExport(f)).rejects.toBeInstanceOf(ApiError);
    expect(await bodyText(calls[0].body)).toContain(PW);
    expectClean(f);
  });

  it('exportConfig 429: rejects and form wiped', async () => {
    mockFetch(() => new Response('{}', { status: 429, headers: { 'Retry-After': '30' } }));
    const f = secrets();
    await expect(runExport(f)).rejects.toMatchObject({ status: 429, retryAfter: 30 });
    expectClean(f);
  });

  it('importConfig success: canaries only in multipart body, form wiped', async () => {
    mockFetch(() => new Response('{"endpoints":1,"slots":2}', { status: 200 }));
    const f = secrets();
    const res = await runImport(f, new File(['{"mode":"encrypted"}'], 'c.json'));
    expect(res).toMatchObject({ endpoints: 1, slots: 2 });
    expect(calls[0].url).toBe('/api/config/import');
    const body = await bodyText(calls[0].body);
    expect(body).toContain(`passphrase=${PASS}`);
    expect(body).toContain(`current_password=${PW}`);
    expectClean(f);
  });

  it('importConfig non-2xx: rejects with ApiError, form wiped', async () => {
    mockFetch(() => new Response('{"error":"bad passphrase"}', { status: 400 }));
    const f = secrets();
    await expect(runImport(f, new File(['x'], 'c.json'))).rejects.toMatchObject({ status: 400, message: 'bad passphrase' });
    expectClean(f);
  });

  it('the checks can fail: a leaked canary is detected', async () => {
    mockFetch(() => new Response('{}', { status: 200 }));
    const f = secrets();
    await runImport(f, new File(['x'], 'c.json'));
    localStorage.setItem('leak', PASS);
    expect(stateDump()).toContain('CANARY');
  });

  it('would fail without the wipe', async () => {
    mockFetch(() => new Response('{}', { status: 200 }));
    const f = secrets();
    const built = buildExportRequest(f);
    if (!built.ok) throw new Error('invalid');
    await api.exportConfig(built.body); // no withClearedForm
    expect(empty(f)).not.toEqual(['', '', '']);
  });
});
