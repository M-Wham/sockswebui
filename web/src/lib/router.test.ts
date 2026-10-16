import { describe, expect, it } from 'vitest';
import { hrefFor, parseRoute } from './router';

describe('parseRoute', () => {
  const cases: [string, ReturnType<typeof parseRoute>][] = [
    ['#/', { name: 'dashboard' }],
    ['', { name: 'dashboard' }],
    ['#/endpoints', { name: 'endpoints' }],
    ['#/slots/2', { name: 'slot', id: 2 }],
    ['#/slots/1', { name: 'slot', id: 1 }],
    ['#/slots/4', { name: 'slot', id: 4 }],
    ['#/logs', { name: 'logs' }],
    ['#/settings', { name: 'settings' }],
    ['#/login', { name: 'login' }],
    ['#/setup', { name: 'setup' }],
    ['#/nope', { name: 'dashboard' }],
    ['#/slots/9', { name: 'dashboard' }],
    ['#/slots/abc', { name: 'dashboard' }],
    ['#/slots', { name: 'dashboard' }],
    ['#/endpoints/extra', { name: 'endpoints', id: 'extra' }],
    ['#/endpoints/a/b', { name: 'dashboard' }],
    ['garbage', { name: 'dashboard' }],
    ['#/import', { name: 'import' }],
    ['#/import/x', { name: 'dashboard' }],
  ];
  for (const [hash, want] of cases) {
    it(`${JSON.stringify(hash)} → ${JSON.stringify(want)}`, () => {
      expect(parseRoute(hash)).toEqual(want);
    });
  }
});

describe('hrefFor', () => {
  it('round-trips through parseRoute', () => {
    for (const r of [
      { name: 'dashboard' },
      { name: 'endpoints' },
      { name: 'slot', id: 3 },
      { name: 'logs' },
      { name: 'settings' },
      { name: 'login' },
      { name: 'setup' },
      { name: 'import' },
    ] as const) {
      expect(parseRoute(hrefFor(r))).toEqual(r);
    }
  });
});
