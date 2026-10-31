import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

/**
 * S00's end-to-end smoke. It checks the three things the skeleton promises a person: the page loads and looks
 * right on a phone, it is usable (no axe violations), and the health check reports the running version.
 */

test.describe('the walking skeleton', () => {
  test('the page loads, is styled, and does not overflow a phone', async ({ page }) => {
    await page.goto('/');

    await expect(page.getByRole('heading', { level: 1, name: 'Cairn' })).toBeVisible();

    // The tokens are applied: a dark warm background, and Fraunces on the title.
    const background = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    expect(background).not.toBe('rgb(255, 255, 255)');
    expect(background).not.toBe('rgba(0, 0, 0, 0)');

    const titleFont = await page
      .getByRole('heading', { level: 1 })
      .evaluate((el) => getComputedStyle(el).fontFamily);
    expect(titleFont.toLowerCase()).toContain('fraunces');

    const bodyFont = await page.evaluate(() => getComputedStyle(document.body).fontFamily);
    expect(bodyFont.toLowerCase()).toContain('lexend');

    // Nothing sticks out sideways: a phone is the primary screen.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });

  test('has no accessibility violations', async ({ page }) => {
    await page.goto('/');
    const results = await new AxeBuilder({ page }).analyze();
    expect(results.violations).toEqual([]);
  });

  test('the health check reports the running service', async ({ request }) => {
    const resp = await request.get('/healthz');
    expect(resp.status()).toBe(200);
    expect(resp.headers()['cache-control']).toBe('no-store');
    expect(resp.headers()['content-security-policy']).toContain("default-src 'none'");

    const body = await resp.json();
    expect(Object.keys(body).sort()).toEqual(['status', 'version']);
    expect(body.status).toBe('ok');
    expect(typeof body.version).toBe('string');
  });

  test('matches the recorded screen', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Cairn' })).toBeVisible();
    // The first baseline for the first screen. Look at the diff before you ever update this file.
    await expect(page).toHaveScreenshot('home.png', { fullPage: true });
  });
});
