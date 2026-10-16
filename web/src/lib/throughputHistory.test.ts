import { describe, expect, it } from 'vitest';
import { BUCKETS, BUCKET_MS, WINDOW_MS, buckets, peak, record, type Sample } from './throughputHistory';

const s = (t: number, down = 1, up = 1): Sample => ({ t, down, up });

describe('constants', () => {
  it('describe a 5 minute window of 30 ten second buckets', () => {
    expect(WINDOW_MS).toBe(300_000);
    expect(BUCKET_MS).toBe(10_000);
    expect(BUCKETS).toBe(30);
  });
});

describe('record', () => {
  it('appends a sample', () => {
    expect(record([s(1000)], s(2000))).toEqual([s(1000), s(2000)]);
  });
  it('drops samples at exactly the window edge but keeps newer ones', () => {
    const h = [s(1000), s(1001)];
    expect(record(h, s(1000 + WINDOW_MS)).map((x) => x.t)).toEqual([1001, 1000 + WINDOW_MS]);
  });
  it('ignores a sample older than the last one', () => {
    const h = [s(5000)];
    expect(record(h, s(4000))).toEqual([s(5000)]);
  });
  it('never mutates the input', () => {
    const h = [s(1000)];
    const copy = JSON.stringify(h);
    const out = record(h, s(2000));
    expect(JSON.stringify(h)).toBe(copy);
    expect(out).not.toBe(h);
  });
});

describe('buckets', () => {
  const now = 1_000_000;
  it('returns exactly 30 bars, oldest first, last ending at now', () => {
    const b = buckets([], now);
    expect(b).toHaveLength(30);
    expect(b[0].start).toBe(now - WINDOW_MS);
    expect(b[29].start + BUCKET_MS).toBe(now);
    expect(b.every((x) => x.down === null && x.up === null)).toBe(true);
  });
  it('averages samples within one bucket', () => {
    const b = buckets([s(now - 5000, 10, 2), s(now - 4000, 20, 4)], now);
    expect(b[29].down).toBe(15);
    expect(b[29].up).toBe(3);
    expect(b[28].down).toBeNull();
  });
  it('leaves null gaps between populated buckets', () => {
    const b = buckets([s(now - 295_000, 8, 8), s(now - 5000, 4, 4)], now);
    expect(b[0].down).toBe(8);
    expect(b[1].down).toBeNull();
    expect(b[29].down).toBe(4);
  });
  it('excludes a sample exactly at now - 300000', () => {
    const b = buckets([s(now - WINDOW_MS, 99, 99)], now);
    expect(b[0].down).toBeNull();
  });
});

describe('peak', () => {
  it('is 0 when every bar is null', () => {
    expect(peak(buckets([], 1_000_000))).toBe(0);
  });
  it('is the max over down and up', () => {
    expect(peak([{ start: 0, down: 3, up: null }, { start: 1, down: null, up: 9 }, { start: 2, down: 4, up: 1 }])).toBe(9);
  });
});
