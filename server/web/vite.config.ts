import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// In dev, the SvelteKit dev server serves the UI on :5173 while the Go API runs
// on the dev profile's fixed address (see tools/dev.sh, which generates
// tmp/dev/config.toml with the same value). Proxy /api to it so the frontend
// stays same-origin (no CORS).
const apiTarget = 'http://127.0.0.1:7332';

export default defineConfig({
  clearScreen: false,
  plugins: [tailwindcss(), sveltekit()],
  server: {
    proxy: {
      // ws: true forwards the WebSocket upgrade for the terminal attach endpoint
      // so the browser stays same-origin with the dev server.
      '/api': { target: apiTarget, changeOrigin: true, ws: true }
    }
  }
});
