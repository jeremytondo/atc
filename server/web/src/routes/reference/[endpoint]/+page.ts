// The endpoint key is a runtime path param, so this route cannot be prerendered
// (the global +layout.ts sets prerender = true). The static adapter's fallback
// serves it as a client-rendered SPA route, matching sessions/[id].
export const prerender = false;
export const ssr = false;
