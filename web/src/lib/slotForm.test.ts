import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import {
  buildSlotBody,
  cancelAllowDirect,
  confirmAllowDirect,
  formFromSlot,
  initialKillswitch,
  mapSlotErrors,
  requestKillswitch,
  openProxyWarning,
  slotFormWarnings,
  BLOCK_LAN_HELP,
  BLOCK_LAN_LABEL,
  blockLanToggleId,
  showBlockLanToggle,
  type SlotView,
} from './slotForm';

const view: SlotView = {
  id: 2,
  configured: true,
  name: 'Office',
  enabled: true,
  bind_addr: '0.0.0.0',
  port: 1081,
  socks_auth: { username: 'alice', password_set: true },
  route: ['wg1', 'p1'],
  killswitch: 'block',
  health: { interval_s: 30, url: 'https://am.i.mullvad.net/json', fail_threshold: 2 },
};

describe('formFromSlot', () => {
  it('maps the view and never pre-fills the SOCKS password', () => {
    const f = formFromSlot(view);
    expect(f).toMatchObject({
      name: 'Office',
      enabled: true,
      bindAddr: '0.0.0.0',
      port: '1081',
      authEnabled: true,
      authUsername: 'alice',
      authPassword: '',
      authPasswordSet: true,
      route: ['wg1', 'p1'],
      killswitch: 'block',
      healthInterval: '30',
      healthUrl: 'https://am.i.mullvad.net/json',
      healthThreshold: '2',
      blockLanTargets: false,
    });
  });

  it('loads block_lan_targets (missing means off)', () => {
    expect(formFromSlot({ ...view, block_lan_targets: true }).blockLanTargets).toBe(true);
    const { block_lan_targets: _drop, ...old } = { ...view, block_lan_targets: false };
    expect(formFromSlot(old as SlotView).blockLanTargets).toBe(false);
  });

  it('handles a slot without SOCKS auth', () => {
    const f = formFromSlot({ ...view, socks_auth: null });
    expect(f.authEnabled).toBe(false);
    expect(f.authPasswordSet).toBe(false);
  });
});

describe('kill switch confirm', () => {
  it('switching to allow-direct without a confirm keeps block', () => {
    const s = requestKillswitch(initialKillswitch('block'), 'allow-direct');
    expect(s.value).toBe('block');
    expect(s.pending).toBe(true);
  });

  it('cancelling the confirm keeps block', () => {
    const s = cancelAllowDirect(requestKillswitch(initialKillswitch('block'), 'allow-direct'));
    expect(s).toEqual({ value: 'block', pending: false });
  });

  it('an explicit confirm sets allow-direct', () => {
    const s = confirmAllowDirect(requestKillswitch(initialKillswitch('block'), 'allow-direct'));
    expect(s).toEqual({ value: 'allow-direct', pending: false });
  });

  it('confirm without a pending request does nothing', () => {
    expect(confirmAllowDirect(initialKillswitch('block'))).toEqual({ value: 'block', pending: false });
  });

  it('going back to block needs no confirm', () => {
    const s = requestKillswitch(initialKillswitch('allow-direct'), 'block');
    expect(s).toEqual({ value: 'block', pending: false });
  });

  it('an unknown stored value loads as block (fail closed)', () => {
    expect(initialKillswitch('weird').value).toBe('block');
  });
});

describe('buildSlotBody', () => {
  it('omits an untouched SOCKS password', () => {
    const body = buildSlotBody(formFromSlot(view));
    expect(body.socks_auth).toEqual({ username: 'alice' });
    expect('password' in (body.socks_auth ?? {})).toBe(false);
    expect(JSON.stringify(body)).not.toContain('password');
  });

  it('sends a newly typed SOCKS password', () => {
    const f = { ...formFromSlot(view), authPassword: 's3cret!' };
    expect(buildSlotBody(f).socks_auth).toEqual({ username: 'alice', password: 's3cret!' });
  });

  it('sends socks_auth null when auth is off', () => {
    const f = { ...formFromSlot(view), authEnabled: false, authPassword: 'ignored' };
    expect(buildSlotBody(f).socks_auth).toBeNull();
  });

  it('builds the full PUT body with numbers and the route', () => {
    const body = buildSlotBody(formFromSlot(view));
    expect(body).toEqual({
      name: 'Office',
      enabled: true,
      bind_addr: '0.0.0.0',
      port: 1081,
      socks_auth: { username: 'alice' },
      route: ['wg1', 'p1'],
      killswitch: 'block',
      block_lan_targets: false,
      health: { interval_s: 30, url: 'https://am.i.mullvad.net/json', fail_threshold: 2 },
    });
  });

  it('fills empty health fields from health_defaults', () => {
    const f = { ...formFromSlot(view), healthInterval: '', healthUrl: ' ', healthThreshold: '5' };
    const body = buildSlotBody(f, { interval_s: 60, url: 'https://x.test/', fail_threshold: 3 });
    expect(body.health).toEqual({ interval_s: 60, url: 'https://x.test/', fail_threshold: 5 });
  });

  it('omits health when every field is empty (server default)', () => {
    const f = { ...formFromSlot(view), healthInterval: '', healthUrl: '', healthThreshold: '' };
    expect(buildSlotBody(f).health).toBeUndefined();
  });

  it('turns a non-numeric port into 0 so the server rejects it', () => {
    expect(buildSlotBody({ ...formFromSlot(view), port: 'abc' }).port).toBe(0);
  });
});

