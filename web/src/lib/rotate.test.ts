import { describe, expect, it } from 'vitest';
import type { EndpointView } from './endpointFilter';
import { affectedSlots, lastTemplateHop, retryWaitText, rotateDialogText } from './rotate';
import type { SlotConfig } from './slotsStore';

const slot = (id: number, name: string, route: string[], killswitch = 'block'): SlotConfig => ({
  id, configured: true, name, enabled: true, bind_addr: '127.0.0.1', port: 1080 + id, route, killswitch,
});

describe('affectedSlots', () => {
  it('derives slots from routes containing the endpoint', () => {
    const slots = [slot(1, 'a', ['wg', 'px']), slot(2, 'b', ['wg']), slot(3, '', ['px'])];
    expect(affectedSlots('px', slots).map((s) => s.id)).toEqual([1, 3]);
    expect(affectedSlots('none', slots)).toEqual([]);
  });
});

describe('rotateDialogText', () => {
  it('matches the documented text without allow-direct', () => {
    const t = rotateDialogText('Proxy', [slot(1, 'alpha', ['px']), slot(2, 'beta', ['px'])]);
    expect(t.message).toBe(
      'Get a new IP for Proxy? Slots alpha, beta will reconnect and open connections will drop. Some providers bill per IP.',
    );
    expect(t.warnings).toEqual([]);
  });
  it('adds the allow-direct line only for allow-direct slots', () => {
    const t = rotateDialogText('Proxy', [slot(1, 'alpha', ['px']), slot(2, 'beta', ['px'], 'allow-direct')]);
    expect(t.warnings).toEqual(["If the provider rejects the new session, slot beta will use this device's own connection."]);
  });
  it('falls back to Slot N for unnamed slots', () => {
    expect(rotateDialogText('P', [slot(3, '', ['px'])]).message).toContain('Slots Slot 3 will');
  });
});

describe('retryWaitText', () => {
  it('derives the wait from Retry-After', () => {
    expect(retryWaitText(5)).toBe('New IP was just requested. Try again in 5 seconds.');
    expect(retryWaitText(1)).toBe('New IP was just requested. Try again in 1 second.');
    expect(retryWaitText(undefined)).toBe('New IP was just requested. Try again in a few seconds.');
  });
});

describe('lastTemplateHop', () => {
  const eps = [
    { id: 'wg', name: 'wg', kind: 'wireguard' },
    { id: 't1', name: 't1', kind: 'socks5', session: { charset: 'alnum', length: 8 } },
    { id: 't2', name: 't2', kind: 'http', session: { charset: 'digits', length: 8 } },
    { id: 'plain', name: 'plain', kind: 'socks5' },
  ] as EndpointView[];
  it('returns the last template endpoint in the route', () => {
    expect(lastTemplateHop(['wg', 't1', 't2', 'plain'], eps)?.id).toBe('t2');
    expect(lastTemplateHop(['wg', 'plain'], eps)).toBeNull();
  });
});
