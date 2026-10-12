import '../src/service/runtime.mjs';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { setTimeout as pause } from 'node:timers/promises';
import { chromium } from 'playwright-core';

const [replacement, data, output] = process.argv.slice(2).map((value) => resolve(value));
if (!replacement || !data || !output || !process.env.HOSTED_APP_BROWSER) throw new Error('Requires replacement release, external data, artifact directory and HOSTED_APP_BROWSER.');
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; worker-src 'none'";
const headers = { 'Content-Security-Policy': CSP, 'X-Content-Type-Options': 'nosniff', 'Cache-Control': 'no-store' };
const report = { browser: '', executed: 0, passed: 0, skipped: 0, checks: [], commands: [], measurements: [], requests: [] };
let browser, service, fixture, root = process.cwd(), mode = 'normal', postCount = 0, releaseGate;
const faults = [];
function check(name, assertion) {
  report.executed++;
  assertion(); report.passed++; report.checks.push(name);
  console.log(`PASS ${name}`);
}
async function until(predicate, label) {
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) { if (await predicate()) return; await pause(25); }
  throw new Error(`Deadline exceeded: ${label}`);
}
async function bind(server) {
  await new Promise((yes, no) => { server.once('error', no); server.listen(0, '127.0.0.1', yes); });
  return server.address().port;
}
const reserve = createServer();
const port = await bind(reserve);
await new Promise((yes) => reserve.close(yes));
const upstream = `http://127.0.0.1:${port}`;
function environment() {
  const app = JSON.parse(readFileSync(join(root, 'app.json'), 'utf8'));
  return { PATH: process.env.PATH, NODE_ENV: 'production', PYRY_APP_ID: app.app_id, PYRY_SERVER_ID: app.server_id,
    PYRY_APP_RELEASE: app.release_version, PYRY_APP_BIND_HOST: '127.0.0.1', PYRY_APP_PORT: String(port),
    PYRY_APP_DATA_DIR: data, PYRY_APP_DB_PATH: join(data, 'app.sqlite') };
}
function command(entry) {
  const r = spawnSync(process.execPath, [`dist/service/${entry}.js`], { cwd: root, env: environment(), encoding: 'utf8', timeout: 10000 });
  report.commands.push({ command: [process.execPath, `dist/service/${entry}.js`], entry, root, status: r.status, stdout: r.stdout, stderr: r.stderr });
  assert.equal(r.error, undefined); assert.equal(r.status, 0, r.stderr);
}
async function stop() {
  if (!service) return;
  const child = service; service = undefined;
  child.kill('SIGTERM');
  try { await until(() => child.exitCode !== null || child.signalCode !== null, 'service termination'); }
  catch { child.kill('SIGKILL'); await until(() => child.signalCode !== null, 'service kill'); }
}
async function start() {
  command('migrate');
  const { PATH, ...startup } = environment();
  report.commands.push({ command: [process.execPath, 'dist/service/main.js'], root, environment: startup });
  service = spawn(process.execPath, ['dist/service/main.js'], { cwd: root, env: environment(), stdio: ['ignore', 'ignore', 'pipe'] });
  service.on('error', (error) => faults.push(error.message));
  service.stderr.on('data', (chunk) => report.commands.push({ entry: 'service stderr', text: String(chunk) }));
  await until(async () => { try { return (await fetch(`${upstream}/_pyry/health`, { signal: AbortSignal.timeout(500) })).status === 200; } catch { return false; } }, 'service readiness');
  command('health');
  const health = await (await fetch(`${upstream}/_pyry/health`, { signal: AbortSignal.timeout(1000) })).json();
  check(`health ${environment().PYRY_APP_RELEASE}`, () => assert.deepEqual(health,
    { ready: true, app_id: environment().PYRY_APP_ID, release_version: environment().PYRY_APP_RELEASE }));
  const assetResponse = await fetch(`${upstream}/`, { signal: AbortSignal.timeout(1000) });
  check('production service is API/health only', () => assert.equal(assetResponse.status, 404));
}
async function pageFor(width, height = 800) {
  const context = await browser.newContext({ viewport: { width, height }, hasTouch: true });
  await context.addInitScript(() => {
    window.smokeViolations = [];
    window.smokeRequests = [];
    document.addEventListener('securitypolicyviolation', (event) => window.smokeViolations.push(event.violatedDirective));
    const original = window.fetch;
    window.fetch = (url, options) => { window.smokeRequests.push({ url, credentials: options?.credentials, method: options?.method ?? 'GET', body: options?.body }); return original(url, options); };
  });
  const page = await context.newPage();
  const responses = [];
  page.on('response', (response) => responses.push({ url: response.url(), headers: response.headers() }));
  page.setDefaultTimeout(5000);
  page.on('pageerror', (error) => faults.push(error.message));
  const response = await page.goto(`http://127.0.0.1:${fixture.address().port}`, { timeout: 10000 });
  check('exact CSP and nosniff', () => { assert.equal(response.headers()['content-security-policy'], CSP); assert.equal(response.headers()['x-content-type-options'], 'nosniff'); });
  await page.getByRole('status').filter({ hasText: /Preferences loaded|Could not load/ }).waitFor();
  const title = await page.locator('h1').textContent();
  const live = await page.getByRole('status').getAttribute('aria-live');
  check('literal title / polite feedback', () => { assert.equal(title, 'Preference example <literal>'); assert.equal(live, 'polite'); });
  check('built asset/API response policy', () => {
    assert.ok(responses.some((r) => r.url.endsWith('/assets/main.js')));
    assert.ok(responses.some((r) => r.url.endsWith('/assets/main.css')));
    assert.ok(responses.some((r) => r.url.endsWith('/api/preferences')));
    for (const r of responses) { assert.equal(r.headers['content-security-policy'], CSP); assert.equal(r.headers['x-content-type-options'], 'nosniff'); }
  });
  return { context, page };
}
async function screenshot(page, name) { await page.screenshot({ path: join(output, `${name}.png`), fullPage: true }); }
async function settings(page, tab, period) {
  assert.equal(await page.getByRole('button', { name: tab, exact: true }).getAttribute('aria-pressed'), 'true');
  assert.equal(await page.getByLabel('Period', { exact: true }).inputValue(), period);
  const state = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth > innerWidth, violations: window.smokeViolations }));
  assert.equal(state.overflow, false); assert.deepEqual(state.violations, []);
}
try {
  browser = await chromium.launch({ executablePath: process.env.HOSTED_APP_BROWSER, headless: true, timeout: 10000 });
  report.browser = browser.version();
  await start();
  fixture = createServer({ headersTimeout: 5000, requestTimeout: 10000, keepAliveTimeout: 1000, connectionsCheckingInterval: 500 }, async (req, res) => {
    Object.entries(headers).forEach(([key, value]) => res.setHeader(key, value));
    const deadline = setTimeout(() => res.destroy(), 10000);
    res.once('close', () => clearTimeout(deadline));
    try {
      if (req.url === '/api/preferences') {
        let body = ''; for await (const chunk of req) { body += chunk; if (Buffer.byteLength(body) > 16384) throw new Error('fixture body cap'); }
        if (req.method === 'POST') postCount++;
        if (mode === 'invalid-read') { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end('{"version":2}'); return; }
        if (mode === 'read-fail' || (req.method === 'POST' && mode === 'fail')) { res.writeHead(503); res.end(); return; }
        const reply = await fetch(`${upstream}/api/preferences`, { method: req.method, body: req.method === 'POST' ? body : undefined, signal: AbortSignal.timeout(5000) });
        if (req.method === 'POST' && mode === 'uncertain') { res.destroy(); return; }
        if (req.method === 'POST' && mode === 'delay') await new Promise((yes) => { releaseGate = yes; });
        res.writeHead(req.method === 'POST' && mode === 'wrong-status' ? 200 : reply.status, { 'Content-Type': 'application/json' }); res.end(Buffer.from(await reply.arrayBuffer())); return;
      }
      const assets = { '/': ['index.html', 'text/html'], '/assets/main.js': ['assets/main.js', 'text/javascript'], '/assets/main.css': ['assets/main.css', 'text/css'] };
      const asset = assets[req.url];
      if (!asset || req.method !== 'GET') { res.writeHead(404); res.end(); return; }
      res.writeHead(200, { 'Content-Type': asset[1] }); res.end(readFileSync(join(root, 'dist/web', asset[0])));
    } catch (error) { faults.push(error.message); if (!res.destroyed) { res.writeHead(500); res.end(); } }
  });
  await bind(fixture);
  for (const width of [320, 1280]) {
    const initialPosts = postCount;
    const { context, page } = await pageFor(width);
    await settings(page, 'Overview', '24h');
    check(`defaults ${width}`, () => assert.equal(postCount, initialPosts));
    const metrics = await page.evaluate(() => {
      const styles = getComputedStyle(document.querySelector('.cards'));
      const theme = getComputedStyle(document.documentElement);
      const luminance = (variable) => {
        const hex = theme.getPropertyValue(variable).trim().slice(1);
        return [0, 2, 4].map((offset) => parseInt(hex.slice(offset, offset + 2), 16) / 255)
          .map((v) => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
          .reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
      };
      const contrast = [['--text', '--bg'], ['--text', '--panel'], ['--muted', '--bg'], ['--muted', '--panel'], ['--accent', '--soft'], ['--accent', '--bg']].map(([text, background]) => {
        const values = [luminance(text), luminance(background)].sort((a, b) => b - a);
        return { text, background, ratio: (values[0] + .05) / (values[1] + .05) };
      });
      return { width: innerWidth, height: innerHeight, overflow: document.documentElement.scrollWidth > innerWidth,
        columns: styles.gridTemplateColumns.split(' ').length, contrast, navWrap: getComputedStyle(document.querySelector('nav')).flexWrap, coarse: matchMedia('(pointer: coarse)').matches,
        controls: [...document.querySelectorAll('button, select')].map((el) => el.getBoundingClientRect().height),
        tableScroll: document.querySelector('.table-scroll').scrollWidth > document.querySelector('.table-scroll').clientWidth,
        inline: document.querySelectorAll('[style], [onclick], script:not([src])').length };
    });
    report.measurements.push(metrics);
    check(`layout/coarse controls ${width}`, () => { assert.equal(metrics.overflow, false); assert.equal(metrics.columns, width > 760 ? 3 : 1); assert.ok(metrics.coarse); assert.ok(metrics.controls.every((v) => v >= 44)); assert.equal(metrics.inline, 0); assert.equal(metrics.navWrap, 'wrap'); assert.ok(metrics.contrast.every((pair) => pair.ratio >= 4.5)); if (width === 320) assert.ok(metrics.tableScroll); });
    await screenshot(page, `layout-${width}`);
    await page.keyboard.press('Tab');
    assert.equal(await page.evaluate(() => document.activeElement.textContent), 'Overview');
    const focus = await page.evaluate(() => { const s = getComputedStyle(document.activeElement); return [s.outlineStyle, s.outlineWidth]; });
    check(`keyboard focus ${width}`, () => { assert.equal(focus[0], 'solid'); assert.equal(focus[1], '3px'); });
    await screenshot(page, `focus-${width}`);
    await page.keyboard.press('Tab'); await page.keyboard.press('Enter'); await settings(page, 'Preferences', '24h');
    await page.getByLabel('Period', { exact: true }).focus(); await page.keyboard.press('End');
    assert.equal(await page.getByLabel('Period', { exact: true }).inputValue(), '7d');
    mode = 'delay'; await page.keyboard.press('Tab'); await page.keyboard.press('Enter');
    await until(() => releaseGate !== undefined, 'delayed acknowledgment gate');
    assert.equal(await page.getByRole('status').textContent(), 'Saving…');
    assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isDisabled(), true);
    const pendingPosts = postCount;
    await page.keyboard.press('Enter'); await pause(500);
    assert.equal(await page.getByRole('status').textContent(), 'Saving…');
    check(`pending save is not duplicated ${width}`, () => assert.equal(postCount, pendingPosts));
    await screenshot(page, `saving-${width}`);
    releaseGate(); releaseGate = undefined; mode = 'normal';
    await page.getByRole('status').filter({ hasText: 'Preferences saved.' }).waitFor();
    check(`delayed 204 acknowledgment ${width}`, () => assert.ok(postCount > 0));
    await screenshot(page, `saved-${width}`);
    for (const failure of ['fail', 'uncertain', 'wrong-status']) {
      mode = failure;
      await page.getByLabel('Period', { exact: true }).selectOption('1h');
      await page.getByRole('button', { name: 'Save', exact: true }).click();
      await page.getByRole('status').filter({ hasText: 'Save not confirmed.' }).waitFor();
      const count = postCount; await pause(1000);
      check(`${failure}: visible error/no replay ${width}`, () => assert.equal(postCount, count));
      await screenshot(page, `${failure}-${width}`);
    }
    mode = 'normal'; await page.getByLabel('Period', { exact: true }).selectOption('7d');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('status').filter({ hasText: 'Preferences saved.' }).waitFor();
    const requests = await page.evaluate(() => window.smokeRequests);
    report.requests.push({ width, requests });
    check(`root-relative/credential-free complete requests ${width}`, () => {
      assert.ok(requests.length > 1); for (const r of requests) { assert.equal(r.url, '/api/preferences'); assert.equal(r.credentials, 'omit'); if (r.method === 'POST') { const record = JSON.parse(r.body); assert.deepEqual(Object.keys(record).sort(), ['period', 'selected_tab', 'version']); assert.equal(record.version, 1); assert.equal(record.selected_tab, 'preferences'); }  }
      assert.deepEqual(requests.filter((r) => r.method === 'POST').map((r) => JSON.parse(r.body).period), ['7d', '1h', '1h', '1h', '7d']);
    });
    check(`no CSP violations ${width}`, () => assert.deepEqual(faults, []));
    const violations = await page.evaluate(() => window.smokeViolations);
    check(`CSP events ${width}`, () => assert.deepEqual(violations, []));
    await context.close();
    // Restore defaults for the next layout without masking browser persistence checks.
    if (width === 320) await fetch(`${upstream}/api/preferences`, { method: 'POST', body: JSON.stringify({ version: 1, selected_tab: 'overview', period: '24h' }), signal: AbortSignal.timeout(1000) });
  }
  for (const readMode of ['read-fail', 'invalid-read']) {
    mode = readMode;
    const failed = await pageFor(320);
    const readError = await failed.page.getByRole('status').textContent();
    check(`${readMode} feedback`, () => assert.match(readError, /Could not load/));
    await screenshot(failed.page, readMode); await failed.context.close();
  }
  mode = 'normal';
  for (const transition of ['restart', 'replacement']) {
    await stop(); if (transition === 'replacement') root = replacement;
    await start();
    for (const width of [320, 1280]) {
      const { context, page } = await pageFor(width); await settings(page, 'Preferences', '7d');
      check(`${transition} fresh-client persistence ${width}`, () => assert.equal(environment().PYRY_APP_RELEASE, transition === 'replacement' ? '1.0.1' : '1.0.0'));
      await screenshot(page, `${transition}-${width}`); await context.close();
    }
  }
  check('no page/service errors', () => assert.deepEqual(faults, []));
} catch (error) { report.failure = error.stack; console.error(`browser smoke: ${error.message}`); process.exitCode = 1; }
finally {
  releaseGate?.();
  if (browser) await browser.close();
  if (fixture) { fixture.closeAllConnections(); await new Promise((yes) => fixture.close(yes)); }
  await stop();
  writeFileSync(join(output, 'browser.json'), JSON.stringify(report) + '\n');
  console.log(`Browser: executed=${report.executed} passed=${report.passed} skipped=${report.skipped}; artifacts=${output}`);
}
