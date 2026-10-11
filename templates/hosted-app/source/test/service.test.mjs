import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { cpSync, mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync, statSync, symlinkSync, unlinkSync } from 'node:fs';
import { request, createServer } from 'node:http';
import { connect, createServer as tcpServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import test from 'node:test';

const defaults = { version: 1, selected_tab: 'overview', period: '24h' };
async function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'preferences-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const release = join(root, 'builds', 'first');
  mkdirSync(release, { recursive: true });
  for (const path of ['app.json', 'dist/service', 'sql/migrations']) cpSync(path, join(release, path), { recursive: true });
  writeFileSync(join(release, 'package.json'), '{"type":"module"}');
  const manifest = JSON.parse(readFileSync('app.json', 'utf8'));
  const portServer = tcpServer();
  portServer.listen(0, '127.0.0.1');
  await once(portServer, 'listening');
  const port = portServer.address().port;
  await new Promise((resolve) => portServer.close(resolve));
  const env = { ...process.env, NODE_ENV: 'production', PYRY_APP_ID: manifest.app_id,
    PYRY_SERVER_ID: manifest.server_id, PYRY_APP_RELEASE: manifest.release_version,
    PYRY_APP_DATA_DIR: join(root, 'data'), PYRY_APP_DB_PATH: join(root, 'data/app.sqlite'),
    PYRY_APP_BIND_HOST: '127.0.0.1', PYRY_APP_PORT: String(port) };
  return { root, release, env, url: `http://127.0.0.1:${port}` };
}
function run(f, entry, env = f.env, ok = true) {
  const result = spawnSync(process.execPath, [`dist/service/${entry}.js`], { cwd: f.release, env, encoding: 'utf8', timeout: 3000 });
  assert.equal(result.error, undefined);
  assert.equal(result.status === 0, ok, result.stderr);
  if (!ok) assert.ok(result.stderr.length > 0);
  return result;
}
async function start(t, f) {
  const child = spawn(process.execPath, ['dist/service/main.js'], { cwd: f.release, env: f.env, stdio: ['ignore', 'pipe', 'pipe'] });
  let errors = '';
  child.stderr.on('data', (data) => { errors += data; });
  const closed = once(child, 'close');
  t.after(async () => { if (child.exitCode === null) child.kill('SIGTERM'); await closed; });
  const deadline = Date.now() + 2500;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) assert.fail(errors);
    try { if ((await fetch(`${f.url}/_pyry/health`)).status === 200) return { child, closed }; } catch {}
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert.fail(`startup deadline: ${errors}`);
}
async function post(f, body) {
  return fetch(`${f.url}/api/preferences`, { method: 'POST', body: typeof body === 'string' ? body : JSON.stringify(body) });
}
async function saved(f) { return (await fetch(`${f.url}/api/preferences`)).json(); }
function sql(f, source) {
  const db = new DatabaseSync(f.env.PYRY_APP_DB_PATH);
  try { return db.prepare(source).all(); } finally { db.close(); }
}
function migration(f, name, source) { writeFileSync(join(f.release, 'sql/migrations', name), source); }

