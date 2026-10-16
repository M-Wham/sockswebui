import { describe, expect, it } from 'vitest';
import { killswitchBadge, stateBadge } from './badges';

describe('stateBadge', () => {
  it.each(['DEGRADED', 'ERROR'] as const)('%s is danger with explanation text', (st) => {
    const b = stateBadge(st, 'block');
    expect(b.cls).toBe('danger');
    expect(b.label).toBe(st);
    expect(b.explain.length).toBeGreaterThan(10);
  });

  it('DEGRADED with block says connections are refused', () => {
    expect(stateBadge('DEGRADED', 'block').explain).toMatch(/refused/i);
  });

  it('DEGRADED with allow-direct says traffic uses the device route', () => {
    expect(stateBadge('DEGRADED', 'allow-direct').explain).toMatch(/device route/i);
  });

  it('UP is ok, STARTING and STOPPED are not danger', () => {
    expect(stateBadge('UP', 'block').cls).toBe('ok');
    expect(stateBadge('STARTING', 'block').cls).toBe('neutral');
    expect(stateBadge('STOPPED', 'block').cls).toBe('neutral');
  });

  it('unknown state is labelled honestly, never UP', () => {
    const b = stateBadge(null, 'block');
    expect(b.label).toBe('UNKNOWN');
    expect(b.cls).toBe('neutral');
  });
});

describe('killswitchBadge', () => {
  it('allow-direct is a red badge with the fallback text', () => {
    const b = killswitchBadge('allow-direct');
    expect(b.cls).toBe('danger');
    expect(b.label).toBe('allow-direct: falls back to device route when degraded');
  });

  it('block is neutral', () => {
    const b = killswitchBadge('block');
    expect(b.cls).toBe('neutral');
    expect(b.label).toMatch(/block/);
  });
});