describe('mapSlotErrors', () => {
  it('maps 400 field errors to form fields and route hops', () => {
    const err = new ApiError(400, 'invalid input', [
      { field: 'port', message: 'must be 1..65535' },
      { field: 'bind_addr', message: 'must be an IP' },
      { field: 'socks_auth.password', message: 'is required when SOCKS auth is enabled' },
      { field: 'health.url', message: 'must be http(s)' },
      { field: 'route[1]', message: 'WireGuard hop must precede proxy hops' },
      { field: 'route', message: 'route has 5 hops' },
      { field: 'slots[0].port', message: 'clashes with slot 2' },
    ]);
    const m = mapSlotErrors(err);
    expect(m.fields).toEqual({
      port: 'must be 1..65535',
      bind_addr: 'must be an IP',
      'socks_auth.password': 'is required when SOCKS auth is enabled',
      'health.url': 'must be http(s)',
      route: 'route has 5 hops',
    });
    expect(m.hops).toEqual({ 1: 'WireGuard hop must precede proxy hops' });
    expect(m.form).toBe('slots[0].port: clashes with slot 2');
  });

  it('maps 409 "wireguard endpoint in use" to the route hop', () => {
    const err = new ApiError(409, 'wireguard endpoint in use', [
      { field: 'route[0]', message: 'wireguard endpoint "wg1" is in use by running slot 1' },
    ]);
    const m = mapSlotErrors(err);
    expect(m.hops).toEqual({ 0: 'wireguard endpoint "wg1" is in use by running slot 1' });
    expect(m.form).toMatch(/in use/);
  });

  it('maps a 409 without a hop index to the route field', () => {
    const err = new ApiError(409, 'wireguard endpoint in use', [
      { field: 'route', message: 'a wireguard endpoint is in use by another running slot' },
    ]);
    expect(mapSlotErrors(err).fields.route).toMatch(/in use/);
  });

  it('joins several messages for one field', () => {
    const err = new ApiError(400, 'invalid input', [
      { field: 'port', message: 'a' },
      { field: 'port', message: 'b' },
    ]);
    expect(mapSlotErrors(err).fields.port).toBe('a; b');
  });

  it('uses the error message when there are no field errors', () => {
    expect(mapSlotErrors(new ApiError(500, 'internal error')).form).toBe('internal error');
    expect(mapSlotErrors(new Error('boom')).form).toBe('could not save the slot');
  });
});

describe('slotFormWarnings (health URL)', () => {
  const w = (healthUrl: string) => slotFormWarnings({ ...formFromSlot(view), healthUrl });
  it.each(['http://127.0.0.1/ip', 'http://[::1]/', 'http://169.254.169.254/latest', 'http://localhost/x'])('%s warns', (u) => {
    const out = w(u);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatch(/direct/i);
  });
  it.each(['https://am.i.mullvad.net/json', '', '   ', 'not a url'])('%j gives none', (u) => expect(w(u)).toEqual([]));
});

describe('openProxyWarning', () => {
  it.each(['0.0.0.0', '192.0.2.5', '::'])('%s without auth warns', (b) => {
    const out = openProxyWarning(b, false);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatch(/anyone who can reach this port/i);
    expect(out[0]).toMatch(/Block LAN targets/);
  });
  it.each(['127.0.0.1', '::1', 'localhost', '', '  '])('%j gives none', (b) => expect(openProxyWarning(b, false)).toEqual([]));
  it.each(['0.0.0.0', '192.0.2.5'])('%s with auth gives none', (b) => expect(openProxyWarning(b, true)).toEqual([]));
});

describe('Block LAN targets toggle', () => {
  it('is visible for an empty route', () => expect(showBlockLanToggle([], 'block')).toBe(true));
  it('is visible for an allow-direct slot with a route', () =>
    expect(showBlockLanToggle(['wg1'], 'allow-direct')).toBe(true));
  it('is hidden for a routed block slot', () => expect(showBlockLanToggle(['wg1', 'p1'], 'block')).toBe(false));

  it('saving sends block_lan_targets', () => {
    expect(buildSlotBody({ ...formFromSlot(view), blockLanTargets: true }).block_lan_targets).toBe(true);
    expect(buildSlotBody(formFromSlot(view)).block_lan_targets).toBe(false);
  });

  it('help text names port forwards and open-proxy binds and says WG/proxy routes exit remotely', () => {
    expect(BLOCK_LAN_HELP).toMatch(/port forward/i);
    expect(BLOCK_LAN_HELP).toMatch(/open[- ]proxy|without SOCKS auth/i);
    expect(BLOCK_LAN_HELP).toMatch(/WireGuard/);
    expect(BLOCK_LAN_HELP).toMatch(/exit .*remote|remote end/i);
  });

  it('the open-proxy warning names the toggle by its label and the toggle id is stable', () => {
    expect(openProxyWarning('0.0.0.0', false)[0]).toContain(`"${BLOCK_LAN_LABEL}"`);
    expect(blockLanToggleId(2)).toBe('slot-2-block_lan_targets');
  });
});
