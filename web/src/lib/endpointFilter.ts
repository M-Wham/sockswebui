// Endpoint list shapes (GET /api/endpoints) and the WireGuard country filter.
// Responses carry only *_set flags for secrets, never the secrets themselves.

export type EndpointKind = 'wireguard' | 'socks5' | 'http';

export interface EndpointView {
  id: string;
  name: string;
  kind: EndpointKind;
  // wireguard
  private_key_set?: boolean;
  preshared_key_set?: boolean;
  addresses?: string[];
  dns?: string[];
  peer_public_key?: string;
  allowed_ips?: string[];
  mtu?: number;
  secrets_omitted?: string[];
  endpoint?: string;
  mullvad_relay?: string;
  country?: string;
  // socks5 / http
  host?: string;
  port?: number;
  username?: string;
  password_set?: boolean;
  password_has_session?: boolean;
  session?: Session;
}

export type SessionCharset = 'alnum' | 'digits';

/** Rotating-session settings. current_id is server-owned and read-only. */
export interface Session {
  charset: SessionCharset;
  length: number;
  current_id?: string;
}

export const ALL_COUNTRIES = 'all';
export const UNKNOWN_COUNTRY = 'unknown';

function countryOf(e: EndpointView): string {
  const c = (e.country ?? '').trim().toLowerCase();
  return c === '' ? UNKNOWN_COUNTRY : c;
}

export function isWireGuard(e: EndpointView): boolean {
  return e.kind === 'wireguard';
}

/** WireGuard endpoints matching filter: "all", "unknown" (no country) or a country code. */
export function filterByCountry(eps: readonly EndpointView[], filter: string): EndpointView[] {
  const want = filter.trim().toLowerCase();
  return eps.filter((e) => isWireGuard(e) && (want === ALL_COUNTRIES || countryOf(e) === want));
}

/** Distinct WG countries, sorted, with "unknown" last when any endpoint lacks one. */
export function countryOptions(eps: readonly EndpointView[]): string[] {
  const set = new Set(eps.filter(isWireGuard).map(countryOf));
  const hasUnknown = set.delete(UNKNOWN_COUNTRY);
  const out = [...set].sort();
  if (hasUnknown) out.push(UNKNOWN_COUNTRY);
  return out;
}
