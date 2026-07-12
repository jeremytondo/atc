// Coverage guard for the API reference: every route named in the shared
// contract fixtures must have an ENDPOINTS entry. Combined with the Go
// server's TestContractFixturesCoverEveryRoute (routes → fixtures), this
// closes the chain routes → fixtures → reference, so no endpoint can be
// silently omitted from the docs console.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { ENDPOINTS } from './endpoints';

const fixturesDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '../../../../../packages/contracts/fixtures'
);

function fixtureRoutes(): string[] {
  const routes = new Set<string>();
  for (const entry of readdirSync(fixturesDir)) {
    if (!entry.endsWith('.json')) continue;
    const fixture = JSON.parse(readFileSync(join(fixturesDir, entry), 'utf8')) as {
      routes?: string[];
    };
    for (const route of fixture.routes ?? []) routes.add(route);
  }
  return [...routes].sort();
}

// Fixture routes are spelled "GET /sessions/{id}"; ENDPOINTS paths carry the
// /api prefix and the same literal {placeholders}.
function hasEndpointFor(route: string): boolean {
  const [method, path] = route.split(' ');
  return ENDPOINTS.some((ep) => ep.method === method && ep.path === `/api${path}`);
}

describe('reference coverage', () => {
  it('documents every route named in the contract fixtures', () => {
    const missing = fixtureRoutes().filter((route) => !hasEndpointFor(route));
    expect(missing).toEqual([]);
  });

  it('does not document routes absent from the fixtures', () => {
    const routes = new Set(fixtureRoutes());
    const stale = ENDPOINTS.map((ep) => `${ep.method} ${ep.path.replace(/^\/api/, '')}`).filter(
      (route) => !routes.has(route)
    );
    expect(stale).toEqual([]);
  });
});
