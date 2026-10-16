// Hash router: the Go UI handler has no SPA fallback, so all routes live after `#/`.
// Unknown hashes resolve to the Slots screen.

export const SLOT_COUNT = 4;

export type Route =
  | { name: 'dashboard' }
  | { name: 'endpoints'; id?: string }
  | { name: 'slot'; id: number }
  | { name: 'logs' }
  | { name: 'settings' }
  | { name: 'login' }
  | { name: 'setup' }
  | { name: 'import' };

const SIMPLE = ['endpoints', 'logs', 'settings', 'login', 'setup', 'import'] as const;

export function parseRoute(hash: string): Route {
  const path = hash.replace(/^#/, '');
  const parts = path.split('/').filter((p) => p !== '');
  if (!path.startsWith('/') && path !== '') return { name: 'dashboard' };
  if (parts.length === 1) {
    const name = SIMPLE.find((n) => n === parts[0]);
    if (name) return { name };
  }
  if (parts.length === 2 && parts[0] === 'endpoints' && /^[A-Za-z0-9_.-]{1,64}$/.test(parts[1])) {
    return { name: 'endpoints', id: parts[1] };
  }
  if (parts.length === 2 && parts[0] === 'slots' && /^[1-9]$/.test(parts[1])) {
    const id = Number(parts[1]);
    if (id <= SLOT_COUNT) return { name: 'slot', id };
  }
  return { name: 'dashboard' };
}

export function hrefFor(r: Route): string {
  switch (r.name) {
    case 'dashboard':
      return '#/';
    case 'slot':
      return `#/slots/${r.id}`;
    default:
      return `#/${r.name}`;
  }
}

/** Navigates by setting location.hash (no-op outside a browser). */
export function navigate(hash: string): void {
  if (typeof location !== 'undefined' && location.hash !== hash) location.hash = hash;
}
