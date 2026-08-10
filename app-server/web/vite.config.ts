import { sveltekit } from "@sveltejs/kit/vite"
import { defineConfig } from "vite"

// The dev server proxies API traffic to a locally running App Server so the
// UI stays same-origin in both dev and production. Start one with
// `mise run -C app-server dev` before `mise run -C app-server web:dev`.
export default defineConfig({
  plugins: [sveltekit()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:7332",
      "/openapi.json": "http://127.0.0.1:7332",
    },
  },
})
