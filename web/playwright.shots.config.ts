import { defineConfig } from '@playwright/test';

// README screenshots: `npm run readme-shots` writes PNGs to ../docs/images/.
// Hermetic: the testrig binary with 4 seeded rig slots; demo names only, fake keys.
process.env.SWT_RIG_SLOTS = '4';

export default defineConfig({
  testDir: './tests',
  testMatch: /readme-shots\.check\.ts/,
  globalSetup: './tests/global-setup.ts',
  timeout: 180_000,
  workers: 1,
  reporter: 'list',
  use: {
    launchOptions: { executablePath: process.env.PW_CHROMIUM ?? '/opt/pw-browsers/chromium' },
  },
});
