import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '+page.svelte'), 'utf8');

// Coarse source-level guard (no component render harness in this repo): the
// admin page must render action IDs and offer a clipboard copy for them.
describe('actions page IDs', () => {
  it('renders action IDs with a clipboard control', () => {
    expect(source).toContain('{action.id}');
    expect(source).toContain('navigator.clipboard.writeText');
  });
});