test('preferences replacement, validation and body boundaries', async (t) => {
  const f = await fixture(t);
  run(f, 'migrate');
  assert.equal(statSync(f.env.PYRY_APP_DATA_DIR).mode & 0o777, 0o700);
  assert.equal(statSync(f.env.PYRY_APP_DB_PATH).mode & 0o777, 0o600);
  await start(t, f);
  assert.deepEqual(await saved(f), defaults);
  const chosen = { version: 1, selected_tab: 'preferences', period: '7d' };
  assert.equal((await post(f, chosen)).status, 204);
  assert.deepEqual(await saved(f), chosen);
  const invalid = ['{', 'null', '[]', 'true', '1', '"value"', {}, { version: null }, { version: '1' }, { version: 2 },
    ...['selected_tab', 'period'].flatMap((key) => [null, 3, true, [], {}, 'bad'].map((value) => ({ version: 1, [key]: value })))];
  for (const value of invalid) {
    assert.equal((await post(f, value)).status, 400, JSON.stringify(value));
    assert.deepEqual(await saved(f), chosen);
  }
  const malformedUTF8 = Buffer.concat([Buffer.from('{\"version\":1,\"extra\":\"'), Buffer.from([255]), Buffer.from('\"}')]);
  assert.equal((await fetch(f.url + '/api/preferences', { method: 'POST', body: malformedUTF8 })).status, 400);
  assert.deepEqual(await saved(f), chosen);
  const exact = '{"version":1,"extra":"' + 'é'.repeat(8180) + '"}';
  const boundary = exact + ' '.repeat(16384 - Buffer.byteLength(exact));
  assert.equal(Buffer.byteLength(boundary), 16384);
  assert.equal((await post(f, boundary)).status, 204);
  assert.deepEqual(await saved(f), defaults);
  assert.equal((await post(f, boundary + ' ')).status, 413);
  const declared = await new Promise((resolve, reject) => {
    const req = request(f.url + '/api/preferences', { method: 'POST', headers: { 'Content-Length': '16385' } },
      (res) => { res.resume(); resolve(res.statusCode); });
    req.on('error', reject); req.flushHeaders();
  });
  assert.equal(declared, 413);
  const status = await new Promise((resolve, reject) => {
    const req = request(`${f.url}/api/preferences`, { method: 'POST' }, (res) => { res.resume(); resolve(res.statusCode); });
    req.on('error', reject);
    req.write(boundary.slice(0, 100));
    req.end(boundary.slice(100) + ' ');
  });
  assert.equal(status, 413);
  assert.deepEqual(await saved(f), defaults);
  for (const selected_tab of ['overview', 'preferences']) for (const period of ['1h', '24h', '7d']) {
    const value = { version: 1, selected_tab, period };
    assert.equal((await post(f, { ...value, ignored: { any: true } })).status, 204);
    assert.deepEqual(await saved(f), value);
  }
  const db = new DatabaseSync(f.env.PYRY_APP_DB_PATH);
  db.exec("CREATE TRIGGER refuse_save BEFORE INSERT ON preferences BEGIN SELECT RAISE(ABORT, 'blocked'); END");
  db.close();
  const before = await saved(f);
  assert.equal((await post(f, defaults)).status, 503);
  assert.deepEqual(await saved(f), before);
});

test('preferences survive restart and release replacement', async (t) => {
  const f = await fixture(t);
  run(f, 'migrate');
  let service = await start(t, f);
  const chosen = { version: 1, selected_tab: 'preferences', period: '1h' };
  assert.equal((await post(f, chosen)).status, 204);
  service.child.kill('SIGTERM'); await service.closed;
  service = await start(t, f);
  assert.deepEqual(await saved(f), chosen);
  service.child.kill('SIGTERM'); await service.closed;
  const replacement = join(f.root, 'builds/second');
  cpSync(f.release, replacement, { recursive: true });
  f.release = replacement; f.env.PYRY_APP_RELEASE = '1.0.1';
  const manifest = JSON.parse(readFileSync(join(replacement, 'app.json'), 'utf8'));
  writeFileSync(join(replacement, 'app.json'), JSON.stringify({ ...manifest, release_version: '1.0.1' }));
  run(f, 'migrate'); await start(t, f);
  assert.deepEqual(await saved(f), chosen);
});

test('migration ordering, ledger validation and atomic rollback', async (t) => {
  const f = await fixture(t);
  migration(f, '0003_second.sql', 'INSERT INTO ordered VALUES (2);');
  migration(f, '0002_first.sql', 'CREATE TABLE ordered (n INTEGER); INSERT INTO ordered VALUES (1);');
  run(f, 'migrate'); run(f, 'migrate');
  assert.deepEqual(sql(f, 'SELECT n FROM ordered').map((r) => r.n), [1, 2]);
  assert.deepEqual(sql(f, 'SELECT version FROM pyry_schema_migrations').map((r) => r.version), [1, 2, 3]);
  assert.equal(sql(f, 'SELECT sha256 FROM pyry_schema_migrations')[0].sha256,
    createHash('sha256').update(readFileSync(join(f.release, 'sql/migrations/0001_preferences.sql'))).digest('hex'));
  migration(f, '0004_pending.sql', 'CREATE TABLE pending (n INTEGER);');
  const original = readFileSync(join(f.release, 'sql/migrations/0002_first.sql'));
  migration(f, '0002_first.sql', 'SELECT 1;'); run(f, 'migrate', f.env, false);
  assert.equal(sql(f, "SELECT name FROM sqlite_master WHERE name='pending'").length, 0);
  unlinkSync(join(f.release, 'sql/migrations/0002_first.sql')); run(f, 'migrate', f.env, false);
  migration(f, '0002_first.sql', original);
  migration(f, '0005_failure.sql', 'CREATE TABLE transient (n INTEGER); INSERT INTO missing VALUES (1);');
  run(f, 'migrate', f.env, false);
  assert.equal(sql(f, "SELECT name FROM sqlite_master WHERE name IN ('pending','transient')").length, 0);
  assert.equal(sql(f, 'SELECT version FROM pyry_schema_migrations').length, 3);
  migration(f, '0005_failure.sql', 'SELECT 1;'); run(f, 'migrate');
  migration(f, '0005_duplicate.sql', 'SELECT 1;'); run(f, 'migrate', f.env, false);
  unlinkSync(join(f.release, 'sql/migrations/0005_duplicate.sql'));
  migration(f, 'invalid.sql', 'SELECT 1;'); run(f, 'migrate', f.env, false);
});

