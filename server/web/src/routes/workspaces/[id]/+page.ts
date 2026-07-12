// The workspace id is only known at runtime, so this route cannot be
// prerendered (the global +layout.ts sets prerender = true). The static
// adapter's fallback page serves it instead.
export const prerender = false;
