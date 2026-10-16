import { describe, expect, it, vi } from 'vitest';
import {
  blankTransferForm,
  buildExportRequest,
  clearTransferForm,
  downloadBlob,
  filenameFromDisposition,
  importResultView,
  parseExportMode,
  rateLimitText,
  withClearedForm,
  utf8Length,
} from './configTransfer';
import type { SlotConfig } from './slotsStore';
import * as slotsStore from './slotsStore';
import * as session from './session';

const form = (o: Partial<ReturnType<typeof blankTransferForm>> = {}) => ({
  ...blankTransferForm(),
  mode: 'encrypted' as const,
  passphrase: 'correct horse battery',
  confirm: 'correct horse battery',
  current_password: 'admin-pass-1',
  ...o,
});

describe('buildExportRequest', () => {
  it('builds an encrypted request', () => {
    expect(buildExportRequest(form())).toEqual({
      ok: true,
      body: { current_password: 'admin-pass-1', mode: 'encrypted', passphrase: 'correct horse battery' },
    });
  });
  it('requires a matching confirm', () => {
    const r = buildExportRequest(form({ confirm: 'different passphrase' }));
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.errors.confirm).toBeTruthy();
  });
  it('counts bytes, not characters', () => {
    const eleven = '€'.repeat(4); // 4 chars, 12 bytes
    expect(utf8Length(eleven)).toBe(12);
    expect(buildExportRequest(form({ passphrase: eleven, confirm: eleven })).ok).toBe(true);
    const short = 'a'.repeat(11);
    const r = buildExportRequest(form({ passphrase: short, confirm: short }));
    expect(r.ok).toBe(false);
    const long = '€'.repeat(342); // 342 chars, 1026 bytes
    const r2 = buildExportRequest(form({ passphrase: long, confirm: long }));
    expect(r2.ok).toBe(false);
    const edge = 'a'.repeat(1024);
    expect(buildExportRequest(form({ passphrase: edge, confirm: edge })).ok).toBe(true);
    const over = 'a'.repeat(1025);
    expect(buildExportRequest(form({ passphrase: over, confirm: over })).ok).toBe(false);
  });
  it('no-secrets omits passphrase but still needs the admin password', () => {
    const r = buildExportRequest(form({ mode: 'no-secrets' }));
    expect(r).toEqual({ ok: true, body: { current_password: 'admin-pass-1', mode: 'no-secrets' } });
    const bad = buildExportRequest(form({ mode: 'no-secrets', current_password: '' }));
    expect(bad.ok).toBe(false);
    if (!bad.ok) expect(bad.errors.current_password).toBeTruthy();
  });
});

describe('parseExportMode', () => {
  it('reads mode from content, never the name', () => {
    expect(parseExportMode('{"format":"x","mode":"no-secrets"}')).toEqual({ mode: 'no-secrets' });
    expect(parseExportMode('{"mode":"encrypted"}')).toEqual({ mode: 'encrypted' });
  });
  it('gives readable errors', () => {
    const a = parseExportMode('not json');
    const b = parseExportMode('{"mode":"weird"}');
    const c = parseExportMode('[1]');
    for (const r of [a, b, c]) expect('error' in r && r.error.length > 10).toBe(true);
  });
});

describe('filenameFromDisposition', () => {
  it('extracts a quoted filename', () => {
    expect(filenameFromDisposition('attachment; filename="sockswebui-pi-1.json"')).toBe('sockswebui-pi-1.json');
  });
  it('falls back safely', () => {
    expect(filenameFromDisposition(null)).toBe('sockswebui-config.json');
    expect(filenameFromDisposition('attachment')).toBe('sockswebui-config.json');
    expect(filenameFromDisposition('attachment; filename="../../etc/passwd"')).toBe('sockswebui-config.json');
    expect(filenameFromDisposition('attachment; filename="a/b.json"')).toBe('sockswebui-config.json');
  });
});

const slot = (id: number, route: string[], configured = true): SlotConfig => ({
  id, configured, name: '', enabled: false, bind_addr: '127.0.0.1', port: 1080 + id, route, killswitch: 'block',
});

