// Restart every enabled slot, strictly one at a time, so at most one slot is
// down at any moment. Fail closed: a slot whose start fails stays stopped.
import { ApiError } from './api';

type Request = <T>(method: 'GET' | 'POST', path: string) => Promise<T>;

export interface RestartResult {
  restarted: number[];
  failed: { id: number; name: string; message: string }[];
}

interface SlotLite {
  id: number;
  name?: string;
  configured?: boolean;
  enabled?: boolean;
}

const msg = (e: unknown): string => (e instanceof ApiError ? e.message : 'request failed');

export async function restartAll(request: Request, onStatus?: (body: unknown) => void): Promise<RestartResult> {
  const out = await request<{ slots?: SlotLite[] }>('GET', '/api/slots');
  // Snapshot before acting: stopping may clear `enabled` server-side.
  const targets = (out?.slots ?? [])
    .filter((s) => s.configured && s.enabled)
    .map((s) => ({ id: s.id, name: s.name ?? '' }))
    .sort((a, b) => a.id - b.id);
  const result: RestartResult = { restarted: [], failed: [] };
  for (const t of targets) {
    // Await into a variable first: `onStatus?.(await ...)` skips the call when onStatus is undefined.
    try {
      const body = await request('POST', `/api/slots/${t.id}/stop`);
      onStatus?.(body);
    } catch (e) {
      result.failed.push({ ...t, message: `stop failed: ${msg(e)}` });
      continue;
    }
    try {
      const body = await request('POST', `/api/slots/${t.id}/start`);
      onStatus?.(body);
      result.restarted.push(t.id);
    } catch (e) {
      result.failed.push({ ...t, message: `start failed: ${msg(e)} (slot left stopped and disabled)` });
    }
  }
  return result;
}

export function restartSummary(r: RestartResult): string {
  const n = r.restarted.length;
  if (n === 0) return 'No enabled slots to restart.';
  return n === 1 ? 'Restarted 1 slot.' : `Restarted ${n} slots.`;
}

export function restartConfirmText(n: number): string {
  return `Restart ${n} enabled slot${n === 1 ? '' : 's'}? Open connections through them will drop.`;
}
