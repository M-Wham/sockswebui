// Rolling 5 minute throughput history for the chart. Pure; callers own the array.

export const WINDOW_MS = 300_000;
export const BUCKET_MS = 10_000;
export const BUCKETS = 30;

export interface Sample {
  t: number;
  down: number;
  up: number;
}

export interface Bar {
  start: number;
  down: number | null;
  up: number | null;
}

export function record(h: readonly Sample[], s: Sample): Sample[] {
  const last = h[h.length - 1];
  if (last && s.t < last.t) return [...h];
  return [...h, s].filter((x) => x.t > s.t - WINDOW_MS);
}

export function buckets(h: readonly Sample[], now: number): Bar[] {
  const origin = now - WINDOW_MS;
  const sums = Array.from({ length: BUCKETS }, () => ({ down: 0, up: 0, n: 0 }));
  for (const s of h) {
    if (s.t <= origin || s.t > now) continue;
    const i = Math.min(BUCKETS - 1, Math.floor((s.t - origin) / BUCKET_MS));
    sums[i].down += s.down;
    sums[i].up += s.up;
    sums[i].n++;
  }
  return sums.map((b, i) => ({
    start: origin + i * BUCKET_MS,
    down: b.n ? b.down / b.n : null,
    up: b.n ? b.up / b.n : null,
  }));
}

export function peak(bars: readonly Bar[]): number {
  let m = 0;
  for (const b of bars) {
    if (b.down !== null && b.down > m) m = b.down;
    if (b.up !== null && b.up > m) m = b.up;
  }
  return m;
}
