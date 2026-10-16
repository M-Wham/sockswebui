import { describe, expect, it } from 'vitest';
import { countryOptions, filterByCountry, UNKNOWN_COUNTRY } from './endpointFilter';
import type { EndpointView } from './endpointFilter';

const eps: EndpointView[] = [
  { id: 'a', name: 'se-got-wg-001', kind: 'wireguard', country: 'se' },
  { id: 'b', name: 'us-nyc-wg-002', kind: 'wireguard', country: 'us' },
  { id: 'c', name: 'home', kind: 'wireguard' },
  { id: 'd', name: 'gatewayproxy', kind: 'socks5', host: '1.2.3.4', port: 1080 },
  { id: 'e', name: 'se-sto-wg-003', kind: 'wireguard', country: 'SE' },
  { id: 'f', name: 'blank', kind: 'wireguard', country: '' },
  { id: 'g', name: 'web', kind: 'http', host: 'h', port: 3128 },
];

const ids = (xs: EndpointView[]) => xs.map((e) => e.id);

describe('filterByCountry', () => {
  it('"all" returns every WireGuard endpoint and no proxies', () => {
    expect(ids(filterByCountry(eps, 'all'))).toEqual(['a', 'b', 'c', 'e', 'f']);
  });
  it('a country returns only WG endpoints with that country (case-insensitive)', () => {
    expect(ids(filterByCountry(eps, 'se'))).toEqual(['a', 'e']);
    expect(ids(filterByCountry(eps, 'us'))).toEqual(['b']);
    expect(ids(filterByCountry(eps, 'de'))).toEqual([]);
  });
  it('endpoints without a country appear under "unknown"', () => {
    expect(ids(filterByCountry(eps, UNKNOWN_COUNTRY))).toEqual(['c', 'f']);
  });
});

describe('countryOptions', () => {
  it('lists distinct lower-case WG countries sorted, then unknown when present', () => {
    expect(countryOptions(eps)).toEqual(['se', 'us', UNKNOWN_COUNTRY]);
    expect(countryOptions(eps.filter((e) => e.country))).toEqual(['se', 'us']);
    expect(countryOptions([])).toEqual([]);
  });
});
