import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { APP_NAME } from './appName';

describe('scaffold', () => {
  it('exposes the app name', () => {
    expect(APP_NAME).toBe('sockswebui');
  });

  it('index.html title contains sockswebui without JS', () => {
    const html = readFileSync(fileURLToPath(new URL('../index.html', import.meta.url)), 'utf8');
    expect(html).toMatch(/<title>[^<]*sockswebui[^<]*<\/title>/);
  });
});
