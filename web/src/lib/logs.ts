// Per-slot log viewer data. Lines are plain text only; the server
// has already redacted secrets. Oldest first, so the newest line is last.

export const LOG_SLOTS = [1, 2, 3, 4] as const;
export const LOG_CAP = 500;

export type RequestFn = <T = unknown>(method: 'GET', path: string) => Promise<T>;

export function logPath(id: number): string {
  return `/api/slots/${id}/logs`;
}

/** Extracts displayable lines from the response body, keeping only the newest LOG_CAP. */
export function logLines(body: unknown): string[] {
  const raw = (body as { lines?: unknown } | undefined)?.lines;
  if (!Array.isArray(raw)) return [];
  const out: string[] = [];
  for (const l of raw) {
    if (typeof l !== 'object' || l === null) continue;
    const { text, msg } = l as { text?: unknown; msg?: unknown };
    if (typeof text === 'string') out.push(text);
    else if (typeof msg === 'string') out.push(msg);
  }
  return out.slice(-LOG_CAP);
}

export async function loadLogs(request: RequestFn, id: number): Promise<string[]> {
  if (!(LOG_SLOTS as readonly number[]).includes(id)) throw new Error('slot must be 1-4');
  return logLines(await request('GET', logPath(id)));
}
