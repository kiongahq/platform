const { defineConfig } = require('@playwright/test');
const port = process.env.KIONGA_CORE_TEST_PORT || '18195';
module.exports = defineConfig({
  testDir: 'tests/e2e',
  testMatch: 'core-backend.core.cjs',
  timeout: 45_000,
  expect: { timeout: 15_000 },
  workers: 1,
  reporter: 'list',
  outputDir: 'artifacts/core-backend',
  use: { baseURL: `http://127.0.0.1:${port}`, channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome', trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'narrow', use: { viewport: { width: 390, height: 844 } } },
  ],
  webServer: {
    command: 'node scripts/test-core-backend.mjs',
    url: `http://127.0.0.1:${port}/api/v1/health`,
    timeout: 120_000,
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
});
