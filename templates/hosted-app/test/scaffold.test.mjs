import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { cpSync, mkdtempSync, readFileSync, writeFileSync, mkdirSync, symlinkSync, lstatSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const scaffold = fileURLToPath(new URL('../scaffold.mjs', import.meta.url));
const host = 'aaaaaaaa-1111-4111-8111-111111111111';
const app = 'bbbbbbbb-2222-4222-a222-222222222222';
const title = 'A "title" </script><img src=x onerror=alert(1)>';
const env = { ...process.env, PATH: `${dirname(process.execPath)}:${process.env.PATH}` };
function temporary(t) {
  const root = mkdtempSync(join(tmpdir(), 'hosted-app-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}
function args(dest, overrides = {}) {
  const inputs = { destination: dest, title, 'server-id': host, 'app-id': app, ...overrides };
  return [scaffold, ...Object.entries(inputs).flatMap(([k, v]) => [`--${k}`, v])];
}
function run(command, argv, cwd, expected = 0) {
  const result = spawnSync(command, argv, { cwd, env, encoding: 'utf8', timeout: 120000 });
  assert.equal(result.error, undefined);
  if (expected === 0) assert.equal(result.status, 0, result.stdout + result.stderr);
  else assert.notEqual(result.status, 0, result.stdout + result.stderr);
  return result;
}
function json(path) { return JSON.parse(readFileSync(path, 'utf8')); }

test('manifest matches the Go contract and preserves caller identities/data', (t) => {
  const root = temporary(t);
  for (const version of [undefined, '2147483647.0.1']) {
    const dest = join(root, version ? 'custom' : 'default');
    run(process.execPath, args(dest, version ? { 'release-version': version } : {}));
    const manifest = json(join(dest, 'app.json'));
    assert.equal(manifest.server_id, host);
    assert.equal(manifest.app_id, app);
    assert.equal(manifest.title, title);
    assert.equal(manifest.release_version, version ?? '1.0.0');
    for (const key of ['server_id', 'app_id', 'title', 'release_version']) delete manifest[key];
    const validator = readFileSync(new URL('../../../internal/apps/manifest.go', import.meta.url), 'utf8');
    assert.deepEqual(manifest, JSON.parse(validator.match(/const fixedManifest = `([^`]+)`/)[1]));
    assert.equal(json(join(dest, 'src/web/title.json')).title, title);
    assert.equal(lstatSync(join(dest, 'sql/migrations')).isDirectory(), true);
  }
});

test('invalid inputs fail before writing', async (t) => {
  const root = temporary(t);
  const invalid = [
    ['title', ''], ['title', ' leading'], ['title', 'trailing\u0085'], ['title', '\u0001'],
    ['title', 'é'.repeat(65)], ['title', '\u007f'], ['title', '\u009f'], ['title', 'trailing '], ['title', ' '],
    ['server-id', host.toUpperCase()], ['app-id', app.toUpperCase()],
    ['server-id', host.replace('-4111-', '-1111-')], ['app-id', app.replace('-a222-', '-c222-')],
    ['release-version', '01.0.0'], ['release-version', '1.2'], ['release-version', '1.2.3-rc'],
    ['release-version', '2147483648.0.0'], ['release-version', '1.0.0\n'], ['release-version', '-1.0.0'], ['release-version', '1.0.0.0'],
  ];
  for (const [field, value] of invalid) {
    const dest = join(root, 'invalid');
    const result = run(process.execPath, args(dest, { [field]: value }), undefined, 1);
    assert.match(result.stderr, new RegExp(field));
    assert.deepEqual(readdirSync(root), []);
  }
  assert.match(run(process.execPath, args(join(root, 'missing/child')), undefined, 1).stderr, /destination/);
  assert.deepEqual(readdirSync(root), []);
  const { scaffold: create } = await import('../scaffold.mjs');
  for (const title of ['\u0000', '\ud800']) {
    assert.throws(() => create({ destination: join(root, 'invalid'), title, 'server-id': host, 'app-id': app }), /title/);
    assert.deepEqual(readdirSync(root), []);
  }
  for (const accepted of ['é'.repeat(64), '\ufeffliteral\ufeff', '0.0.0']) {
    const dest = join(root, `valid-${readdirSync(root).length}`);
    run(process.execPath, args(dest, { title: accepted }));
  }
  run(process.execPath, [scaffold, '--unknown'], undefined, 1);
  run(process.execPath, [scaffold], undefined, 1);
});

test('existing destinations are untouched', (t) => {
  const root = temporary(t);
  const target = join(root, 'target');
  mkdirSync(target);
  writeFileSync(join(target, 'sentinel'), 'private');
  const directory = join(root, 'directory');
  mkdirSync(directory);
  writeFileSync(join(directory, 'sentinel'), 'keep');
  const file = join(root, 'file');
  writeFileSync(file, 'keep');
  const live = join(root, 'live');
  const dangling = join(root, 'dangling');
  symlinkSync(target, live);
  symlinkSync(join(root, 'absent'), dangling);
  for (const dest of [directory, file, live, dangling]) {
    for (const path of [dest, `${dest}/`]) {
      assert.match(run(process.execPath, args(path), undefined, 1).stderr, /destination/);
    }
  }
  assert.equal(readFileSync(file, 'utf8'), 'keep');
  assert.equal(readFileSync(join(directory, 'sentinel'), 'utf8'), 'keep');
  assert.deepEqual(readdirSync(target), ['sentinel']);
  assert.equal(readFileSync(join(target, 'sentinel'), 'utf8'), 'private');
  assert.equal(lstatSync(dangling).isSymbolicLink(), true);
  assert.equal(lstatSync(live).isSymbolicLink(), true);
});

test('concurrent destination creation has one owner', async (t) => {
  const dest = join(temporary(t), 'app');
  const results = await Promise.all([1, 2].map(() => new Promise((resolve, reject) => {
    const child = spawn(process.execPath, args(dest), { env, stdio: 'ignore', timeout: 10000 });
    child.on('error', reject);
    child.on('close', resolve);
  })));
  assert.deepEqual(results.sort(), [0, 1]);
  assert.equal(json(join(dest, 'app.json')).title, title);
});

test('fresh generated app passes the toolchain', (t) => {
  const dest = join(temporary(t), 'app');
  run(process.execPath, args(dest));
  run('npm', ['ci', '--no-audit', '--no-fund'], dest);
  run('npm', ['run', 'typecheck'], dest);
  for (const source of ['src/web/App.tsx', 'src/service/main.ts']) {
    const path = join(dest, source);
    const original = readFileSync(path, 'utf8');
    writeFileSync(path, original + '\nconst typeError: number = "bad";\n');
    assert.match(run('npm', ['run', 'typecheck'], dest, 1).stdout, /TS2322/);
    writeFileSync(path, original);
  }
  run('npm', ['test'], dest);
  for (const path of ['.env', 'secret.txt', 'sql/private.sqlite', 'src/service/secret.txt']) {
    writeFileSync(join(dest, path), 'PRIVATE_SENTINEL');
  }
  mkdirSync(join(dest, 'dist/web'), { recursive: true });
  writeFileSync(join(dest, 'dist/web/stale.env'), 'PRIVATE_SENTINEL');
  run('npm', ['run', 'build'], dest);
  const files = readdirSync(join(dest, 'dist/web'), { recursive: true }).sort();
  assert.deepEqual(files, ['assets', 'assets/main.css', 'assets/main.js', 'index.html']);
  const html = readFileSync(join(dest, 'dist/web/index.html'), 'utf8');
  assert.match(html, /src="\/assets\/main.js"/);
  assert.match(html, /href="\/assets\/main.css"/);
  assert.doesNotMatch(html, /<script[^>]*>[^<]+|style=|onerror=|<img/);
  for (const file of files.filter((f) => f !== 'assets')) {
    assert.doesNotMatch(readFileSync(join(dest, 'dist/web', file), 'utf8'), /PRIVATE_SENTINEL/);
  }
  const first = files.filter((f) => f !== 'assets').map((f) => readFileSync(join(dest, 'dist/web', f)));
  run('npm', ['run', 'build'], dest);
  const second = files.filter((f) => f !== 'assets').map((f) => readFileSync(join(dest, 'dist/web', f)));
  assert.deepEqual(second, first);
  const release = join(dirname(dest), 'release');
  mkdirSync(release);
  cpSync(join(dest, 'dist'), join(release, 'dist'), { recursive: true });
  cpSync(join(dest, 'sql/migrations'), join(release, 'sql/migrations'), { recursive: true });
  writeFileSync(join(release, 'package.json'), JSON.stringify({ type: 'module' }));
  for (const entry of ['main', 'migrate']) {
    const result = run(process.execPath, [`dist/service/${entry}.js`], release, 1);
    assert.match(result.stderr, /unfinished-service/);
    assert.equal(result.stdout, '');
  }
  if (process.env.HOSTED_APP_UNSUPPORTED_NODE) {
    const unsupported = process.env.HOSTED_APP_UNSUPPORTED_NODE;
    const npmCLI = process.env.HOSTED_APP_NPM_CLI;
    const marker = join(dest, 'node_modules/runtime-marker');
    writeFileSync(marker, 'keep-before-install');
    for (const command of [['ci'], ['run', 'build']]) {
      const result = run(unsupported, [npmCLI, ...command], dest, 1);
      assert.match(result.stderr, /24\.15\.0|EBADDEVENGINES|EBADENGINE/);
      assert.equal(readFileSync(marker, 'utf8'), 'keep-before-install');
    }
    assert.match(run(unsupported, ['scripts/build.mjs'], dest, 1).stderr, /24\.15\.0/);
    assert.deepEqual(readFileSync(join(dest, 'dist/web/index.html')), second.at(-1));
    for (const entry of ['main', 'migrate']) {
      assert.match(run(unsupported, [`dist/service/${entry}.js`], dest, 1).stderr, /24\.15\.0/);
    }
  }
});
