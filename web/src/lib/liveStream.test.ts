import { describe, expect, it, vi } from 'vitest';
import { createLiveStream } from './liveStream';
import type { SseDeps } from './sse';

function rig() {
  const opened: SseDeps[] = [];
  const closes = vi.fn();
  const live = createLiveStream((deps) => {
    opened.push(deps);
    return closes;
  });
  return { live, opened, closes };
}

const snap = { slots: [{ id: 1, state: 'UP', name: 'a' }] };

describe('createLiveStream', () => {
  it('opens one stream for any number of subscribers and closes after the last leaves', () => {
    const { live, opened, closes } = rig();
    const a = live.subscribe({});
    const b = live.subscribe({});
    expect(opened).toHaveLength(1);
    a();
    expect(closes).not.toHaveBeenCalled();
    b();
    expect(closes).toHaveBeenCalledTimes(1);
  });

  it('tracks connection: open is up, error is down, never assumed up', () => {
    const { live, opened } = rig();
    const seen: boolean[] = [];
    live.subscribe({ onChange: () => seen.push(live.connected()) });
    expect(live.connected()).toBe(false);
    opened[0].onOpen?.();
    opened[0].onError?.();
    expect(seen).toEqual([true, false]);
  });

  it('keeps reduced slot state so a late subscriber sees the latest, then follows frames', () => {
    const { live, opened } = rig();
    live.subscribe({});
    opened[0].onFrame('snapshot', snap);
    opened[0].onFrame('state', { slot: 1, state: 'DEGRADED', msg: 'x' });
    const frames: string[] = [];
    live.subscribe({ onFrame: (ev) => frames.push(ev) });
    expect(live.slots()[1].state).toBe('DEGRADED');
    opened[0].onFrame('state', { slot: 1, state: 'STOPPED', msg: '' });
    expect(frames).toEqual(['state']);
    expect(live.slots()[1].state).toBe('STOPPED');
  });

  it('fresh join reconnects once so a new snapshot arrives, keeping the old stream out', () => {
    const { live, opened, closes } = rig();
    live.subscribe({});
    opened[0].onFrame('snapshot', snap);
    live.subscribe({}, { fresh: true });
    expect(closes).toHaveBeenCalledTimes(1);
    expect(opened).toHaveLength(2);
  });

  it('fresh join before any snapshot does not open a second stream', () => {
    const { live, opened } = rig();
    live.subscribe({});
    live.subscribe({}, { fresh: true });
    expect(opened).toHaveLength(1);
  });

  it('forgets state once nobody listens', () => {
    const { live, opened } = rig();
    const off = live.subscribe({});
    opened[0].onFrame('snapshot', snap);
    off();
    expect(live.slots()[1].state).toBeNull();
  });
});
