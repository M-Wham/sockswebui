// Pure route-builder operations. Every op returns a new route, the
// re-run validateRoute result and a sentence for the aria-live region. Inputs are never
// mutated. The server stays the authority; this is instant feedback only.

import { MAX_ROUTE_HOPS, validateRoute, type RouteEndpoint, type RouteValidation } from './routeRules';

export interface LibraryEndpoint extends RouteEndpoint {
  name?: string;
}

export interface EditResult {
  route: string[];
  validation: RouteValidation;
  /** Set when the op was refused (route unchanged). */
  refused?: string;
  /** Screen-reader announcement describing what happened. */
  announce: string;
}

export const MAX_HOPS_MESSAGE = `A route can have at most ${MAX_ROUTE_HOPS} hops. Remove one before adding another.`;

/** All endpoints, WireGuard first, original order kept within each group. */
export function library<T extends LibraryEndpoint>(eps: readonly T[]): T[] {
  return [...eps.filter((e) => e.kind === 'wireguard'), ...eps.filter((e) => e.kind !== 'wireguard')];
}

/** Hop index → messages, for rendering errors next to each hop. */
export function hopErrors(v: RouteValidation): Map<number, string[]> {
  const out = new Map<number, string[]>();
  for (const e of v.errors) {
    if (e.index === undefined) continue;
    out.set(e.index, [...(out.get(e.index) ?? []), e.message]);
  }
  return out;
}

/** Route-level errors (not tied to one hop). */
export function routeLevelErrors(v: RouteValidation): string[] {
  return v.errors.filter((e) => e.index === undefined).map((e) => e.message);
}

export function nameOf(id: string, eps: readonly LibraryEndpoint[]): string {
  return eps.find((e) => e.id === id)?.name || id;
}

function result(route: string[], eps: readonly LibraryEndpoint[], announce: string): EditResult {
  return { route, validation: validateRoute(route, eps as RouteEndpoint[]), announce };
}

function refuse(route: readonly string[], eps: readonly LibraryEndpoint[]): EditResult {
  return { ...result([...route], eps, MAX_HOPS_MESSAGE), refused: MAX_HOPS_MESSAGE };
}

function clamp(i: number, max: number): number {
  return Math.max(0, Math.min(Math.trunc(i), max));
}

export function add(route: readonly string[], id: string, eps: readonly LibraryEndpoint[]): EditResult {
  if (route.length >= MAX_ROUTE_HOPS) return refuse(route, eps);
  const next = [...route, id];
  return result(next, eps, `Added ${nameOf(id, eps)} as hop ${next.length} of ${next.length}.`);
}

export function remove(route: readonly string[], i: number, eps: readonly LibraryEndpoint[]): EditResult {
  if (i < 0 || i >= route.length) return result([...route], eps, '');
  const next = route.filter((_, j) => j !== i);
  const tail = next.length === 0 ? ' The route is now empty.' : '';
  return result(next, eps, `Removed ${nameOf(route[i], eps)}.${tail}`);
}

function move(route: readonly string[], from: number, to: number, eps: readonly LibraryEndpoint[]): EditResult {
  if (from < 0 || from >= route.length || to < 0 || to >= route.length || from === to) {
    return result([...route], eps, '');
  }
  const next = [...route];
  const [id] = next.splice(from, 1);
  next.splice(to, 0, id);
  return result(next, eps, `Moved ${nameOf(id, eps)} to position ${to + 1} of ${next.length}.`);
}

export function moveUp(route: readonly string[], i: number, eps: readonly LibraryEndpoint[]): EditResult {
  return move(route, i, i - 1, eps);
}

export function moveDown(route: readonly string[], i: number, eps: readonly LibraryEndpoint[]): EditResult {
  return move(route, i, i + 1, eps);
}

/**
 * Drop endpoint `id` at `index`. Without `from` it comes from the library (inserted, refused
 * when the route is full); with `from` it is a reorder of the hop at that index.
 */
export function drop(
  route: readonly string[],
  id: string,
  index: number,
  eps: readonly LibraryEndpoint[],
  from?: number,
): EditResult {
  if (from !== undefined && route[from] === id) {
    return move(route, from, clamp(index, route.length - 1), eps);
  }
  if (route.length >= MAX_ROUTE_HOPS) return refuse(route, eps);
  const at = clamp(index, route.length);
  const next = [...route];
  next.splice(at, 0, id);
  return result(next, eps, `Added ${nameOf(id, eps)} as hop ${at + 1} of ${next.length}.`);
}
