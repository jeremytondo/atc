// The session id is only known at runtime, so this route cannot be prerendered
// (the global +layout.ts sets prerender = true). The static adapter's
// index.html fallback serves it as a client-rendered SPA route.
export const prerender = false;
export const ssr = false;
