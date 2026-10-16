import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  testMatch: /.*\.spec\.ts/,
  globalSetup: './tests/global-setup.ts',
  timeout: 180_000,
  workers: 1,
  fullyParallel: false,
  reporter: 'list',
  use: {
    // baseURL is chosen at runtime by global-setup; the spec reads SMOKE_BASE_URL.
    launchOptions: { executablePath: process.env.PW_CHROMIUM ?? '/opt/pw-browsers/chromium' },
  },
});
