import { describe, expect, it } from 'vitest';
import { buildProxyRequest, emptyProxyForm, formFromEndpoint, hasErrors, passwordStatus, proxyFormWarnings, validateProxyForm } from './proxyForm';
import type { EndpointView } from './endpointFilter';

const existing: EndpointView = {
  id: 'ep-1',
  name: 'gatewayproxy',
  kind: 'socks5',
  host: '10.0.0.1',
  port: 1080,
  username: 'bob',
  password_set: true,
};

describe('formFromEndpoint', () => {
  it('never pre-fills the password and records whether one is set', () => {
    const f = formFromEndpoint(existing);
    expect(f.password).toBe('');
    expect(f.passwordSet).toBe(true);
    expect(f).toMatchObject({ name: 'gatewayproxy', kind: 'socks5', host: '10.0.0.1', port: '1080', username: 'bob' });
    // even if a server ever sent one, it is ignored
    expect(formFromEndpoint({ ...existing, password: 'leak' } as EndpointView).password).toBe('');
  });
  it('renders password_set as "set / not set"', () => {
    expect(passwordStatus(formFromEndpoint(existing).passwordSet)).toBe('set');
    expect(passwordStatus(formFromEndpoint({ ...existing, password_set: false }).passwordSet)).toBe('not set');
    expect(passwordStatus(formFromEndpoint({ ...existing, password_set: undefined }).passwordSet)).toBe('not set');
  });
});

describe('buildProxyRequest', () => {
  it('omits password for an existing proxy when the field is untouched', () => {
    const req = buildProxyRequest(formFromEndpoint(existing), 'ep-1');
    expect(req.method).toBe('PUT');
    expect(req.path).toBe('/api/endpoints/ep-1');
    expect(req.body).toEqual({ name: 'gatewayproxy', kind: 'socks5', host: '10.0.0.1', port: 1080, username: 'bob' });
    expect('password' in req.body).toBe(false);
  });
  it('sends a newly typed password', () => {
    const f = { ...formFromEndpoint(existing), password: 'n3w' };
    expect(buildProxyRequest(f, 'ep-1').body.password).toBe('n3w');
  });
  it('sends an empty password only when clearing is requested', () => {
    const f = { ...formFromEndpoint(existing), clearPassword: true };
    expect(buildProxyRequest(f, 'ep-1').body.password).toBe('');
  });
  it('POSTs a new proxy, trimming text and encoding the id in the path', () => {
    const f = { ...emptyProxyForm(), name: ' up ', kind: 'http' as const, host: ' h.example ', port: '3128' };
    const req = buildProxyRequest(f);
    expect(req.method).toBe('POST');
    expect(req.path).toBe('/api/endpoints');
    expect(req.body).toEqual({ name: 'up', kind: 'http', host: 'h.example', port: 3128, username: '' });
    expect(buildProxyRequest(f, 'a/b').path).toBe('/api/endpoints/a%2Fb');
  });
  it('sends port 0 for a non-numeric port so the server reports the field error', () => {
    expect(buildProxyRequest({ ...emptyProxyForm(), port: 'abc' }).body.port).toBe(0);
  });
});

