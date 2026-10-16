// Config export/import helpers. Pure, no network, no storage: passphrase and
// password values exist only in the dialog's local form object and are wiped after use.

import { hrefFor } from './router';
import type { SlotConfig } from './slotsStore';

export type ExportMode = 'encrypted' | 'no-secrets';
export const PASSPHRASE_MIN = 12;
export const PASSPHRASE_MAX = 1024;
export const FALLBACK_FILENAME = 'sockswebui-config.json';

export interface TransferForm {
  mode: ExportMode;
  passphrase: string;
  confirm: string;
  current_password: string;
}

export interface ExportRequest {
  current_password: string;
  mode: ExportMode;
  passphrase?: string;
}

export interface ImportConfigResult {
  endpoints?: number;
  slots?: number;
  backup?: string;
  secrets_missing?: { endpoints?: string[]; slots?: number[] };
}

export const blankTransferForm = (): TransferForm => ({
  mode: 'encrypted',
  passphrase: '',
  confirm: '',
  current_password: '',
});

export function clearTransferForm(f: TransferForm): void {
  f.passphrase = '';
  f.confirm = '';
  f.current_password = '';
}

export const utf8Length = (s: string): number => new TextEncoder().encode(s).length;

export type ExportBuild =
  | { ok: true; body: ExportRequest }
  | { ok: false; errors: Record<string, string> };

export function buildExportRequest(f: TransferForm): ExportBuild {
  const errors: Record<string, string> = {};
  if (f.current_password === '') errors.current_password = 'Enter your current admin password.';
  if (f.mode === 'encrypted') {
    const n = utf8Length(f.passphrase);
    if (n < PASSPHRASE_MIN || n > PASSPHRASE_MAX) {
      errors.passphrase = `Use ${PASSPHRASE_MIN} to ${PASSPHRASE_MAX} bytes (non-ASCII characters take more than one).`;
    }
    if (f.confirm !== f.passphrase) errors.confirm = 'The passphrases do not match.';
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  const body: ExportRequest = { current_password: f.current_password, mode: f.mode };
  if (f.mode === 'encrypted') body.passphrase = f.passphrase;
  return { ok: true, body };
}

/** Reads `mode` from the file content; the file name is never consulted. */
export function parseExportMode(text: string): { mode: ExportMode } | { error: string } {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return { error: 'This file is not valid JSON, so it is not a sockswebui export.' };
  }
  const mode = typeof v === 'object' && v !== null ? (v as Record<string, unknown>).mode : undefined;
  if (mode === 'encrypted' || mode === 'no-secrets') return { mode };
  return { error: 'This file has no recognised export mode (expected "encrypted" or "no-secrets").' };
}

export function filenameFromDisposition(header: string | null): string {
  const m = header?.match(/filename="([^"]*)"/i) ?? header?.match(/filename=([^;\s]+)/i);
  const name = m?.[1] ?? '';
  return /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name) && !name.includes('..') ? name : FALLBACK_FILENAME;
}

export interface ImportView {
  endpoints: number;
  slots: number;
  backup: string;
  /** Slot ids "Start all" will start, in order. */
  startable: number[];
  missingEndpoints: { id: string; href: string }[];
  /** `via` lists missing-secret route endpoints (empty when the slot's own secrets are missing). */
  missingSlots: { id: number; href: string; via: string[] }[];
}

export function importResultView(r: ImportConfigResult, slots: readonly SlotConfig[]): ImportView {
  const mEnd = new Set(r.secrets_missing?.endpoints ?? []);
  const mSlot = new Set(r.secrets_missing?.slots ?? []);
  const missingSlots: ImportView['missingSlots'] = [];
  const startable: number[] = [];
  for (const s of slots) {
    const via = s.route.filter((id) => mEnd.has(id));
    if (mSlot.has(s.id) || via.length > 0) {
      missingSlots.push({ id: s.id, href: hrefFor({ name: 'slot', id: s.id }), via });
    } else if (s.configured && s.route.length > 0) {
      startable.push(s.id);
    }
  }
  return {
    endpoints: r.endpoints ?? 0,
    slots: r.slots ?? 0,
    backup: r.backup ?? '',
    startable,
    missingEndpoints: [...mEnd].map((id) => ({ id, href: `#/endpoints/${encodeURIComponent(id)}` })),
    missingSlots,
  };
}

export interface DownloadDeps {
  createObjectURL: (b: Blob) => string;
  revokeObjectURL: (u: string) => void;
  createAnchor: () => HTMLAnchorElement;
  append: (a: HTMLAnchorElement) => void;
  schedule: (fn: () => void) => void;
}

const browserDeps = (): DownloadDeps => ({
  createObjectURL: (b) => URL.createObjectURL(b),
  revokeObjectURL: (u) => URL.revokeObjectURL(u),
  createAnchor: () => document.createElement('a'),
  append: (a) => document.body.appendChild(a),
  schedule: (fn) => void setTimeout(fn, 0),
});

export function downloadBlob(blob: Blob, filename: string, deps: DownloadDeps = browserDeps()): void {
  const url = deps.createObjectURL(blob);
  const a = deps.createAnchor();
  a.href = url;
  a.download = filename;
  deps.append(a);
  a.click();
  a.remove();
  deps.schedule(() => deps.revokeObjectURL(url));
}

/** Runs a request that reads the form; the secrets are wiped afterwards whether it resolves or rejects. */
export async function withClearedForm<T>(f: TransferForm, run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } finally {
    clearTransferForm(f);
  }
}

/** Wait text for a 429 from the shared login limiter. */
export function rateLimitText(retryAfterSeconds: number | undefined): string {
  const ok = retryAfterSeconds !== undefined && Number.isFinite(retryAfterSeconds) && retryAfterSeconds >= 1;
  const n = ok ? Math.ceil(retryAfterSeconds) : 0;
  const wait = !ok ? 'a few seconds' : `${n} second${n === 1 ? '' : 's'}`;
  return `Too many attempts. Try again in ${wait}.`;
}
