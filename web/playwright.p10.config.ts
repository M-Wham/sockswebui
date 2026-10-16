import base from './playwright.config';
import { defineConfig } from '@playwright/test';

// Exit checks: `npm run p10-secrets-exit`. Own config; one --dev-testrig instance
// (global-setup.ts exports its URL as SMOKE_BASE_URL).
export default defineConfig({ ...base, testMatch: /p10-.*\.check\.ts/ });
