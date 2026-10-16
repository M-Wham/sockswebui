// Badge text and class for slot state and kill-switch policy.
// Badges always carry text, never colour alone. DEGRADED, ERROR and allow-direct are loud.

import type { SlotState } from './slotsStore';

export type BadgeClass = 'ok' | 'neutral' | 'danger';

export interface Badge {
  cls: BadgeClass;
  label: string;
  explain: string;
}

export const ALLOW_DIRECT_LABEL = 'allow-direct: falls back to device route when degraded';

export function stateBadge(state: SlotState | null, killswitch: string): Badge {
  switch (state) {
    case 'UP':
      return { cls: 'ok', label: 'UP', explain: 'Health probe passing through the full route.' };
    case 'STARTING':
      return { cls: 'neutral', label: 'STARTING', explain: 'Building the route and waiting for the first probe.' };
    case 'STOPPED':
      return { cls: 'neutral', label: 'STOPPED', explain: 'The proxy is not running.' };
    case 'DEGRADED':
      return {
        cls: 'danger',
        label: 'DEGRADED',
        explain:
          killswitch === 'allow-direct'
            ? 'Health probes failing: new connections go out via the device route (allow-direct) while the route is rebuilt.'
            : 'Health probes failing: kill switch active, new connections are refused while the route is rebuilt.',
      };
    case 'ERROR':
      return { cls: 'danger', label: 'ERROR', explain: 'The slot could not run and needs your action.' };
    default:
      return { cls: 'neutral', label: 'UNKNOWN', explain: 'Waiting for the device to report this slot.' };
  }
}

export function killswitchBadge(killswitch: string): Badge {
  if (killswitch === 'allow-direct') {
    return {
      cls: 'danger',
      label: ALLOW_DIRECT_LABEL,
      explain: 'When degraded, traffic leaves through this device’s own connection instead of the tunnel.',
    };
  }
  return {
    cls: 'neutral',
    label: `kill switch: ${killswitch || 'block'}`,
    explain: 'When degraded, connections are refused; nothing leaks via the device route.',
  };
}
