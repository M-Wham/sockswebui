// Pure aggregate numbers for the Slots stats panel. Reads only reported state.

import { SLOT_COUNT } from './router';
import type { SlotConfig, SlotsState } from './slotsStore';

export interface DashboardStats {
  running: number;
  total: number;
  degraded: number;
  down: number;
  up: number;
}

export function dashboardStats(slots: SlotsState, configs: readonly SlotConfig[]): DashboardStats {
  let running = 0;
  let degraded = 0;
  for (const c of configs) {
    if (!c.configured) continue;
    const st = slots[c.id]?.state;
    if (st === 'STARTING' || st === 'UP' || st === 'DEGRADED') running++;
    if (st === 'DEGRADED') degraded++;
  }
  let down = 0;
  let up = 0;
  for (const s of Object.values(slots)) {
    down += s.rx_rate;
    up += s.tx_rate;
  }
  return { running, total: SLOT_COUNT, degraded, down, up };
}
