// Change-password form: PUT /api/settings with the current
// bind address plus current_password/new_password. Values are never logged or persisted.

import { ApiError } from './api';

export const MIN_PASSWORD = 8;
export const PASSWORD_CHANGED_NOTICE = 'Password changed — other sessions were signed out';

export interface PasswordForm {
  current: string;
  next: string;
  confirm: string;
}

export interface PasswordResult {
  ok: boolean;
  notice: string;
  fields: Record<string, string>;
  form: string;
}

export interface PasswordDeps {
  request: <T = unknown>(method: 'PUT', path: string, body: unknown) => Promise<T>;
  session: { setToken(t: string): void };
}

export function emptyPasswordForm(): PasswordForm {
  return { current: '', next: '', confirm: '' };
}

/** Client-side checks; keys match the server's field names where one exists. */
export function validatePasswordForm(f: PasswordForm): Record<string, string> {
  const e: Record<string, string> = {};
  if (f.current === '') e.current_password = 'Enter your current password.';
  if ([...f.next].length < MIN_PASSWORD) e.new_password = `Use at least ${MIN_PASSWORD} characters.`;
  if (f.confirm !== f.next) e.confirm = 'Does not match the new password.';
  return e;
}

const fail = (fields: Record<string, string>, form = ''): PasswordResult => ({ ok: false, notice: '', fields, form });

export async function submitPasswordChange(
  deps: PasswordDeps,
  bindAddr: string,
  f: PasswordForm,
): Promise<PasswordResult> {
  const local = validatePasswordForm(f);
  if (Object.keys(local).length > 0) return fail(local);
  try {
    const out = await deps.request<{ csrf_token?: unknown }>('PUT', '/api/settings', {
      ui_bind_addr: bindAddr,
      current_password: f.current,
      new_password: f.next,
    });
    if (typeof out?.csrf_token !== 'string' || out.csrf_token === '') {
      return fail({}, 'Password may have changed, but the response had no new session token. Log in again.');
    }
    deps.session.setToken(out.csrf_token);
    return { ok: true, notice: PASSWORD_CHANGED_NOTICE, fields: {}, form: '' };
  } catch (err) {
    if (!(err instanceof ApiError)) return fail({}, 'could not change the password');
    const fields: Record<string, string> = {};
    const other: string[] = [];
    for (const fe of err.fieldErrors) {
      if (fe.field === 'current_password' || fe.field === 'new_password') fields[fe.field] = fe.message;
      else other.push(`${fe.field}: ${fe.message}`);
    }
    if (err.status !== 400 || err.fieldErrors.length === 0) other.unshift(err.message);
    return fail(fields, other.join('; '));
  }
}