test('migration SQL cannot escape its transaction or connection', async (t) => {
  const f = await fixture(t); run(f, 'migrate');
  const forbidden = ['COMMIT;', 'END;', 'ROLLBACK;', 'BEGIN;', 'SAVEPOINT own;', 'PRAGMA foreign_keys=OFF;',
    "ATTACH DATABASE 'outside.sqlite' AS other;", "VACUUM INTO 'outside.sqlite';", "SELECT load_extension('bad');",
    'DELETE FROM pyry_schema_migrations;',
    'CREATE TEMP TRIGGER steal AFTER INSERT ON pyry_schema_migrations BEGIN DELETE FROM pyry_schema_migrations; END;', "CREATE TRIGGER steal AFTER INSERT ON preferences BEGIN DELETE FROM pyry_schema_migrations; END;"];
  for (const source of forbidden) {
    migration(f, '0002_forbidden.sql', 'CREATE TABLE transient (n INTEGER); ' + source);
    run(f, 'migrate', f.env, false);
    assert.equal(sql(f, "SELECT name FROM sqlite_master WHERE name='transient'").length, 0);
    assert.equal(sql(f, 'SELECT version FROM pyry_schema_migrations').length, 1);
    assert.equal(statSync(f.env.PYRY_APP_DB_PATH).mode & 0o777, 0o600);
  }
  assert.throws(() => statSync(join(f.release, 'outside.sqlite')), /ENOENT/);
  migration(f, '0002_forbidden.sql', 'DROP TABLE preferences;');
  run(f, 'migrate', f.env, false);
  assert.equal(sql(f, "SELECT name FROM sqlite_master WHERE name='preferences'").length, 1);
});

test('startup and readiness failures', async (t) => {
  const f = await fixture(t);
  for (const key of ['PYRY_APP_ID', 'PYRY_SERVER_ID', 'PYRY_APP_RELEASE', 'PYRY_APP_DATA_DIR', 'PYRY_APP_DB_PATH', 'PYRY_APP_BIND_HOST', 'PYRY_APP_PORT', 'NODE_ENV']) {
    const env = { ...f.env }; delete env[key];
    for (const entry of ['main', 'migrate', 'health']) run(f, entry, env, false);
  }
  const invalid = { PYRY_APP_ID: ['AAAAAAAA-1111-4111-8111-111111111111', 'cccccccc-1111-4111-8111-111111111111'],
    PYRY_SERVER_ID: ['bad'], PYRY_APP_RELEASE: ['01.0.0', '2147483648.0.0', '1.0.0\n', '1.0.1'],
    PYRY_APP_DATA_DIR: ['relative'], PYRY_APP_DB_PATH: ['relative', join(f.root, 'other.sqlite')],
    PYRY_APP_BIND_HOST: ['localhost', '0.0.0.0'], PYRY_APP_PORT: ['0', '65536', '1.5', '1e3', '1\n'], NODE_ENV: ['development'] };
  for (const [key, values] of Object.entries(invalid)) for (const value of values) run(f, 'migrate', { ...f.env, [key]: value }, false);
  for (const data of [join(f.release, 'data'), join(f.root, 'source/data'), join(f.root, 'staging/candidate/data')]) {
    run(f, 'migrate', { ...f.env, PYRY_APP_DATA_DIR: data, PYRY_APP_DB_PATH: join(data, 'app.sqlite') }, false);
    assert.throws(() => statSync(data), /ENOENT/);
  }
  symlinkSync(f.release, join(f.root, 'escape'));
  const data = join(f.root, 'escape/data');
  run(f, 'migrate', { ...f.env, PYRY_APP_DATA_DIR: data, PYRY_APP_DB_PATH: join(data, 'app.sqlite') }, false);
  run(f, 'main', f.env, false);
  assert.throws(() => statSync(f.env.PYRY_APP_DATA_DIR), /ENOENT/);
  mkdirSync(f.env.PYRY_APP_DATA_DIR);
  const redirected = join(f.release, 'app.sqlite'); writeFileSync(redirected, 'unchanged');
  symlinkSync(redirected, f.env.PYRY_APP_DB_PATH);
  for (const entry of ['main', 'migrate']) run(f, entry, f.env, false);
  assert.equal(readFileSync(redirected, 'utf8'), 'unchanged');
  unlinkSync(f.env.PYRY_APP_DB_PATH);
  let db = new DatabaseSync(f.env.PYRY_APP_DB_PATH); db.close();
  run(f, 'main', f.env, false); run(f, 'migrate');
  await start(t, f); run(f, 'main', f.env, false);
  db = new DatabaseSync(f.env.PYRY_APP_DB_PATH); db.exec('DROP TABLE preferences'); db.close();
  assert.equal((await fetch(`${f.url}/_pyry/health`)).status, 503);
  db = new DatabaseSync(f.env.PYRY_APP_DB_PATH);
  db.exec('CREATE TABLE preferences (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, selected_tab TEXT NOT NULL, period TEXT NOT NULL)'); db.close();
  assert.equal((await fetch(`${f.url}/_pyry/health`)).status, 503);
  assert.match(run(f, 'main', f.env, false).stderr, /schema/);
});

