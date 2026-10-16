// Client-side mirror of the server route rules (internal/config/route.go is the authority).
// Used by the route builder for instant inline feedback; the server's 400 field errors still win.

export type EndpointKind = 'wireguard' | 'socks5' | 'http';

export interface RouteEndpoint {
  id: string;
  kind: EndpointKind | string;
}

export interface RouteError {
  /** Hop index the error refers to; undefined for route-level errors (e.g. too many hops). */
  index?: number;
  message: string;
}

export interface RouteValidation {
  errors: RouteError[];
  warnings: string[];
}

export const MAX_ROUTE_HOPS = 4;

export const EMPTY_ROUTE_WARNING =
  'Route is empty: this slot connects directly through the device, with no VPN or proxy.';

export const PROXY_FIRST_WARNING =
  'A proxy is the first hop: its address and any credentials cross the device network in cleartext. Put a WireGuard hop first to protect them.';

export function validateRoute(route: string[], endpoints: RouteEndpoint[]): RouteValidation {
  const errors: RouteError[] = [];
  const warnings: string[] = [];

  if (route.length === 0) {
    warnings.push(EMPTY_ROUTE_WARNING);
    return { errors, warnings };
  }
  if (route.length > MAX_ROUTE_HOPS) {
    errors.push({
      message: `route has ${route.length} hops; the maximum is ${MAX_ROUTE_HOPS}`,
    });
  }

  const kinds = new Map(endpoints.map((e) => [e.id, e.kind]));
  let seenProxy = false;
  let seenWireguard = false;
  route.forEach((id, index) => {
    const kind = kinds.get(id);
    if (kind === undefined) {
      errors.push({ index, message: `unknown endpoint "${id}"` });
      return;
    }
    if (kind === 'wireguard') {
      seenWireguard = true;
      if (seenProxy) {
        errors.push({
          index,
          message:
            `WireGuard hop "${id}" must precede proxy hops: WireGuard needs UDP ` +
            'and upstream SOCKS5/HTTP proxies are TCP-only',
        });
      }
    } else if (kind === 'socks5' || kind === 'http') {
      if (!seenProxy && !seenWireguard) warnings.push(PROXY_FIRST_WARNING);
      seenProxy = true;
    }
  });

  return { errors, warnings };
}
