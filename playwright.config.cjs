// Browser acceptance lane. Runs against a live stack (make -C ../deploy local-up) using
// the locally installed Chrome, so no browser download is needed.
//   KIONGA_URL=http://localhost:18080 npx playwright test
const fs = require('node:fs');
const {defineConfig} = require('@playwright/test');

// Sign-in also runs in WebKit (Safari's engine), which handles cookies on
// http://localhost differently from Chrome. Install it once with
// `npx playwright install webkit`; without it the project is left out.
let webkitInstalled = false;
try { webkitInstalled = fs.existsSync(require('playwright').webkit.executablePath()); } catch { /* not installed */ }

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
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {name: 'desktop', use: {channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome', viewport: {width: 1440, height: 900}}},
    {name: 'narrow', use: {channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome', viewport: {width: 390, height: 844}, isMobile: false, hasTouch: true}},
    ...(webkitInstalled ? [{name: 'webkit', testMatch: 'sso.spec.cjs', use: {browserName: 'webkit', viewport: {width: 1440, height: 900}}}] : []),
  ],
});
