// Pure helpers for the Slots screen endpoint summary.

import type { EndpointView } from './endpointFilter';
import { nameOf } from './routeEditor';
import type { SlotConfig } from './slotsStore';

export function routeSummary(route: readonly string[], eps: readonly EndpointView[]): string {
  if (route.length === 0) return 'direct';
  return route.map((id) => nameOf(id, eps)).join(' › ');
}

export function endpointTypeLabel(e: EndpointView): string {
  if (e.kind === 'wireguard') return e.mullvad_relay ? 'Mullvad WireGuard' : 'WireGuard';
  return e.kind === 'socks5' ? 'Upstream SOCKS5' : 'Upstream HTTP';
}

export function endpointLocation(e: EndpointView): string {
  if (e.kind === 'wireguard') return (e.country ?? '').trim().toUpperCase() || '—';
  return `${e.host ?? ''}:${e.port ?? ''}`;
}

export function usedBy(id: string, configs: readonly SlotConfig[]): number[] {
  return configs
    .filter((c) => c.configured && c.route.includes(id))
    .map((c) => c.id)
    .sort((a, b) => a - b);
}

export function summaryRows(
  eps: readonly EndpointView[],
  configs: readonly SlotConfig[],
  limit = 8,
): { rows: EndpointView[]; total: number } {
  const byName = (a: EndpointView, b: EndpointView) => (a.name || a.id).localeCompare(b.name || b.id);
  const used = new Set<string>();
  for (const c of configs) if (c.configured) for (const id of c.route) used.add(id);
  const first = eps.filter((e) => used.has(e.id)).sort(byName);
  const rest = eps.filter((e) => !used.has(e.id)).sort(byName);
  return { rows: [...first, ...rest].slice(0, limit), total: eps.length };
}
