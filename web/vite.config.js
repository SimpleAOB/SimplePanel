import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/web/dist',
    // Keep dist/.gitkeep; the prebuild script clears old assets.
    emptyOutDir: false,
    assetsInlineLimit: 0,
  },
  server: {
    proxy: { '/api': 'http://localhost:3000' },
  },
});
