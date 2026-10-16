import base from './playwright.config';
import { defineConfig } from '@playwright/test';

// Exit checks: `npm run p9-exit`. Own config and own two-instance global setup.
export default defineConfig({
  ...base,
  testMatch: /p9-.*\.check\.ts/,
  globalSetup: './tests/global-setup-p9.ts',
});
