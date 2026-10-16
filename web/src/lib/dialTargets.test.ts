import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { classify, isSensitiveHost } from './dialTargets';

// Test-only read of the Go-owned vectors; never imported, so it stays out of the bundle.
const vectors: { addr: string; want: string }[] = JSON.parse(
  readFileSync(new URL('../../../internal/egress/testdata/devicedeny-vectors.json', import.meta.url), 'utf8'),
);

describe('classify matches the shared device-deny vectors', () => {
  it('has rows', () => expect(vectors.length).toBeGreaterThan(30));
  for (const v of vectors) {
    it(`${v.addr} -> ${v.want}`, () => expect(classify(v.addr)).toBe(v.want));
  }
});

describe('classify extras', () => {
  it('brackets, case and names', () => {
    expect(classify('[::ffff:127.0.0.1]')).toBe('deny');
    expect(classify('[FE80::1]')).toBe('deny');
    expect(classify('localhost')).toBe('deny');
    expect(classify('a.localhost')).toBe('deny');
    expect(classify('localhost.example.com')).toBe('allow');
    expect(classify('proxy.example.net')).toBe('allow');
  });
});

describe('isSensitiveHost', () => {
  it('pins the warning subset', () => {
    for (const h of ['0.0.0.0', '::', '224.0.0.1', 'ff02::1', '[::1]', 'fe80::1%eth0', '::ffff:7f00:1', '64:ff9b::7f00:1']) {
      expect(isSensitiveHost(h), h).toBe(true);
    }
    for (const h of ['198.51.100.7', '192.0.2.1', '240.0.0.1', '10.0.0.1', '8.8.8.8', '2001:db8::1']) {
      expect(isSensitiveHost(h), h).toBe(false);
    }
  });
});
