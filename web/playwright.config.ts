import { fileURLToPath } from 'node:url';
import { defineConfig, devices } from '@playwright/test';

// The repository root, so the server command never depends on where Playwright was started from.
const repoRoot = fileURLToPath(new URL('..', import.meta.url));

/**
 * End-to-end tests drive the real binary and the world, never a live provider.
 *
 * The server is `cmd/cairn-dev`: the real service against in-process fakes, on a port of its own so a
 * developer's running copy on 8893 is never disturbed. A phone viewport is the first target, because the
 * app is used on a phone and the mockups are frames of one.
 */
const PORT = 18893;
const BASE_URL = `http://localhost:${PORT}`;

export default defineConfig({
  testDir: 'e2e',
  // One baseline per screen, browser and platform. The same fonts rasterise differently on macOS and on the
  // Linux CI runner, and the potato is Linux, so CI's copy is the one that matters. A baseline is regenerated
  // on purpose by the `baselines` workflow, never by running --update-snapshots on whatever machine is handy.
  snapshotPathTemplate:
    '{testDir}/__screenshots__/{testFileName}/{arg}-{projectName}-{platform}{ext}',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : [['list']],
  expect: {
    // A pixel diff is evidence, not proof: the Go tests carry the behavioural assertions. Keep this tight so
    // a real visual change is caught, and always look at the diff before updating a baseline.
    toHaveScreenshot: { maxDiffPixelRatio: 0.01, animations: 'disabled' },
  },
  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // A phone first, and reduced motion so a timestamp or an animation can never make a diff flaky.
    ...devices['iPhone 13'],
    viewport: { width: 390, height: 844 },
    reducedMotion: 'reduce',
    colorScheme: 'dark',
  },
  projects: [
    {
      name: 'chromium-phone',
      use: {
        ...devices['iPhone 13'],
        browserName: 'chromium',
        viewport: { width: 390, height: 844 },
      },
    },
    {
      name: 'webkit-phone',
      use: { ...devices['iPhone 13'], browserName: 'webkit' },
    },
  ],
  webServer: {
    command: 'go build -o /tmp/cairn-e2e ./cmd/cairn-dev && exec /tmp/cairn-e2e',
    cwd: repoRoot,
    url: `${BASE_URL}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    env: {
      CAIRN_ADDR: `127.0.0.1:${PORT}`,
      CAIRN_ALLOWED_HOSTS: `localhost:${PORT},127.0.0.1:${PORT}`,
      CAIRN_DEV: '1',
    },
  },
});
