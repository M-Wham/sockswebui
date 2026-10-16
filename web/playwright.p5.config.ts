import base from './playwright.config';
import { defineConfig } from '@playwright/test';

// Exit checks (keyboard-only, 360px): `npm run p5-exit`. Kept out of `npm run smoke`.
export default defineConfig({ ...base, testMatch: /p5-.*\.check\.ts/ });
