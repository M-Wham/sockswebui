import { describe, expect, it } from 'vitest';
import { endpointLocation, endpointTypeLabel, routeSummary, summaryRows, usedBy } from './endpointSummary';
import type { EndpointView } from './endpointFilter';
import type { SlotConfig } from './slotsStore';

const ep = (o: Partial<EndpointView> & { id: string }): EndpointView => ({ name: o.id, kind: 'wireguard', ...o });
const cfg = (id: number, route: string[], configured = true): SlotConfig => ({
  id, configured, name: '', enabled: true, bind_addr: '', port: 0, route, killswitch: '',
});

const eps = [
  ep({ id: 'a', name: 'Alpha' }),
  ep({ id: 'b', name: 'Bravo', kind: 'socks5', host: 'h', port: 1080 }),
];

describe('routeSummary', () => {
  it('joins hop names with a separator', () => {
    expect(routeSummary(['a', 'b'], eps)).toBe('Alpha › Bravo');
  });
  it('falls back to the id for an unknown hop', () => {
    expect(routeSummary(['zz'], eps)).toBe('zz');
  });
  it('says direct for an empty route', () => {
    expect(routeSummary([], eps)).toBe('direct');
  });
});

describe('endpointTypeLabel', () => {
  it('labels all four cases', () => {
    expect(endpointTypeLabel(ep({ id: 'x', mullvad_relay: 'se-got-wg-001' }))).toBe('Mullvad WireGuard');
    expect(endpointTypeLabel(ep({ id: 'x' }))).toBe('WireGuard');
    expect(endpointTypeLabel(ep({ id: 'x', kind: 'socks5' }))).toBe('Upstream SOCKS5');
    expect(endpointTypeLabel(ep({ id: 'x', kind: 'http' }))).toBe('Upstream HTTP');
  });
});

describe('endpointLocation', () => {
  it('upper-cases the WireGuard country or shows a dash', () => {
    expect(endpointLocation(ep({ id: 'x', country: 'se' }))).toBe('SE');
    expect(endpointLocation(ep({ id: 'x' }))).toBe('—');
  });
  it('shows host:port for proxies', () => {
    expect(endpointLocation(ep({ id: 'x', kind: 'http', host: 'p.example', port: 8080 }))).toBe('p.example:8080');
  });
});

describe('usedBy', () => {
  it('lists configured slots containing the id, ascending', () => {
    const c = [cfg(3, ['a']), cfg(1, ['b', 'a']), cfg(2, ['b']), cfg(4, ['a'], false)];
    expect(usedBy('a', c)).toEqual([1, 3]);
    expect(usedBy('none', c)).toEqual([]);
  });
});

describe('summaryRows', () => {
  const many = [
    ep({ id: 'e', name: 'Echo' }),
    ep({ id: 'c', name: 'Charlie' }),
    ep({ id: 'd', name: 'Delta' }),
    ep({ id: 'a', name: 'Alpha' }),
    ep({ id: 'b', name: 'Bravo' }),
  ];
  it('puts used endpoints first, then the rest, each in name order', () => {
    const r = summaryRows(many, [cfg(1, ['e', 'c'])]);
    expect(r.rows.map((x) => x.name)).toEqual(['Charlie', 'Echo', 'Alpha', 'Bravo', 'Delta']);
    expect(r.total).toBe(5);
  });
  it('caps at the limit while total stays full', () => {
    const r = summaryRows(many, [], 2);
    expect(r.rows.map((x) => x.name)).toEqual(['Alpha', 'Bravo']);
    expect(r.total).toBe(5);
  });
  it('defaults the limit to 8', () => {
    const big = Array.from({ length: 12 }, (_, i) => ep({ id: `i${i}`, name: `n${String(i).padStart(2, '0')}` }));
    const r = summaryRows(big, []);
    expect(r.rows).toHaveLength(8);
    expect(r.total).toBe(12);
  });
});
