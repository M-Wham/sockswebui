import { describe, expect, it } from 'vitest';
import { serviceLine, versionLabel } from './systemInfo';

describe('versionLabel', () => {
  it.each([
    ['1.0.0', 'v1.0.0'],
    ['v1.0.0', 'v1.0.0'],
    ['dev', 'dev'],
    [undefined, 'version unknown'],
    [null, 'version unknown'],
    ['', 'version unknown'],
    [42, 'version unknown'],
  ])('%j -> %s', (v, want) => {
    expect(versionLabel(v)).toBe(want);
  });
});

describe('serviceLine', () => {
  it('maps tri-state', () => {
    expect(serviceLine(true)).toBe('Service: running');
    expect(serviceLine(false)).toBe('Service: unreachable');
    expect(serviceLine(null)).toBe('Service: checking…');
  });
});
