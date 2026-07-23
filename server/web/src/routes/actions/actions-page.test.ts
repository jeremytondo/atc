import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '+page.svelte'), 'utf8');

describe('actions page IDs', () => {
  it('renders every action ID and provides a clipboard control for it', () => {
    expect(source).toContain('{action.id}</code>');
    expect(source).toContain('navigator.clipboard.writeText(id)');
    expect(source).toContain('aria-label={`Copy action ID ${action.id}`}');
  });
});
