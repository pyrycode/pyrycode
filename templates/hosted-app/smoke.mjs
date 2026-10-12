import { assertRuntime } from './source/src/service/runtime.mjs';
import { scaffold } from './scaffold.mjs';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync, accessSync, constants, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

// Run from any directory: node templates/hosted-app/smoke.mjs /absolute/artifact-directory
assertRuntime();
const output = resolve(process.argv[2] ?? 'hosted-app-smoke-artifacts');
mkdirSync(output, { recursive: true });
const temporary = mkdtempSync(join(tmpdir(), 'hosted-app-smoke-'));
const commands = [];
const tests = [];
function run(command, args, cwd = temporary) {
  const result = spawnSync(command, args, { cwd, encoding: 'utf8', timeout: 120000, detached: true,
    env: { ...process.env, PATH: `${process.execPath.slice(0, process.execPath.lastIndexOf('/'))}:${process.env.PATH}` } });
  if (result.error && result.pid > 0) { try { process.kill(-result.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; } }
  commands.push({ command: [command, ...args], cwd, status: result.status,
    stdout: result.stdout ?? '', stderr: result.stderr ?? '', error: result.error?.message });
  writeFileSync(join(output, 'commands.json'), JSON.stringify({ node: process.version, commands }) + '\n');
  if (result.error || result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed: ${result.error?.message ?? `${result.stderr ?? ''} ${result.stdout ?? ''}`}`);
  if (args[0] === 'test') {
    const count = (name) => Number(result.stdout.match(new RegExp(`# ${name} (\\d+)`))?.[1]);
    const counts = { cwd, executed: count('tests'), passed: count('pass'), skipped: count('skipped') };
    if (!counts.executed || counts.executed !== counts.passed || counts.skipped !== 0) throw new Error('Deterministic tests did not all execute and pass');
    tests.push(counts);
  }
  console.log(`PASS ${command} ${args.join(' ')}`);
  return result.stdout.trim();
}
try {
  const npm = run('npm', ['--version']);
  if (!process.env.HOSTED_APP_BROWSER) throw new Error('Set HOSTED_APP_BROWSER to an installed Chromium executable (Playwright Core 1.61.1 compatible).');
  accessSync(process.env.HOSTED_APP_BROWSER, constants.X_OK);
  const browserVersion = run(process.env.HOSTED_APP_BROWSER, ['--version']);
  const releases = ['1.0.0', '1.0.1'].map((version) => join(temporary, `example-${version}`));
  for (const [i, destination] of releases.entries()) {
    scaffold({ destination, title: 'Preference example <literal>', 'server-id': '11111111-1111-4111-8111-111111111111',
      'app-id': '22222222-2222-4222-8222-222222222222', 'release-version': i ? '1.0.1' : '1.0.0' });
    commands.push({ command: ['scaffold', '--destination', destination, '--title', 'Preference example <literal>',
      '--server-id', '11111111-1111-4111-8111-111111111111', '--app-id', '22222222-2222-4222-8222-222222222222',
      '--release-version', i ? '1.0.1' : '1.0.0'], status: 0 });
    for (const args of [['ci', '--no-audit', '--no-fund'], ['run', 'typecheck'], ['test'], ['run', 'build']]) run('npm', args, destination);
  }
  run('npm', ['run', 'smoke:browser', '--', releases[1], join(temporary, 'data'), output], releases[0]);
  const browser = JSON.parse(readFileSync(join(output, 'browser.json'), 'utf8'));
  writeFileSync(join(output, 'versions.json'), JSON.stringify({ node: process.version, npm,
    browserExecutable: process.env.HOSTED_APP_BROWSER, browserVersion, tests,
    browser: { version: browser.browser, executed: browser.executed, passed: browser.passed, skipped: browser.skipped },
    artifacts: readdirSync(output).sort() }, null, 2) + '\n');
  console.log(`Evidence: ${output}`);
} catch (error) { console.error(`hosted-app smoke: ${error.message}`); process.exitCode = 1; }
finally { rmSync(temporary, { recursive: true, force: true }); }
