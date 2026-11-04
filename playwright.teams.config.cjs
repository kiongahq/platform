// Isolated console acceptance lane. Playwright owns and stops both servers.
process.env.KIONGA_TEAMS_MOCK = '1';
const {defineConfig} = require('@playwright/test');
const base = require('./playwright.config.cjs');
module.exports = defineConfig({
  ...base,
  testMatch: 'agent-teams.spec.cjs',
  projects: base.projects.filter(project => project.name !== 'webkit'),
  use: {...base.use, baseURL: 'http://127.0.0.1:3417'},
  webServer: [
    {
      command: 'node frontend/tests/fixtures/teams-mock-gateway.mjs 18917',
      url: 'http://127.0.0.1:18917/api/v1/agent-definitions',
      reuseExistingServer: false,
      timeout: 30_000,
      gracefulShutdown: {signal: 'SIGTERM', timeout: 5_000},
    },
    {
      command: 'npm run dev --prefix frontend -- --hostname 127.0.0.1 --port 3417',
      env: {KIONGA_GATEWAY_URL: 'http://127.0.0.1:18917'},
      url: 'http://127.0.0.1:3417/login',
      reuseExistingServer: false,
      timeout: 120_000,
      gracefulShutdown: {signal: 'SIGTERM', timeout: 5_000},
    },
  ],
});
