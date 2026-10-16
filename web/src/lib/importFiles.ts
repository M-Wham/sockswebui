// Client side of POST /api/endpoints/import: only .conf/.zip files are sent,
// all in one multipart request (field "files"); the 5 MB request limit is checked first.

export const MAX_IMPORT_BYTES = 5 * 1024 * 1024;

export type ImportStatus = 'imported' | 'skipped-duplicate' | 'error';

export interface ImportRow {
  file: string;
  status: ImportStatus;
  reason: string;
  warnings: string[];
  id?: string;
}

export interface ImportPlan {
  accepted: File[];
  rejected: ImportRow[];
  /** Non-empty when the accepted files exceed the request limit. */
  tooLarge: string;
}

export interface ImportOutcome {
  rows: ImportRow[];
  message: string;
}

const STATUSES: readonly string[] = ['imported', 'skipped-duplicate', 'error'];

export function isSupportedImport(name: string): boolean {
  return /\.(conf|zip)$/i.test(name);
}

export function planImport(files: readonly File[]): ImportPlan {
  const accepted: File[] = [];
  const rejected: ImportRow[] = [];
  for (const f of files) {
    if (isSupportedImport(f.name)) accepted.push(f);
    else rejected.push({ file: f.name, status: 'error', reason: 'unsupported file type', warnings: [] });
  }
  const total = accepted.reduce((n, f) => n + f.size, 0);
  const tooLarge =
    total > MAX_IMPORT_BYTES
      ? `These files total ${(total / 1048576).toFixed(1)} MB; one upload may be at most 5 MB. Import them in smaller batches.`
      : '';
  return { accepted, rejected, tooLarge };
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string') : [];
}

/** Maps the server's per-file results to table rows, dropping malformed entries. */
export function toRows(resp: unknown): ImportRow[] {
  const results = (resp as { results?: unknown } | undefined)?.results;
  if (!Array.isArray(results)) return [];
  const rows: ImportRow[] = [];
  for (const r of results) {
    if (typeof r !== 'object' || r === null) continue;
    const o = r as Record<string, unknown>;
    if (typeof o.file !== 'string' || typeof o.status !== 'string' || !STATUSES.includes(o.status)) continue;
    const row: ImportRow = {
      file: o.file,
      status: o.status as ImportStatus,
      reason: typeof o.reason === 'string' ? o.reason : '',
      warnings: strings(o.warnings),
    };
    if (typeof o.id === 'string' && o.id !== '') row.id = o.id;
    rows.push(row);
  }
  return rows;
}

/** Plans, uploads (once) and merges server results with client-side rejections. */
export async function runImport(
  files: readonly File[],
  send: (form: FormData) => Promise<unknown>,
): Promise<ImportOutcome> {
  const plan = planImport(files);
  if (plan.tooLarge) return { rows: [], message: plan.tooLarge };
  if (plan.accepted.length === 0) return { rows: plan.rejected, message: '' };
  const form = new FormData();
  for (const f of plan.accepted) form.append('files', f, f.name);
  const resp = await send(form);
  return { rows: [...toRows(resp), ...plan.rejected], message: '' };
}
