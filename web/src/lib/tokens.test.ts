import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/**
 * The mockups are the UI spec, so the tokens are theirs. This test is the only thing stopping the app's
 * copy of the palette from quietly drifting away from the frame a designer or an agent checked by eye.
 */
const repoRoot = new URL('../../../', import.meta.url);
const mockupCSS = fileURLToPath(new URL('mockups/sepia.css', repoRoot));
const appCSS = fileURLToPath(new URL('../app.css', import.meta.url));

function rootTokens(css: string): Set<string> {
  const block = /:root\s*\{([\s\S]*?)\n\}/.exec(css);
  if (!block) throw new Error('no :root block');
  const names = [...block[1].matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1]);
  return new Set(names);
}

describe('design tokens', () => {
  const spec = rootTokens(readFileSync(mockupCSS, 'utf8'));
  const app = rootTokens(readFileSync(appCSS, 'utf8'));

  it('finds tokens in both files', () => {
    expect(spec.size).toBeGreaterThan(10);
    expect(app.size).toBeGreaterThan(10);
  });

  it('defines every token the mockups define', () => {
    const missing = [...spec].filter((name) => !app.has(name));
    expect(missing).toEqual([]);
  });

  it('defines no token the mockups do not have', () => {
    // A token invented in the app but absent from the spec is either a typo or an undiscussed design change.
    const extra = [...app].filter((name) => !spec.has(name));
    expect(extra).toEqual([]);
  });
});