test('health command and outstanding-client shutdown', async (t) => {
  const f = await fixture(t); run(f, 'migrate');
  const service = await start(t, f);
  run(f, 'health');
  const health = await fetch(`${f.url}/_pyry/health`);
  const bytes = await health.text(); assert.ok(Buffer.byteLength(bytes) <= 4096);
  assert.deepEqual(JSON.parse(bytes), { ready: true, app_id: f.env.PYRY_APP_ID, release_version: f.env.PYRY_APP_RELEASE });
  const socket = connect(Number(f.env.PYRY_APP_PORT), '127.0.0.1');
  socket.on('error', () => {}); await once(socket, 'connect');
  socket.write('POST /api/preferences HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n{');
  const before = Date.now(); service.child.kill('SIGTERM');
  const [code] = await service.closed; assert.equal(code, 0); assert.ok(Date.now() - before < 5000); socket.destroy();
  const variants = [
    [200, { ready: false }], [200, { ready: true, app_id: 'other', release_version: f.env.PYRY_APP_RELEASE }],
    [200, { ready: true, app_id: f.env.PYRY_APP_ID, release_version: '9.0.0' }],
    [503, {}], [302, {}], [200, 'x'.repeat(4097)], [200, '{'], [200, null],
  ];
  for (const [status, payload] of variants) {
    const server = createServer((_, res) => { res.writeHead(status, { Location: `${f.url}/_pyry/health` }); res.end(typeof payload === 'string' ? payload : JSON.stringify(payload)); });
    server.listen(Number(f.env.PYRY_APP_PORT), '127.0.0.1'); await once(server, 'listening');
    try {
      const result = await new Promise((resolve) => {
        const child = spawn(process.execPath, ['dist/service/health.js'], { cwd: f.release, env: f.env, stdio: 'ignore' });
        child.on('close', resolve);
      });
      assert.notEqual(result, 0);
    } finally { await new Promise((resolve) => server.close(resolve)); }
  }
  const slow = createServer(() => {});
  slow.listen(Number(f.env.PYRY_APP_PORT), '127.0.0.1'); await once(slow, 'listening');
  const timed = Date.now();
  const timeoutCode = await new Promise((resolve) => {
    const child = spawn(process.execPath, ['dist/service/health.js'], { cwd: f.release, env: f.env, stdio: 'ignore' }); child.on('close', resolve);
  });
  assert.notEqual(timeoutCode, 0); assert.ok(Date.now() - timed < 2000);
  slow.closeAllConnections(); await new Promise((resolve) => slow.close(resolve));
});

test('SQL connections enforce foreign keys and disable extensions', async (t) => {
  const f = await fixture(t); run(f, 'migrate');
  const { database } = await import('../dist/service/database.js');
  const db = database({ data: f.env.PYRY_APP_DATA_DIR, db: f.env.PYRY_APP_DB_PATH });
  try {
    assert.equal(db.prepare('PRAGMA foreign_keys').get().foreign_keys, 1);
    db.exec('CREATE TABLE parent (id INTEGER PRIMARY KEY); CREATE TABLE child (id INTEGER REFERENCES parent(id));');
    assert.throws(() => db.prepare('INSERT INTO child VALUES (?)').run(1), /FOREIGN KEY/);
    assert.throws(() => db.enableLoadExtension(true), /extension/i);
    db.exec('PRAGMA writable_schema=ON');
    assert.throws(() => db.exec("DELETE FROM sqlite_schema WHERE name='preferences'"), /sqlite_master|modified/);
  } finally { db.close(); }
});