describe('importResultView', () => {
  const res = { endpoints: 3, slots: 3, backup: 'b.json', secrets_missing: { endpoints: ['e1'], slots: [3] } };
  const slots = [slot(1, ['e1']), slot(2, ['e2']), slot(3, ['e2']), slot(4, [], false)];
  const v = importResultView(res, slots);
  it('counts and links', () => {
    expect(v.endpoints).toBe(3);
    expect(v.slots).toBe(3);
    expect(v.missingEndpoints).toEqual([{ id: 'e1', href: '#/endpoints/e1' }]);
    expect(v.missingSlots.map((s) => [s.id, s.href])).toEqual([[1, '#/slots/1'], [3, '#/slots/3']]);
  });
  it('marks slots blocked through a route endpoint', () => {
    expect(v.missingSlots[0].via).toEqual(['e1']);
    expect(v.missingSlots[1].via).toEqual([]);
  });
  it('start-all targets are configured, routed, unblocked slots', () => {
    expect(v.startable).toEqual([2]);
  });
  it('tolerates missing fields', () => {
    const e = importResultView({}, []);
    expect(e).toMatchObject({ endpoints: 0, slots: 0, startable: [], missingEndpoints: [], missingSlots: [] });
  });
});

describe('no persistence', () => {
  it('clears secrets after success or failure', () => {
    const f = form();
    clearTransferForm(f);
    expect(f.passphrase).toBe('');
    expect(f.confirm).toBe('');
    expect(f.current_password).toBe('');
  });
  const mk = (): Storage => {
    const m = new Map<string, string>();
    return {
      get length() { return m.size; }, clear: () => m.clear(), key: (i) => [...m.keys()][i] ?? null,
      getItem: (k) => m.get(k) ?? null, removeItem: (k) => void m.delete(k), setItem: (k, v) => void m.set(k, v),
    };
  };
  const secrets = () => form({ passphrase: 'zzz-secret-passphrase', confirm: 'zzz-secret-passphrase', current_password: 'zzz-admin' });
  const dump = () =>
    JSON.stringify([
      Object.fromEntries(Array.from({ length: localStorage.length }, (_, i) => [localStorage.key(i), localStorage.getItem(localStorage.key(i)!)])),
      Object.fromEntries(Array.from({ length: sessionStorage.length }, (_, i) => [sessionStorage.key(i), sessionStorage.getItem(sessionStorage.key(i)!)])),
      slotsStore.initialSlots(),
      session.session.token(),
      globalThis.location?.href ?? '',
    ]);

  it('withClearedForm wipes the form after a resolved request', async () => {
    vi.stubGlobal('localStorage', mk());
    vi.stubGlobal('sessionStorage', mk());
    const f = secrets();
    const seen: string[] = [];
    const out = await withClearedForm(f, async () => {
      seen.push(f.passphrase); // values are available while the request runs
      return 'ok';
    });
    expect(out).toBe('ok');
    expect(seen).toEqual(['zzz-secret-passphrase']);
    expect([f.passphrase, f.confirm, f.current_password]).toEqual(['', '', '']);
    expect(dump()).not.toContain('zzz');
    vi.unstubAllGlobals();
  });
  it('withClearedForm wipes the form after a rejected request and rethrows', async () => {
    vi.stubGlobal('localStorage', mk());
    vi.stubGlobal('sessionStorage', mk());
    const f = secrets();
    await expect(withClearedForm(f, async () => { throw new Error('boom'); })).rejects.toThrow('boom');
    expect([f.passphrase, f.confirm, f.current_password]).toEqual(['', '', '']);
    expect(dump()).not.toContain('zzz');
    vi.unstubAllGlobals();
  });
});

describe('rateLimitText', () => {
  it('uses its own wording and the Retry-After wait', () => {
    expect(rateLimitText(30)).toBe('Too many attempts. Try again in 30 seconds.');
    expect(rateLimitText(1)).toBe('Too many attempts. Try again in 1 second.');
    expect(rateLimitText(undefined)).toBe('Too many attempts. Try again in a few seconds.');
  });
});

describe('downloadBlob', () => {
  it('creates then revokes the object URL', () => {
    const order: string[] = [];
    const click = vi.fn(() => order.push('click'));
    const a = { href: '', download: '', click, remove: vi.fn() } as unknown as HTMLAnchorElement;
    const createObjectURL = vi.fn(() => (order.push('create'), 'blob:x'));
    const revokeObjectURL = vi.fn(() => void order.push('revoke'));
    downloadBlob(new Blob(['x']), 'f.json', {
      createObjectURL, revokeObjectURL, createAnchor: () => a, append: vi.fn(), schedule: (fn) => fn(),
    });
    expect(a.href).toBe('blob:x');
    expect(a.download).toBe('f.json');
    expect(order).toEqual(['create', 'click', 'revoke']);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:x');
  });
});
