import { describe, expect, it } from 'vitest';
import { dashboardStats } from './dashboardStats';
import { initialSlots, type SlotConfig, type SlotState } from './slotsStore';

function cfg(id: number, configured: boolean): SlotConfig {
  return { id, configured, name: '', enabled: true, bind_addr: '', port: 0, route: [], killswitch: '' };
}

function slots(spec: Record<number, [SlotState | null, number, number]>) {
  const s = initialSlots();
  for (const [id, [state, rx, tx]] of Object.entries(spec)) {
    s[Number(id)] = { ...s[Number(id)], state, rx_rate: rx, tx_rate: tx };
  }
  return s;
}

describe('dashboardStats', () => {
  it('counts running and degraded, sums rates over mixed states', () => {
    const s = slots({ 1: ['UP', 100, 10], 2: ['DEGRADED', 50, 5], 3: ['STARTING', 0, 0], 4: ['ERROR', 1, 1] });
    const c = [1, 2, 3, 4].map((i) => cfg(i, true));
    expect(dashboardStats(s, c)).toEqual({ running: 3, total: 4, degraded: 1, down: 151, up: 16 });
  });

  it('ignores unconfigured slots for running but still sums their rates', () => {
    const s = slots({ 1: ['UP', 5, 6], 2: ['UP', 7, 8] });
    const c = [cfg(1, true), cfg(2, false), cfg(3, false), cfg(4, false)];
    const r = dashboardStats(s, c);
    expect(r.running).toBe(1);
    expect(r.degraded).toBe(0);
    expect(r.down).toBe(12);
    expect(r.up).toBe(14);
  });

  it('is all zero with no configs', () => {
    expect(dashboardStats(initialSlots(), [])).toEqual({ running: 0, total: 4, degraded: 0, down: 0, up: 0 });
  });
});
