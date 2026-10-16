import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    // dist is wiped each build; public/.keep re-creates the tracked web/dist/.keep.
    emptyOutDir: true,
    reportCompressedSize: true,
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    watch: false,
  },
});
