// Slot editor form model. The SOCKS auth password is write-only:
// never pre-filled, omitted from the PUT unless newly typed (server keeps the stored one).
// allow-direct is an explicit opt-in: requesting it only opens a confirm; the value stays
// "block" until confirmAllowDirect.

import { ApiError } from './api';
import { isSensitiveHost } from './dialTargets';

export type Killswitch = 'block' | 'allow-direct';

export interface Health {
  interval_s: number;
  url: string;
  fail_threshold: number;
}

export interface SlotView {
  id: number;
  configured: boolean;
  name: string;
  enabled: boolean;
  bind_addr: string;
  port: number;
  socks_auth: { username: string; password_set: boolean } | null;
  route: string[];
  killswitch: string;
  /** Deny LAN-class targets on device-route dials. Older servers omit it. */
  block_lan_targets?: boolean;
  health: Health;
  status?: { state?: string };
}

export interface SlotForm {
  name: string;
  enabled: boolean;
  bindAddr: string;
  port: string;
  authEnabled: boolean;
  authUsername: string;
  /** Newly typed password only; '' means keep the stored one. */
  authPassword: string;
  authPasswordSet: boolean;
  route: string[];
  killswitch: Killswitch;
  blockLanTargets: boolean;
  healthInterval: string;
  healthUrl: string;
  healthThreshold: string;
}

export interface SlotBody {
  name: string;
  enabled: boolean;
  bind_addr: string;
  port: number;
  socks_auth: { username: string; password?: string } | null;
  route: string[];
  killswitch: Killswitch;
  block_lan_targets: boolean;
  health?: Health;
}

export function formFromSlot(v: SlotView): SlotForm {
  return {
    name: v.name ?? '',
    enabled: v.enabled === true,
    bindAddr: v.bind_addr ?? '',
    port: v.port ? String(v.port) : '',
    authEnabled: v.socks_auth != null,
    authUsername: v.socks_auth?.username ?? '',
    authPassword: '',
    authPasswordSet: v.socks_auth?.password_set === true,
    route: [...(v.route ?? [])],
    killswitch: initialKillswitch(v.killswitch).value,
    blockLanTargets: v.block_lan_targets === true,
    healthInterval: v.health?.interval_s ? String(v.health.interval_s) : '',
    healthUrl: v.health?.url ?? '',
    healthThreshold: v.health?.fail_threshold ? String(v.health.fail_threshold) : '',
  };
}

// --- kill switch -------------------------------------------------------------------

export interface KillswitchState {
  value: Killswitch;
  /** allow-direct was requested and awaits an explicit confirm. */
  pending: boolean;
}

/** Stored value as loaded from the server; anything unknown fails closed to block. */
export function initialKillswitch(stored: string): KillswitchState {
  return { value: stored === 'allow-direct' ? 'allow-direct' : 'block', pending: false };
}

export function requestKillswitch(s: KillswitchState, requested: Killswitch): KillswitchState {
  if (requested === 'allow-direct' && s.value !== 'allow-direct') return { value: 'block', pending: true };
  return { value: requested === 'allow-direct' ? 'allow-direct' : 'block', pending: false };
}

export function confirmAllowDirect(s: KillswitchState): KillswitchState {
  return s.pending ? { value: 'allow-direct', pending: false } : { ...s };
}

export function cancelAllowDirect(s: KillswitchState): KillswitchState {
  return s.pending ? { value: 'block', pending: false } : { ...s };
}

// --- block LAN targets -------------------------------------------

export const BLOCK_LAN_LABEL = 'Block LAN targets';

export const BLOCK_LAN_HELP =
  'Refuse connections to private LAN addresses (192.168.x.x, 10.x.x.x and similar) when this slot dials through the device. Turn it on if the slot can be reached from beyond your LAN, for example through a router port forward or an open-proxy bind (0.0.0.0 without SOCKS auth). WireGuard and proxy routes exit at the remote end, so this only affects an empty route and the allow-direct fallback.';

/** The toggle only matters where the slot dials via the device route. */
export function showBlockLanToggle(route: string[], killswitch: Killswitch): boolean {
  return route.length === 0 || killswitch === 'allow-direct';
}

/** Element id of the toggle; the open-proxy warning links here. Matches SlotEditor's fid(). */
export function blockLanToggleId(slotId: number): string {
  return `slot-${slotId}-block_lan_targets`;
}

// --- request body ------------------------------------------------------------------

function toInt(s: string): number {
  const t = s.trim();
  return /^\d+$/.test(t) ? Number(t) : 0;
}

