// Shared browser-test helpers: sign-in for the bootstrap administrator and a
// provisioned normal user, plus layout assertions used by every audit.
const {test: base, expect, request: requestFactory} = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');

const ADMIN = {username: process.env.KIONGA_ADMIN_USER || 'admin', password: process.env.KIONGA_ADMIN_PASSWORD || 'mlaiops-local'};
const USER = {username: 'e2e-user', password: 'e2e-user-password-123'};

async function signIn(page, {username, password}, returnTo = '/console.html') {
  await page.goto(`/login.html?return_to=${encodeURIComponent(returnTo)}`);
  await page.fill('input[name="username"]', username);
  await page.fill('input[name="password"]', password);
  await Promise.all([page.waitForURL(url => !url.pathname.startsWith('/login')), page.click('button[type="submit"]')]);
}

/* Provision the normal user through the real admin API (idempotent). */
async function ensureNormalUser(baseURL, {services = ['overview', 'projects', 'pipelines', 'models', 'features', 'storage', 'catalog', 'workbench', 'ide'], projectIDs = []} = {}) {
  const context = await requestFactory.newContext({baseURL});
  const login = await context.post('/auth/local/login', {form: {...ADMIN, return_to: '/console.html'}, maxRedirects: 0});
  expect([200, 302]).toContain(login.status());
  const access = await context.put(`/api/v1/admin/users/${USER.username}`, {data: {
    email: 'e2e-user@example.com', role: 'user', services, project_ids: projectIDs,
    storage: {size_gb: 25, buckets: []},
    compute: {profile: 'starter', vcpus: 2, memory_gb: 4, gpus: 0, max_vms: 1, max_projects: 2, max_concurrent_runs: 1, max_functions: 3},
    disabled: false,
  }});
  expect(access.ok(), await access.text()).toBeTruthy();
  const password = await context.put(`/api/v1/admin/users/${USER.username}/local-password`, {data: {password: USER.password}});
  expect(password.ok(), await password.text()).toBeTruthy();
  await context.dispose();
}

function screenshotPath(testInfo, name) {
  const dir = path.join('artifacts', 'screenshots', testInfo.project.name);
  fs.mkdirSync(dir, {recursive: true});
  return path.join(dir, `${name}.png`);
}

/* Layout invariants every console page must satisfy. */
async function assertLayout(page, label) {
  const report = await page.evaluate(() => {
    const visible = element => {
      const style = getComputedStyle(element);
      const box = element.getBoundingClientRect();
      return style.visibility !== 'hidden' && style.display !== 'none' && box.width > 0 && box.height > 0 && !element.closest('[hidden],dialog:not([open])');
    };
    const root = document.documentElement;
    const overflowX = root.scrollWidth - root.clientWidth;
    const controls = [...document.querySelectorAll('main button, main a[href], main select, main input, .app-bar button, dialog[open] button, dialog[open] a[href], dialog[open] select, dialog[open] input')].filter(visible);
    const unnamed = controls.filter(control => {
      const name = (control.getAttribute('aria-label') || control.textContent || control.getAttribute('title') || control.getAttribute('placeholder') || (control.labels && control.labels[0]?.textContent) || '').trim();
      return !name;
    }).map(control => control.outerHTML.slice(0, 120));
    // Only controls a user can actually hit count: one scrolled out of view
    // inside a dialog or panel is clipped, not overlapping.
    const hittable = control => {
      const box = control.getBoundingClientRect();
      const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
      return hit && (hit === control || control.contains(hit) || hit.contains(control));
    };
    const boxes = controls.filter(control => control.matches('button, a[href]') && hittable(control)).map(control => ({control, box: control.getBoundingClientRect()}));
    const overlaps = [];
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i], b = boxes[j];
        if (a.control.contains(b.control) || b.control.contains(a.control)) continue;
        const x = Math.min(a.box.right, b.box.right) - Math.max(a.box.left, b.box.left);
        const y = Math.min(a.box.bottom, b.box.bottom) - Math.max(a.box.top, b.box.top);
        if (x > 2 && y > 2) overlaps.push(`${a.control.textContent.trim().slice(0, 30)} ⟷ ${b.control.textContent.trim().slice(0, 30)}`);
      }
    }
    // Text clipped horizontally inside a card or panel without an ellipsis.
    const clipped = [...document.querySelectorAll('main .card, main .panel, main .settings-card')].filter(visible)
      .filter(node => node.scrollWidth - node.clientWidth > 2 && getComputedStyle(node).overflowX !== 'auto')
      .map(node => (node.querySelector('h2,h3')?.textContent || node.className).trim().slice(0, 60));
    return {overflowX, unnamed, overlaps, clipped};
  });
  expect(report.overflowX, `${label}: page scrolls horizontally by ${report.overflowX}px`).toBeLessThanOrEqual(1);
  expect(report.unnamed, `${label}: controls without an accessible name`).toEqual([]);
  expect(report.overlaps, `${label}: overlapping controls`).toEqual([]);
  expect(report.clipped, `${label}: content wider than its container`).toEqual([]);
}

/* Fails the test on uncaught page errors or same-origin 5xx responses. */
const test = base.extend({
  page: async ({page}, use) => {
    const problems = [];
    page.on('pageerror', error => problems.push(`pageerror: ${error.message}`));
    page.on('response', response => {
      const url = new URL(response.url());
      if (url.origin === new URL(page.url() || 'http://x').origin && response.status() >= 500) problems.push(`${response.status()} ${url.pathname}`);
    });
    await use(page);
    expect(problems, 'browser errors').toEqual([]);
  },
});

module.exports = {test, expect, ADMIN, USER, signIn, ensureNormalUser, assertLayout, screenshotPath};
