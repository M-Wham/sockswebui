// Text helpers for the sidebar footer. Render only what the API reported.

export function versionLabel(v: unknown): string {
  if (typeof v !== 'string' || v === '') return 'version unknown';
  if (v === 'dev' || v.startsWith('v')) return v;
  return `v${v}`;
}

export function serviceLine(ok: boolean | null): string {
  if (ok === null) return 'Service: checking…';
  return ok ? 'Service: running' : 'Service: unreachable';
}
