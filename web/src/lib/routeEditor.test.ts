import { describe, expect, it } from 'vitest';
import {
  add,
  drop,
  hopErrors,
  library,
  MAX_HOPS_MESSAGE,
  moveDown,
  moveUp,
  remove,
  type LibraryEndpoint,
} from './routeEditor';
import { EMPTY_ROUTE_WARNING } from './routeRules';

const eps: LibraryEndpoint[] = [
  { id: 'p1', name: 'GatewayProxy', kind: 'socks5' },
  { id: 'wg1', name: 'rigWG', kind: 'wireguard' },
  { id: 'p2', name: 'OtherProxy', kind: 'http' },
  { id: 'wg2', name: 'WG-US', kind: 'wireguard' },
];

describe('library', () => {
  it('lists every endpoint with WireGuard first, keeping order within a kind', () => {
    expect(library(eps).map((e) => e.id)).toEqual(['wg1', 'wg2', 'p1', 'p2']);
  });
});

describe('add', () => {
  it('appends the hop and re-validates', () => {
    const r = add(['wg1'], 'p1', eps);
    expect(r.route).toEqual(['wg1', 'p1']);
    expect(r.refused).toBeUndefined();
    expect(r.validation.errors).toEqual([]);
    expect(r.announce).toMatch(/GatewayProxy/);
  });

  it('refuses a 5th hop with the max-4 message and leaves the route unchanged', () => {
    const start = ['wg1', 'wg2', 'p1', 'p2'];
    const r = add(start, 'p1', eps);
    expect(r.route).toEqual(start);
    expect(r.refused).toBe(MAX_HOPS_MESSAGE);
    expect(MAX_HOPS_MESSAGE).toMatch(/4 hops/);
    expect(r.announce).toBe(MAX_HOPS_MESSAGE);
  });

  it('does not mutate its input', () => {
    const start = ['wg1'];
    add(start, 'p1', eps);
    expect(start).toEqual(['wg1']);
  });

  it('flags [proxy, wg] on the WireGuard hop index', () => {
    const r = add(['p1'], 'wg1', eps);
    expect(r.route).toEqual(['p1', 'wg1']);
    expect(r.validation.errors).toHaveLength(1);
    expect(r.validation.errors[0].index).toBe(1);
    expect(hopErrors(r.validation).get(1)?.[0]).toMatch(/must precede proxy hops/);
    expect(hopErrors(r.validation).has(0)).toBe(false);
  });
});

describe('remove', () => {
  it('removes the hop at i', () => {
    const r = remove(['wg1', 'p1', 'p2'], 1, eps);
    expect(r.route).toEqual(['wg1', 'p2']);
    expect(r.announce).toMatch(/GatewayProxy/);
  });

  it('removing the last hop shows the direct (no tunnel) warning', () => {
    const r = remove(['wg1'], 0, eps);
    expect(r.route).toEqual([]);
    expect(r.validation.warnings).toEqual([EMPTY_ROUTE_WARNING]);
    expect(EMPTY_ROUTE_WARNING).toMatch(/direct/);
    expect(EMPTY_ROUTE_WARNING).toMatch(/no VPN/);
  });

  it('ignores an out-of-range index', () => {
    expect(remove(['wg1'], 3, eps).route).toEqual(['wg1']);
  });
});

describe('moveUp / moveDown', () => {
  it('moveUp swaps with the previous hop and re-validates', () => {
    const r = moveUp(['p1', 'wg1'], 1, eps);
    expect(r.route).toEqual(['wg1', 'p1']);
    expect(r.validation.errors).toEqual([]);
    expect(r.announce).toMatch(/rigWG.*position 1/);
  });

  it('moveDown swaps with the next hop and re-validates', () => {
    const r = moveDown(['wg1', 'p1'], 0, eps);
    expect(r.route).toEqual(['p1', 'wg1']);
    expect(r.validation.errors.map((e) => e.index)).toEqual([1]);
  });

  it('is a no-op at the edges', () => {
    expect(moveUp(['wg1', 'p1'], 0, eps).route).toEqual(['wg1', 'p1']);
    expect(moveDown(['wg1', 'p1'], 1, eps).route).toEqual(['wg1', 'p1']);
  });
});

describe('drop', () => {
  it('inserts a library endpoint at the drop index', () => {
    const r = drop(['wg1', 'p2'], 'p1', 1, eps);
    expect(r.route).toEqual(['wg1', 'p1', 'p2']);
  });

  it('clamps the index to the end of the route', () => {
    expect(drop(['wg1'], 'p1', 9, eps).route).toEqual(['wg1', 'p1']);
    expect(drop(['p1'], 'wg1', -2, eps).route).toEqual(['wg1', 'p1']);
  });

  it('refuses a 5th hop from the library', () => {
    const start = ['wg1', 'wg2', 'p1', 'p2'];
    const r = drop(start, 'p1', 0, eps);
    expect(r.route).toEqual(start);
    expect(r.refused).toBe(MAX_HOPS_MESSAGE);
  });

  it('reorders an existing hop when the source index is given', () => {
    const r = drop(['p1', 'p2', 'wg1'], 'wg1', 0, eps, 2);
    expect(r.route).toEqual(['wg1', 'p1', 'p2']);
    expect(r.validation.errors).toEqual([]);
  });

  it('a reorder of a full route is allowed', () => {
    const r = drop(['p1', 'wg1', 'wg2', 'p2'], 'p1', 3, eps, 0);
    expect(r.refused).toBeUndefined();
    expect(r.route).toEqual(['wg1', 'wg2', 'p2', 'p1']);
  });

  it('[proxy, wg] after a drop shows the error on the WG hop', () => {
    const r = drop(['p1'], 'wg1', 1, eps);
    expect(hopErrors(r.validation).get(1)?.[0]).toMatch(/WireGuard/);
  });
});
