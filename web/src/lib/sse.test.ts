import { describe, expect, it, vi } from 'vitest';
import { connectEvents, FRAME_EVENTS, type EventSourceLike } from './sse';

class FakeES implements EventSourceLike {
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (e: { data: string }) => void>();
  closed = false;
  constructor(readonly url: string) {}
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    this.listeners.set(type, fn);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  emit(type: string, data: string) {
    this.listeners.get(type)?.({ data });
  }
}

function setup() {
  const made: FakeES[] = [];
  const frames: [string, unknown][] = [];
  const timers: (() => void)[] = [];
  const onClosed = vi.fn();
  const onOpen = vi.fn();
  const onError = vi.fn();
  const stop = connectEvents({
    url: '/api/events',
    create: (u) => {
      const es = new FakeES(u);
      made.push(es);
      return es;
    },
    onFrame: (ev, data) => frames.push([ev, data]),
    onClosed,
    onOpen,
    onError,
    schedule: (fn) => {
      timers.push(fn);
      return timers.length;
    },
    cancel: () => {},
  });
  return { made, frames, timers, onClosed, onOpen, onError, stop };
}

describe('connectEvents', () => {
  it('forwards parsed frames for every event type and skips bad JSON', () => {
    const { made, frames } = setup();
    expect(made[0].url).toBe('/api/events');
    for (const ev of FRAME_EVENTS) expect(made[0].listeners.has(ev)).toBe(true);
    made[0].emit('state', '{"slot":1,"state":"UP"}');
    made[0].emit('health', 'not json');
    expect(frames).toEqual([['state', { slot: 1, state: 'UP' }]]);
  });

  it('reports open and every error, even a transient one the browser retries', () => {
    const { made, onOpen, onError, onClosed } = setup();
    made[0].onopen?.();
    expect(onOpen).toHaveBeenCalledTimes(1);
    made[0].readyState = 0;
    made[0].onerror?.();
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onClosed).not.toHaveBeenCalled();
  });

  it('leaves transient errors to the browser auto-reconnect', () => {
    const { made, timers, onClosed } = setup();
    made[0].readyState = 0; // CONNECTING: the browser retries itself
    made[0].onerror?.();
    expect(timers).toHaveLength(0);
    expect(onClosed).not.toHaveBeenCalled();
  });

  it('reconnects with a new EventSource when the stream is closed for good', () => {
    const { made, timers, onClosed } = setup();
    made[0].readyState = 2;
    made[0].onerror?.();
    expect(onClosed).toHaveBeenCalledOnce();
    expect(timers).toHaveLength(1);
    timers[0]();
    expect(made).toHaveLength(2);
  });

  it('backs off 1 s doubling to the 30 s cap and resets on open', () => {
    const delays: number[] = [];
    const made: FakeES[] = [];
    const timers: (() => void)[] = [];
    connectEvents({
      url: '/api/events',
      create: (u) => {
        const es = new FakeES(u);
        made.push(es);
        return es;
      },
      onFrame: () => {},
      schedule: (fn, ms) => {
        delays.push(ms);
        timers.push(fn);
        return timers.length;
      },
      cancel: () => {},
    });
    for (let i = 0; i < 8; i++) {
      made[i].readyState = 2;
      made[i].onerror?.();
      timers[i]();
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]);
    made[8].onopen?.();
    made[8].readyState = 2;
    made[8].onerror?.();
    expect(delays[8]).toBe(1000);
  });

  it('stop closes the stream and prevents reconnects', () => {
    const { made, timers, stop } = setup();
    stop();
    expect(made[0].closed).toBe(true);
    made[0].onerror?.();
    expect(timers).toHaveLength(0);
  });
});
