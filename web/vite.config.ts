import { sveltekit } from '@sveltejs/kit/vite';
import adapter from '@sveltejs/adapter-static';
import { defineConfig } from 'vitest/config';

// SvelteKit 3 configures the app from here rather than from svelte.config.js.
export default defineConfig(async () => ({
  plugins: [
    await sveltekit({
      adapter: adapter({
        pages: 'build',
        assets: 'build',
        fallback: 'index.html',
        // Precompressed at build time: the binary carries the .br and .gz copies, so the server never
        // spends CPU compressing a file that has not changed (docs/STANDARDS.md 7).
        precompress: true,
      }),
      csp: {
        // Hash mode: SvelteKit adds a hash for every inline script it emits, so the app runs no inline
        // script and loads nothing from a third-party origin.
        mode: 'hash',
        directives: {
          'default-src': ['self'],
          'script-src': ['self'],
          'style-src': ['self', 'unsafe-inline'],
          'img-src': ['self', 'data:'],
          'font-src': ['self'],
          'connect-src': ['self', 'ws:', 'wss:'],
          'base-uri': ['self'],
          'form-action': ['self'],
          'frame-ancestors': ['none'],
          'object-src': ['none'],
        },
      },
    }),
  ],
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
}));
