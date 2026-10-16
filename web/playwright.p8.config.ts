import base from './playwright.config';
import { defineConfig } from '@playwright/test';

// Exit checks: `npm run p8-exit`. Own config so `npm run smoke` stays at 1 test.
export default defineConfig({ ...base, testMatch: /p8-.*\.check\.ts/ });
