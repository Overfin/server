import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Two pages under /admin/: the control panel (index.html) and the first-run
// setup (setup.html). They're built into the Go package that serves them
// (internal/admin), which embeds them: `go build` needs no Node.
export default defineConfig({
  base: '/admin/',
  plugins: [svelte()],
  build: {
    outDir: '../internal/admin/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    sourcemap: false,
    rolldownOptions: {
      input: { panel: 'index.html', setup: 'setup.html' },
      // Characters outside ASCII written as escapes: the repository's
      // check refuses invisible ones (Svelte's runtime has U+FEFF).
      output: { charset: 'ascii' },
    },
  },
  server: {
    // npm run dev: the API is a local Overfin's.
    proxy: { '/admin/api': 'http://localhost:8080', '/admin/login': 'http://localhost:8080', '/admin/auth': 'http://localhost:8080' },
  },
});