describe('rotating session', () => {
  const on = (o: Partial<ReturnType<typeof emptyProxyForm>>) => ({ ...emptyProxyForm(), sessionEnabled: true, ...o });
  it('builds the payload without current_id and omits session when off', () => {
    const e: EndpointView = { ...existing, username: 'u-{session}', session: { charset: 'digits', length: 8, current_id: '12345678' } };
    const f = formFromEndpoint(e);
    expect(f.currentId).toBe('12345678');
    expect(buildProxyRequest(f, 'ep-1').body.session).toEqual({ charset: 'digits', length: 8 });
    expect(buildProxyRequest({ ...f, sessionEnabled: false }, 'ep-1').body.session).toBeUndefined();
  });
  it('accepts exactly one token', () => {
    expect(validateProxyForm(on({ username: 'u-{session}' }))).toEqual({});
    expect(validateProxyForm(on({ username: 'u', password: 'p{session}' }))).toEqual({});
  });
  it('rejects token count 0 and 2', () => {
    expect(validateProxyForm(on({ username: 'u' })).session).toBeTruthy();
    expect(validateProxyForm(on({ username: 'u-{session}', password: 'p{session}' })).session).toMatch(/exactly once/);
  });
  it('rejects a token-only username or password', () => {
    expect(validateProxyForm(on({ username: '{session}' })).username).toBeTruthy();
    expect(validateProxyForm(on({ username: 'u', password: '{session}' })).password).toBeTruthy();
  });
  it('rejects length outside 1-32', () => {
    for (const l of ['0', '33', 'x', '']) expect(validateProxyForm(on({ username: 'u{session}', sessionLength: l })).sessionLength).toBeTruthy();
    for (const l of ['1', '32']) expect(validateProxyForm(on({ username: 'u{session}', sessionLength: l })).sessionLength).toBeUndefined();
  });
  it('enforces the socks5 255-byte limit only for socks5', () => {
    const u = 'a'.repeat(255 - 10 + 1) + '{session}'.slice(0, 0) + '{session}';
    const base = { username: 'a'.repeat(246) + '{session}' }; // 246 + 10 = 256
    expect(validateProxyForm(on(base)).username).toBeTruthy();
    expect(validateProxyForm(on({ username: 'a'.repeat(245) + '{session}' })).username).toBeUndefined();
    expect(validateProxyForm(on({ ...base, kind: 'http' })).username).toBeUndefined();
    expect(u.length).toBeGreaterThan(0);
  });
  it('uses password_has_session when the password is not retyped', () => {
    const f = formFromEndpoint({ ...existing, username: 'bob', password_has_session: true, session: { charset: 'alnum', length: 10 } });
    expect(validateProxyForm(f, true)).toEqual({});
    expect(validateProxyForm({ ...f, username: 'b-{session}' }, true).session).toMatch(/exactly once/);
    expect(validateProxyForm({ ...f, password: 'new' }, true).session).toBeTruthy();
    expect(validateProxyForm({ ...f, clearPassword: true }, true).session).toBeTruthy();
  });
  it('tells the user to remove the token when the toggle is off', () => {
    const e = validateProxyForm({ ...emptyProxyForm(), username: 'u{session}' });
    expect(e.session).toMatch(/Remove \{session\}/);
    expect(hasErrors(e)).toBe(true);
    expect(hasErrors({})).toBe(false);
  });
  it('never echoes typed username or password in messages', () => {
    const f = on({ username: 'SECRETUSER{session}{session}', password: 'SECRETPASS{session}', sessionLength: '99' });
    const text = JSON.stringify(validateProxyForm(f));
    expect(text).not.toContain('SECRETUSER');
    expect(text).not.toContain('SECRETPASS');
    const t2 = JSON.stringify(validateProxyForm(on({ username: 'SECRETUSER' + 'a'.repeat(300) + '{session}' })));
    expect(t2).not.toContain('SECRETUSER');
  });
});

describe('proxyFormWarnings', () => {
  const w = (host: string) => proxyFormWarnings({ ...emptyProxyForm(), host });
  it('warns for loopback, link-local and metadata hosts', () => {
    for (const h of ['127.0.0.1', 'localhost', '::1', '169.254.169.254', 'fe80::1', 'fd00:ec2::254', '100.100.100.200',
      '[::ffff:127.0.0.1]', 'fe80::1%eth0', '::127.0.0.1']) {
      expect(w(h).length, h).toBe(1);
    }
  });
  it('does not warn for documentation ranges or ordinary names', () => {
    for (const h of ['198.51.100.7', 'proxy.example.net', 'localhost.example.com', '']) expect(w(h), h).toEqual([]);
  });
  it('never turns into an error', () => {
    const f = { ...emptyProxyForm(), host: '127.0.0.1', port: '1080' };
    expect(hasErrors(validateProxyForm(f))).toBe(false);
  });
});
