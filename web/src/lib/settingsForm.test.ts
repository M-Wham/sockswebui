import { describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { bindFlagNote, bindInUse, healthRows, mapSettingsErrors, saveSettings, showConfigError, START_ON_ENABLE_HELP } from './settingsForm';

describe('saveSettings', () => {
  it('sends trimmed ui_bind_addr and start_on_enable', async () => {
    const request = vi.fn().mockResolvedValue({ ui_bind_addr: '127.0.0.1:9090', start_on_enable: false, config_error: '' });
    const out = await saveSettings(request, '  127.0.0.1:9090 ', false);
    expect(request).toHaveBeenCalledWith('PUT', '/api/settings', { ui_bind_addr: '127.0.0.1:9090', start_on_enable: false });
    expect(out.start_on_enable).toBe(false);
  });
  it('has help text covering both states', () => {
    expect(START_ON_ENABLE_HELP).toMatch(/starts it/);
    expect(START_ON_ENABLE_HELP).toMatch(/at boot/);
  });
});

describe('mapSettingsErrors', () => {
  it('puts a 400 ui_bind_addr error on that field', () => {
    const e = new ApiError(400, 'invalid', [{ field: 'ui_bind_addr', message: 'must be host:port' }]);
    expect(mapSettingsErrors(e)).toEqual({ fields: { ui_bind_addr: 'must be host:port' }, form: '' });
  });
  it('keeps other errors at form level', () => {
    expect(mapSettingsErrors(new ApiError(500, 'boom')).form).toBe('boom');
    expect(mapSettingsErrors(new Error('x')).form).toBe('could not save settings');
  });
});

describe('read-only parts', () => {
  it('lists health defaults as label/value rows', () => {
    expect(healthRows({ interval_s: 30, url: 'https://x', fail_threshold: 2 })).toEqual([
      { label: 'Interval (s)', value: '30' },
      { label: 'Probe URL', value: 'https://x' },
      { label: 'Fail threshold', value: '2' },
    ]);
    expect(healthRows(undefined)).toEqual([]);
  });
  it('shows config_error only when non-empty', () => {
    expect(showConfigError({ ui_bind_addr: 'a', config_error: 'bad json' })).toBe('bad json');
    expect(showConfigError({ ui_bind_addr: 'a', config_error: '' })).toBe('');
  });
});

describe('bindFlagNote', () => {
  const note = 'Set by --ui-addr; this field takes effect only without the flag';
  it('shows the note when the flag set the bind', () => {
    expect(bindFlagNote({ ui_bind_addr: 'a', config_error: '', ui_bind_source: 'flag' })).toBe(note);
  });
  it('hides it for setting or absent source', () => {
    expect(bindFlagNote({ ui_bind_addr: 'a', config_error: '', ui_bind_source: 'setting' })).toBe('');
    expect(bindFlagNote({ ui_bind_addr: 'a', config_error: '' })).toBe('');
  });
});

describe('bindInUse', () => {
  const base = { ui_bind_addr: 'a', config_error: '' };
  it('shows the effective address when the flag is in effect', () => {
    expect(bindInUse({ ...base, ui_bind_source: 'flag', ui_bind_effective: '0.0.0.0:8787' })).toBe('In use: 0.0.0.0:8787');
  });
  it('is empty for other sources or an empty effective address', () => {
    expect(bindInUse({ ...base, ui_bind_source: 'setting', ui_bind_effective: '0.0.0.0:8787' })).toBe('');
    expect(bindInUse({ ...base, ui_bind_source: 'flag', ui_bind_effective: '' })).toBe('');
    expect(bindInUse({ ...base, ui_bind_source: 'flag' })).toBe('');
  });
});
