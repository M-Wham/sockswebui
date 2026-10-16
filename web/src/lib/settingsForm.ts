// Settings screen logic: UI bind address (writable), health defaults
// (read-only) and the corrupt-boot message. Saves send ui_bind_addr (always) and start_on_enable.

import { ApiError, type Settings } from './api';

export type SettingsRequest = <T = unknown>(method: 'PUT', path: string, body: unknown) => Promise<T>;

export interface SettingsErrors {
  fields: Record<string, string>;
  form: string;
}

export const START_ON_ENABLE_HELP =
  'On: saving a slot as enabled starts it. Off: enabled only means the slot starts at boot.';

export function saveSettings(request: SettingsRequest, addr: string, startOnEnable: boolean): Promise<Settings> {
  return request<Settings>('PUT', '/api/settings', { ui_bind_addr: addr.trim(), start_on_enable: startOnEnable });
}

export function mapSettingsErrors(err: unknown): SettingsErrors {
  const out: SettingsErrors = { fields: {}, form: '' };
  if (!(err instanceof ApiError)) {
    out.form = 'could not save settings';
    return out;
  }
  const other: string[] = [];
  for (const fe of err.fieldErrors) {
    if (fe.field === 'ui_bind_addr') out.fields.ui_bind_addr = fe.message;
    else other.push(`${fe.field}: ${fe.message}`);
  }
  if (err.status !== 400 || err.fieldErrors.length === 0) other.unshift(err.message);
  out.form = other.join('; ');
  return out;
}

export function healthRows(h: Record<string, unknown> | undefined): { label: string; value: string }[] {
  if (!h) return [];
  const rows: { label: string; value: string }[] = [];
  const add = (label: string, key: string) => {
    if (h[key] !== undefined) rows.push({ label, value: String(h[key]) });
  };
  add('Interval (s)', 'interval_s');
  add('Probe URL', 'url');
  add('Fail threshold', 'fail_threshold');
  return rows;
}

export function showConfigError(s: Settings): string {
  return typeof s.config_error === 'string' ? s.config_error : '';
}

export function bindFlagNote(s: Settings): string {
  return s.ui_bind_source === 'flag' ? 'Set by --ui-addr; this field takes effect only without the flag' : '';
}

export function bindInUse(s: Settings): string {
  return s.ui_bind_source === 'flag' && s.ui_bind_effective ? `In use: ${s.ui_bind_effective}` : '';
}
