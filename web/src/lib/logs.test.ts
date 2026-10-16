import { describe, expect, it, vi } from 'vitest';
import { LOG_CAP, LOG_SLOTS, loadLogs, logLines, logPath } from './logs';

describe('logs', () => {
  it('offers slots 1-4 and builds the path per slot', () => {
    expect(LOG_SLOTS).toEqual([1, 2, 3, 4]);
    expect(logPath(3)).toBe('/api/slots/3/logs');
  });

  it('fetches the selected slot via GET', async () => {
    const request = vi.fn().mockResolvedValue({ slot: 2, lines: [{ ts: 't', level: 'INFO', msg: 'm', text: 'line one' }] });
    expect(await loadLogs(request, 2)).toEqual(['line one']);
    expect(request).toHaveBeenCalledWith('GET', '/api/slots/2/logs');
  });

  it('rejects slot ids outside 1..4 without a request', async () => {
    const request = vi.fn();
    await expect(loadLogs(request, 5)).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
  });

  it('keeps order (newest last) and caps at the newest 500', () => {
    const lines = Array.from({ length: 520 }, (_, i) => ({ text: `l${i}` }));
    const out = logLines({ lines });
    expect(out).toHaveLength(LOG_CAP);
    expect(out[0]).toBe('l20');
    expect(out[out.length - 1]).toBe('l519');
  });

  it('falls back to msg, and tolerates a malformed body', () => {
    expect(logLines({ lines: [{ msg: 'only msg' }, 7, null, { text: 5 }] })).toEqual(['only msg']);
    expect(logLines(undefined)).toEqual([]);
    expect(logLines({ lines: 'x' })).toEqual([]);
  });
});
