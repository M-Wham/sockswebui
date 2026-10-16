import { describe, expect, it, vi } from 'vitest';
import type { PasswordDeps } from './passwordForm';
import { ApiError } from './api';
import { PASSWORD_CHANGED_NOTICE, emptyPasswordForm, submitPasswordChange, validatePasswordForm } from './passwordForm';

const form = { current: 'old-password', next: 'new-password', confirm: 'new-password' };

function deps(request: ReturnType<typeof vi.fn>): PasswordDeps & { setToken: ReturnType<typeof vi.fn> } {
  const setToken = vi.fn();
  return { request: request as unknown as PasswordDeps['request'], session: { setToken }, setToken };
}

describe('validatePasswordForm', () => {
  it('refuses a short new password', () => {
    expect(validatePasswordForm({ ...form, next: 'short', confirm: 'short' }).new_password).toMatch(/8/);
  });
  it('refuses a mismatched confirmation', () => {
    expect(validatePasswordForm({ ...form, confirm: 'other-password' }).confirm).toBeDefined();
  });
  it('requires the current password', () => {
    expect(validatePasswordForm({ ...form, current: '' }).current_password).toBeDefined();
  });
  it('accepts a good form', () => {
    expect(validatePasswordForm(form)).toEqual({});
  });
});

describe('submitPasswordChange', () => {
  it('sends the current bind address with both passwords and swaps the CSRF token', async () => {
    const request = vi.fn().mockResolvedValue({ ui_bind_addr: '0.0.0.0:8787', csrf_token: 'new-tok' });
    const d = deps(request);
    const out = await submitPasswordChange(d, '0.0.0.0:8787', form);
    expect(request).toHaveBeenCalledWith('PUT', '/api/settings', {
      ui_bind_addr: '0.0.0.0:8787',
      current_password: 'old-password',
      new_password: 'new-password',
    });
    expect(d.setToken).toHaveBeenCalledWith('new-tok');
    expect(out).toEqual({ ok: true, notice: PASSWORD_CHANGED_NOTICE, fields: {}, form: '' });
    expect(PASSWORD_CHANGED_NOTICE).toBe('Password changed — other sessions were signed out');
  });

  it('does not send anything when validation fails', async () => {
    const request = vi.fn();
    const out = await submitPasswordChange(deps(request), 'a:1', { ...form, next: 'x', confirm: 'x' });
    expect(request).not.toHaveBeenCalled();
    expect(out.ok).toBe(false);
    expect(out.fields.new_password).toBeDefined();
  });

  it('maps 400 field errors onto the inputs and keeps the old token', async () => {
    const request = vi.fn().mockRejectedValue(new ApiError(400, 'invalid', [
      { field: 'current_password', message: 'is incorrect' },
      { field: 'new_password', message: 'must be at least 8 characters' },
    ]));
    const d = deps(request);
    const out = await submitPasswordChange(d, 'a:1', form);
    expect(out.ok).toBe(false);
    expect(out.fields).toEqual({ current_password: 'is incorrect', new_password: 'must be at least 8 characters' });
    expect(d.setToken).not.toHaveBeenCalled();
  });

  it('treats a success without csrf_token as a failure to show', async () => {
    const out = await submitPasswordChange(deps(vi.fn().mockResolvedValue({})), 'a:1', form);
    expect(out.ok).toBe(false);
    expect(out.form).not.toBe('');
  });

  it('empty form is all blank', () => {
    expect(emptyPasswordForm()).toEqual({ current: '', next: '', confirm: '' });
  });
});
