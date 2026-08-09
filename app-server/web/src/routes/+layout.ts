// Every route is prerendered to a real HTML file (no SPA fallback), and
// rendering happens client-side only — the pages talk to the live server
// with relative fetches after load.
export const prerender = true
export const ssr = false
