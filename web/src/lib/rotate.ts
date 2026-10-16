// "New IP" helpers: which slots a rotate affects, the confirm text,
// the rate-limit wait text, and which hop a slot row rotates. Pure; no network.

import type { EndpointView } from './endpointFilter';
import type { SlotConfig } from './slotsStore';

export const slotLabel = (s: Pick<SlotConfig, 'id' | 'name'>): string => s.name || `Slot ${s.id}`;

export function affectedSlots(endpointId: string, slots: readonly SlotConfig[]): SlotConfig[] {
  return slots.filter((s) => s.route.includes(endpointId));
}

export interface RotateDialogText {
  message: string;
  /** Extra lines, one per affected allow-direct slot. */
  warnings: string[];
}

export function rotateDialogText(endpointName: string, affected: readonly SlotConfig[]): RotateDialogText {
  const names = affected.map(slotLabel).join(', ');
  const mid = affected.length === 0 ? 'No slots use it right now.' : `Slots ${names} will reconnect and open connections will drop.`;
  return {
    message: `Get a new IP for ${endpointName}? ${mid} Some providers bill per IP.`,
    warnings: affected
      .filter((s) => s.killswitch === 'allow-direct')
      .map((s) => `If the provider rejects the new session, slot ${slotLabel(s)} will use this device's own connection.`),
  };
}

export function retryWaitText(retryAfterSeconds: number | undefined): string {
  const wait =
    retryAfterSeconds === undefined || !Number.isFinite(retryAfterSeconds) || retryAfterSeconds < 1
      ? 'a few seconds'
      : `${Math.ceil(retryAfterSeconds)} second${Math.ceil(retryAfterSeconds) === 1 ? '' : 's'}`;
  return `New IP was just requested. Try again in ${wait}.`;
}

/** The last route hop that is a rotating-session template endpoint, or null. */
export function lastTemplateHop(route: readonly string[], endpoints: readonly EndpointView[]): EndpointView | null {
  for (let i = route.length - 1; i >= 0; i--) {
    const e = endpoints.find((x) => x.id === route[i]);
    if (e?.session !== undefined) return e;
  }
  return null;
}
