import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import { restartAll, restartConfirmText, restartSummary } from './restartAll';

const slots = [
  { id: 4, name: 'd', configured: false, enabled: false },
  { id: 3, name: 'Three', configured: true, enabled: true },
  { id: 2, name: 'b', configured: true, enabled: false },
  { id: 1, name: 'One', configured: true, enabled: true },
];

type Fail = Record<string, string>;
function fake(list: unknown[] = slots, fail: Fail = {}) {
  const calls: string[] = [];
  const request = async <T>(method: string, path: string): Promise<T> => {
    calls.push(`${method} ${path}`);
    if (fail[path]) throw new ApiError(500, fail[path]);
    if (method === 'GET') return { slots: list } as T;
    return { path } as T;
  };
  return { calls, request };
}

describe('restartAll', () => {
  it('stops then starts each enabled slot in ascending order', async () => {
    const f = fake();
    const bodies: unknown[] = [];
    const r = await restartAll(f.request, (b) => bodies.push(b));
    expect(f.calls).toEqual([
      'GET /api/slots',
      'POST /api/slots/1/stop',
      'POST /api/slots/1/start',
      'POST /api/slots/3/stop',
      'POST /api/slots/3/start',
    ]);
    expect(r).toEqual({ restarted: [1, 3], failed: [] });
    expect(bodies).toHaveLength(4);
  });

  it('skips start after a failed stop and continues', async () => {
    const f = fake(slots, { '/api/slots/1/stop': 'nope' });
    const r = await restartAll(f.request);
    expect(f.calls).not.toContain('POST /api/slots/1/start');
    expect(f.calls).toContain('POST /api/slots/3/start');
    expect(r.restarted).toEqual([3]);
    expect(r.failed[0]).toEqual({ id: 1, name: 'One', message: 'stop failed: nope' });
  });

  it('reports a failed start', async () => {
    const f = fake(slots, { '/api/slots/3/start': 'boom' });
    const r = await restartAll(f.request);
    expect(r.restarted).toEqual([1]);
    expect(r.failed[0].message).toBe('start failed: boom (slot left stopped and disabled)');
    expect(r.failed[0].id).toBe(3);
  });

  it('uses a generic message for non-ApiError failures', async () => {
    const calls: string[] = [];
    const request = async <T>(method: string, path: string): Promise<T> => {
      calls.push(path);
      if (method === 'GET') return { slots } as T;
      throw new Error('secret detail');
    };
    const r = await restartAll(request);
    expect(r.failed[0].message).toBe('stop failed: request failed');
  });

  it('is strictly sequential', async () => {
    const calls: string[] = [];
    let release!: () => void;
    const gate = new Promise<void>((res) => (release = res));
    const request = async <T>(method: string, path: string): Promise<T> => {
      calls.push(`${method} ${path}`);
      if (method === 'GET') return { slots } as T;
      if (path === '/api/slots/1/start') await gate;
      return {} as T;
    };
    const p = restartAll(request);
    for (let i = 0; i < 20; i++) await Promise.resolve();
    expect(calls).toEqual(['GET /api/slots', 'POST /api/slots/1/stop', 'POST /api/slots/1/start']);
    release();
    await p;
    expect(calls).toHaveLength(5);
  });

  it('snapshots ids before acting (stop may clear enabled)', async () => {
    let n = 0;
    const calls: string[] = [];
    const request = async <T>(method: string, path: string): Promise<T> => {
      calls.push(path);
      n++;
      return (method === 'GET' ? { slots } : {}) as T;
    };
    await restartAll(request);
    expect(n).toBe(5); // exactly one GET
  });

  it('handles an empty list', async () => {
    expect((await restartAll(fake([]).request)).restarted).toEqual([]);
  });

  it('handles a response with no slots list', async () => {
    const calls: string[] = [];
    const request = async <T>(method: string, path: string): Promise<T> => {
      calls.push(`${method} ${path}`);
      return {} as T;
    };
    expect(await restartAll(request)).toEqual({ restarted: [], failed: [] });
    expect(calls).toEqual(['GET /api/slots']);
  });

  it('rejects when the list fails', async () => {
    await expect(restartAll(fake(slots, { '/api/slots': 'down' }).request)).rejects.toThrow();
  });
});

describe('text', () => {
  it('summary', () => {
    expect(restartSummary({ restarted: [], failed: [] })).toBe('No enabled slots to restart.');
    expect(restartSummary({ restarted: [1], failed: [] })).toBe('Restarted 1 slot.');
    expect(restartSummary({ restarted: [1, 2], failed: [] })).toBe('Restarted 2 slots.');
  });
  it('confirm', () => {
    expect(restartConfirmText(1)).toBe('Restart 1 enabled slot? Open connections through them will drop.');
    expect(restartConfirmText(3)).toBe('Restart 3 enabled slots? Open connections through them will drop.');
  });
});