function buildHealth(f: SlotForm, defaults?: Health): Health | undefined {
  const interval = f.healthInterval.trim();
  const url = f.healthUrl.trim();
  const threshold = f.healthThreshold.trim();
  if (interval === '' && url === '' && threshold === '') return undefined;
  return {
    interval_s: interval !== '' ? toInt(interval) : (defaults?.interval_s ?? 0),
    url: url !== '' ? url : (defaults?.url ?? ''),
    fail_threshold: threshold !== '' ? toInt(threshold) : (defaults?.fail_threshold ?? 0),
  };
}

/** PUT /api/slots/{id} body. Empty health fields fall back to the global health_defaults. */
export function buildSlotBody(f: SlotForm, healthDefaults?: Health): SlotBody {
  let socksAuth: SlotBody['socks_auth'] = null;
  if (f.authEnabled) {
    socksAuth = { username: f.authUsername.trim() };
    if (f.authPassword !== '') socksAuth.password = f.authPassword;
  }
  const body: SlotBody = {
    name: f.name.trim(),
    enabled: f.enabled,
    bind_addr: f.bindAddr.trim(),
    port: toInt(f.port),
    socks_auth: socksAuth,
    route: [...f.route],
    killswitch: f.killswitch,
    block_lan_targets: f.blockLanTargets,
  };
  const health = buildHealth(f, healthDefaults);
  if (health) body.health = health;
  return body;
}

// --- server errors -----------------------------------------------------------------

export const SLOT_FIELDS = [
  'name',
  'bind_addr',
  'port',
  'killswitch',
  'socks_auth.username',
  'socks_auth.password',
  'health.interval_s',
  'health.url',
  'health.fail_threshold',
  'route',
] as const;

export interface SlotErrors {
  /** Keyed by server field path (e.g. "port", "socks_auth.password", "route"). */
  fields: Record<string, string>;
  /** Route hop index → message (from "route[i]"). */
  hops: Record<number, string>;
  /** Anything not tied to a field on this form. */
  form: string;
}

function append(map: Record<string | number, string>, key: string | number, msg: string): void {
  map[key] = map[key] ? `${map[key]}; ${msg}` : msg;
}

/** Maps a failed PUT (400 field errors, 409 wireguard endpoint in use) onto the form. */
export function mapSlotErrors(err: unknown): SlotErrors {
  const out: SlotErrors = { fields: {}, hops: {}, form: '' };
  if (!(err instanceof ApiError)) {
    out.form = 'could not save the slot';
    return out;
  }
  const other: string[] = [];
  for (const fe of err.fieldErrors) {
    const hop = /^route\[(\d+)\]$/.exec(fe.field);
    if (hop) append(out.hops, Number(hop[1]), fe.message);
    else if ((SLOT_FIELDS as readonly string[]).includes(fe.field)) append(out.fields, fe.field, fe.message);
    else other.push(`${fe.field}: ${fe.message}`);
  }
  if (err.status !== 400 || err.fieldErrors.length === 0) other.unshift(err.message);
  out.form = other.join('; ');
  return out;
}

// --- warnings ----------------------------------------------------------------------

/**
 * Non-blocking warnings. A health URL whose host is loopback, link-local,
 * unspecified, multicast or cloud-metadata cannot be probed on a direct slot: the egress deny
 * table rejects it after phase 1.
 */
export function slotFormWarnings(f: SlotForm): string[] {
  const raw = f.healthUrl.trim();
  if (raw === '') return [];
  let host: string;
  try {
    host = new URL(raw).hostname;
  } catch {
    return [];
  }
  if (host !== '' && isSensitiveHost(host)) {
    return ['The probe URL host is a loopback, link-local, unspecified, multicast or cloud-metadata address. Saving is allowed, but on a direct slot the probe to it fails after phase 1, so the slot would report as unhealthy.'];
  }
  return [];
}

/**
 * Open-proxy warning. Warn only: a non-loopback bind without SOCKS auth lets
 * anyone who can reach the port use the slot. Save stays allowed.
 */
export function openProxyWarning(bind: string, authEnabled: boolean): string[] {
  const b = bind.trim().replace(/^\[|\]$/g, '').toLowerCase();
  if (b === '' || authEnabled) return [];
  if (b === 'localhost' || b === '::1' || b === '127.0.0.1' || /^127\.\d+\.\d+\.\d+$/.test(b)) return [];
  return [
    `Anyone who can reach this port can use your VPN and proxy accounts. For a direct or allow-direct slot they can also reach your LAN unless "${BLOCK_LAN_LABEL}" is on. Saving is allowed; consider binding to 127.0.0.1 or requiring a username and password.`,
  ];
}
