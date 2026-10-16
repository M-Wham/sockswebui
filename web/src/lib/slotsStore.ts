// Live slot state for the Slots screen: a pure reducer over SSE frames and API
// responses. State honesty: a slot's state changes only when a frame or an API response
// says so; nothing here ever invents UP. Unknown slots and malformed frames are ignored.

import { SLOT_COUNT } from './router';

export const SLOT_STATES = ['STOPPED', 'STARTING', 'UP', 'DEGRADED', 'ERROR'] as const;
export type SlotState = (typeof SLOT_STATES)[number];

export interface SlotLive {
  id: number;
  name: string;
  /** null until a snapshot/state frame or API response reports it. */
  state: SlotState | null;
  msg: string;
  health_ok: boolean | null;
  exit_ip: string;
  country: string;
  latency_ms: number | null;
  /** Last probe/start error text from the API status; shown as text only. */
  last_error: string;
  rx_rate: number;
  tx_rate: number;
  bytes_in: number;
  bytes_out: number;
  /** Bumped by every snapshot/state frame; lets a late API response yield to SSE. */
  seq: number;
}

export type SlotsState = Record<number, SlotLive>;

function blank(id: number): SlotLive {
  return {
    id,
    name: '',
    state: null,
    msg: '',
    health_ok: null,
    exit_ip: '',
    country: '',
    latency_ms: null,
    last_error: '',
    rx_rate: 0,
    tx_rate: 0,
    bytes_in: 0,
    bytes_out: 0,
    seq: 0,
  };
}

export function initialSlots(): SlotsState {
  const s: SlotsState = {};
  for (let id = 1; id <= SLOT_COUNT; id++) s[id] = blank(id);
  return s;
}

type Obj = Record<string, unknown>;

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}
function str(v: unknown): string {
  return typeof v === 'string' ? v : '';
}
function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0;
}
function asState(v: unknown): SlotState | null {
  return SLOT_STATES.find((s) => s === v) ?? null;
}
function knownId(s: SlotsState, v: unknown): number | null {
  return typeof v === 'number' && Number.isInteger(v) && v in s ? v : null;
}

/** Exit fields from an API `status` object; null/missing fields become empty. */
function statusFields(v: unknown): Pick<SlotLive, 'exit_ip' | 'country' | 'latency_ms' | 'last_error'> {
  const o = isObj(v) ? v : {};
  return {
    exit_ip: str(o.exit_ip),
    country: str(o.country),
    latency_ms: typeof o.latency_ms === 'number' ? num(o.latency_ms) : null,
    last_error: str(o.last_error),
  };
}

function patch(s: SlotsState, id: number, p: Partial<SlotLive>): SlotsState {
  return { ...s, [id]: { ...s[id], ...p } };
}

function snapshot(s: SlotsState, data: Obj): SlotsState | null {
  if (!Array.isArray(data.slots)) return null;
  const next = initialSlots();
  for (const k of Object.keys(next)) next[+k].seq = s[+k].seq + 1;
  for (const raw of data.slots) {
    if (!isObj(raw)) continue;
    const id = knownId(next, raw.id);
    const state = asState(raw.state);
    if (id === null || state === null) continue;
    next[id] = {
      ...next[id],
      name: str(raw.name),
      state,
      msg: str(raw.msg),
      bytes_in: num(raw.bytes_in),
      bytes_out: num(raw.bytes_out),
      ...statusFields(raw.status),
    };
  }
  return next;
}

/** Applies one SSE frame (event name + parsed JSON data); returns s unchanged when it does not apply. */
export function reduce(s: SlotsState, event: string, data: unknown): SlotsState {
  if (!isObj(data)) return s;
  if (event === 'snapshot') return snapshot(s, data) ?? s;
  const id = knownId(s, data.slot);
  if (id === null) return s;
  switch (event) {
    case 'state': {
      const state = asState(data.state);
      return state === null ? s : patch(s, id, { state, msg: str(data.msg), seq: s[id].seq + 1 });
    }
    case 'health': {
      const h = data.health;
      if (!isObj(h) || typeof h.ok !== 'boolean') return s;
      if (!h.ok) return patch(s, id, { health_ok: false, exit_ip: '', country: '', latency_ms: null });
      return patch(s, id, {
        health_ok: true,
        last_error: '',
        exit_ip: str(h.exit_ip),
        country: str(h.country),
        latency_ms: typeof h.latency_ms === 'number' ? num(h.latency_ms) : null,
      });
    }
    case 'throughput': {
      const t = data.throughput;
      if (!isObj(t)) return s;
      return patch(s, id, {
        rx_rate: num(t.rate_in),
        tx_rate: num(t.rate_out),
        bytes_in: num(t.bytes_in),
        bytes_out: num(t.bytes_out),
      });
    }
    default:
      return s;
  }
}

/**
 * Applies a slot status body from POST /api/slots/:id/start|stop. seenSeq is the slot's
 * seq recorded before the request: if any snapshot/state frame arrived since, SSE is
 * newer than the response and the response is dropped.
 */
export function applyStatus(s: SlotsState, body: unknown, seenSeq?: number): SlotsState {
  if (!isObj(body)) return s;
  const id = knownId(s, body.id);
  const state = asState(body.state);
  if (id === null || state === null) return s;
  if (seenSeq !== undefined && s[id].seq !== seenSeq) return s;
  return patch(s, id, { state, msg: str(body.msg) });
}

/**
 * Seeds exit/country/latency/last_error from GET /api/slots `status` for slots that have no
 * live data yet. Never touches state (that comes from SSE/actions) and never overrides a
 * health frame or snapshot already received.
 */
export function seedFromList(s: SlotsState, list: unknown): SlotsState {
  if (!Array.isArray(list)) return s;
  let next = s;
  for (const raw of list) {
    if (!isObj(raw)) continue;
    const id = knownId(next, raw.id);
    if (id === null || next[id].seq !== 0 || next[id].health_ok !== null) continue;
    next = patch(next, id, statusFields(raw.status));
  }
  return next;
}

const UNITS = ['B/s', 'KB/s', 'MB/s', 'GB/s'];

export function formatRate(bps: number): string {
  let v = num(bps);
  let i = 0;
  while (v >= 1000 && i < UNITS.length - 1) {
    v /= 1000;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${UNITS[i]}`;
}

/** Slot config as returned by GET /api/slots (secrets never included). */
export interface SlotConfig {
  id: number;
  configured: boolean;
  name: string;
  enabled: boolean;
  bind_addr: string;
  port: number;
  route: string[];
  killswitch: string;
}
