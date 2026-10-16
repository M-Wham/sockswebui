import { describe, expect, it } from 'vitest';
import { MAX_ROUTE_HOPS, validateRoute, type RouteEndpoint } from './routeRules';

const endpoints: RouteEndpoint[] = [
  { id: 'WG-SE', kind: 'wireguard' },
  { id: 'WG-US', kind: 'wireguard' },
  { id: 'GatewayProxy', kind: 'socks5' },
  { id: 'OtherProxy', kind: 'http' },
];

describe('validateRoute — documented valid examples', () => {
  const valid: string[][] = [
    ['WG-SE'],
    ['WG-SE', 'WG-US'],
    ['WG-SE', 'GatewayProxy'],
    ['GatewayProxy'],
    ['GatewayProxy', 'OtherProxy'],
  ];
  for (const route of valid) {
    it(`accepts [${route.join(', ')}]`, () => {
      const r = validateRoute(route, endpoints);
      expect(r.errors).toEqual([]);
    });
  }

  it('accepts the maximum of 4 hops', () => {
    const r = validateRoute(['WG-SE', 'WG-US', 'GatewayProxy', 'OtherProxy'], endpoints);
    expect(r.errors).toEqual([]);
  });
});

describe('validateRoute — documented invalid examples', () => {
  it('rejects [GatewayProxy, WG-SE] and explains WireGuard needs UDP / must precede proxies', () => {
    const r = validateRoute(['GatewayProxy', 'WG-SE'], endpoints);
    expect(r.errors).toHaveLength(1);
    expect(r.errors[0].index).toBe(1);
    expect(r.errors[0].message).toMatch(/precede/i);
    expect(r.errors[0].message).toMatch(/UDP/);
    expect(r.errors[0].message).toMatch(/TCP-only/);
  });

  it('flags every WireGuard hop after a proxy hop', () => {
    const r = validateRoute(['WG-SE', 'GatewayProxy', 'WG-US'], endpoints);
    expect(r.errors.map((e) => e.index)).toEqual([2]);
  });

  it('rejects 5 hops with a max-4 error (route-level, no index)', () => {
    const r = validateRoute(['WG-SE', 'WG-US', 'WG-SE', 'GatewayProxy', 'OtherProxy'], endpoints);
    expect(MAX_ROUTE_HOPS).toBe(4);
    const maxErr = r.errors.filter((e) => e.index === undefined);
    expect(maxErr).toHaveLength(1);
    expect(maxErr[0].message).toMatch(/5 hops/);
    expect(maxErr[0].message).toMatch(/maximum is 4/);
  });

  it('rejects an unknown endpoint id', () => {
    const r = validateRoute(['WG-SE', 'nope'], endpoints);
    expect(r.errors).toEqual([{ index: 1, message: expect.stringMatching(/unknown endpoint "nope"/) }]);
  });

  it('does not report ordering errors against unknown hops', () => {
    const r = validateRoute(['nope', 'WG-SE'], endpoints);
    expect(r.errors.map((e) => e.index)).toEqual([0]);
  });
});

describe('validateRoute — proxy without WireGuard before it', () => {
  for (const route of [['GatewayProxy'], ['GatewayProxy', 'OtherProxy']]) {
    it(`warns about cleartext credentials for [${route.join(', ')}] and keeps errors empty`, () => {
      const r = validateRoute(route, endpoints);
      expect(r.errors).toEqual([]);
      expect(r.warnings).toHaveLength(1);
      expect(r.warnings[0]).toMatch(/cleartext/i);
      expect(r.warnings[0]).toMatch(/credentials/i);
    });
  }
  for (const route of [['WG-SE', 'GatewayProxy'], ['WG-SE']]) {
    it(`does not warn for [${route.join(', ')}]`, () => {
      expect(validateRoute(route, endpoints).warnings).toEqual([]);
    });
  }
});

describe('validateRoute — empty route', () => {
  it('warns (not errors) that traffic goes direct', () => {
    const r = validateRoute([], endpoints);
    expect(r.errors).toEqual([]);
    expect(r.warnings).toHaveLength(1);
    expect(r.warnings[0]).toMatch(/direct/i);
  });
});
