// Browser acceptance lane. Runs against a live stack (make local-up) using
// the locally installed Chrome, so no browser download is needed.
//   KIONGA_URL=http://localhost:18080 npx playwright test
const {defineConfig} = require('@playwright/test');

module.exports = defineConfig({
  testDir: 'tests/e2e',
  testMatch: '*.spec.cjs',
  timeout: 90_000,
  expect: {timeout: 15_000},
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', {open: 'never', outputFolder: 'artifacts/playwright-report'}]],
  outputDir: 'artifacts/playwright-results',
  use: {
    baseURL: process.env.KIONGA_URL || 'http://localhost:18080',
    channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {name: 'desktop', use: {viewport: {width: 1440, height: 900}}},
    {name: 'narrow', use: {viewport: {width: 390, height: 844}, isMobile: false, hasTouch: true}},
  ],
});
