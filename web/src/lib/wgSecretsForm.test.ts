import { describe, expect, it } from 'vitest';
import type { EndpointView } from './endpointFilter';
import {
  buildWgSecretsRequest,
  clearWgSecretsForm,
  emptyWgSecretsForm,
  privateKeyHint,
  validateWgSecrets,
  saveWgSecrets,
} from './wgSecretsForm';
import { ApiError } from './api';

const KEY = 'AAAAfakefakefakefakefakefakefakefakefakefake1=';
const PSK = 'BBBBfakefakefakefakefakefakefakefakefakefake2=';

const ep: EndpointView = {
  id: 'ep-1', name: 'se-got-wg-001', kind: 'wireguard', private_key_set: false, preshared_key_set: false,
  addresses: ['10.64.0.2/32'], endpoint: '1.2.3.4:51820', country: 'se', mullvad_relay: 'se-got-wg-001',
};

describe('buildWgSecretsRequest', () => {
  it('sends only typed secrets and the stored non-secret settings', () => {
    const f = { privateKey: KEY, presharedKey: '' };
    const r = buildWgSecretsRequest(ep, f);
    expect(r.method).toBe('PUT');
    expect(r.path).toBe('/api/endpoints/ep-1');
    expect(r.body.private_key).toBe(KEY);
    expect('preshared_key' in r.body).toBe(false);
    expect(r.body.kind).toBe('wireguard');
    expect(r.body.endpoint).toBe('1.2.3.4:51820');
    expect(r.body.addresses).toEqual(['10.64.0.2/32']);
    expect(JSON.stringify(r.body)).not.toContain('_set');
  });
  it('omits private_key when only the preshared key is typed', () => {
    const r = buildWgSecretsRequest(ep, { privateKey: '', presharedKey: PSK });
    expect('private_key' in r.body).toBe(false);
    expect(r.body.preshared_key).toBe(PSK);
  });
  it('trims values', () => {
    const r = buildWgSecretsRequest(ep, { privateKey: `  ${KEY}\n`, presharedKey: ` ${PSK} ` });
    expect(r.body.private_key).toBe(KEY);
    expect(r.body.preshared_key).toBe(PSK);
  });
});

describe('validateWgSecrets', () => {
  it('rejects both fields empty, including whitespace only', () => {
    expect(validateWgSecrets({ privateKey: '', presharedKey: '' }).form).toBeTruthy();
    expect(validateWgSecrets({ privateKey: ' ', presharedKey: '\t' }).form).toBeTruthy();
  });
  it('accepts either field', () => {
    expect(validateWgSecrets({ privateKey: KEY, presharedKey: '' })).toEqual({});
    expect(validateWgSecrets({ privateKey: '', presharedKey: PSK })).toEqual({});
  });
  it('error text never contains typed values', () => {
    const e = validateWgSecrets({ privateKey: ' ', presharedKey: '' });
    expect(JSON.stringify(e)).not.toContain(KEY);
  });
});

describe('write-only behaviour', () => {
  it('clearWgSecretsForm empties both fields', () => {
    const f = { privateKey: KEY, presharedKey: PSK };
    clearWgSecretsForm(f);
    expect(f).toEqual(emptyWgSecretsForm());
  });
  it('saveWgSecrets clears the form after success', async () => {
    const f = { privateKey: KEY, presharedKey: PSK };
    const calls: unknown[] = [];
    await saveWgSecrets(ep, f, async (m, p, b) => { calls.push([m, p, b]); return {}; });
    expect(calls).toHaveLength(1);
    expect(f).toEqual(emptyWgSecretsForm());
  });
  it('saveWgSecrets clears the form after failure and the error has no key text', async () => {
    const f = { privateKey: KEY, presharedKey: '' };
    const out = await saveWgSecrets(ep, f, async () => { throw new ApiError(400, 'invalid', [{ field: 'private_key', message: 'must be a base64 key' }]); });
    expect(out.ok).toBe(false);
    expect(f).toEqual(emptyWgSecretsForm());
    expect(JSON.stringify(out)).not.toContain(KEY);
  });
  it('saveWgSecrets does not call the API for an invalid form', async () => {
    let n = 0;
    const out = await saveWgSecrets(ep, emptyWgSecretsForm(), async () => { n++; return {}; });
    expect(n).toBe(0);
    expect(out.ok).toBe(false);
  });
});

describe('privateKeyHint', () => {
  it('flags a missing private key only', () => {
    expect(privateKeyHint(ep)).toBe('private key missing');
    expect(privateKeyHint({ ...ep, private_key_set: true })).toBe('');
  });
});
