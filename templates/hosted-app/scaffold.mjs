import { copyFileSync, constants, mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import './source/src/service/runtime.mjs';

const source = fileURLToPath(new URL('./source/', import.meta.url));
const files = ['package.json', 'package-lock.json', '.npmrc', '.gitignore',
  'tsconfig.web.json', 'tsconfig.service.json', 'scripts/build.mjs',
  'src/web/App.tsx', 'src/web/main.tsx', 'src/web/style.css', 'src/web/assets.d.ts',
  'src/service/main.ts', 'src/service/migrate.ts', 'src/service/runtime.mjs', 'test/app.test.mjs'];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const space = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]|[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]$/u;

// Creates source only. It neither registers an identity nor proves readiness.
export function scaffold(options) {
  const { destination, title, 'server-id': serverID, 'app-id': appID } = options;
  const version = options['release-version'] ?? '1.0.0';
  for (const [key, value] of [['server-id', serverID], ['app-id', appID]]) {
    if (typeof value !== 'string' || !uuid.test(value) || value.length !== 36) throw new Error(`${key}: expected canonical lowercase UUIDv4`);
  }
  if (typeof title !== 'string' || !title.isWellFormed() || !title.length || Buffer.byteLength(title) > 128 || /\p{Cc}/u.test(title) || space.test(title)) {
    throw new Error('title: expected 1–128 UTF-8 bytes without controls or surrounding whitespace');
  }
  const parts = typeof version === 'string' ? version.split('.') : [];
  if (version.length > 32 || parts.length !== 3 || parts.some((p) => !/^(0|[1-9][0-9]*)$/.test(p) || /[^0-9]/.test(p) || Number(p) > 2147483647)) {
    throw new Error('release-version: expected MAJOR.MINOR.PATCH components in 0–2147483647 without leading zeros');
  }
  if (typeof destination !== 'string' || !destination.length) throw new Error('destination: expected a new path with an existing parent');
  const dest = resolve(destination);
  // Nonrecursive mkdir claims ownership atomically, including dangling symlinks.
  try { mkdirSync(dest, { mode: 0o700 }); }
  catch (error) { throw new Error(`destination: cannot create new directory (${error.code})`); }
  for (const file of files) {
    const target = join(dest, file);
    mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
    copyFileSync(join(source, file), target, constants.COPYFILE_EXCL);
  }
  mkdirSync(join(dest, 'sql/migrations'), { recursive: true, mode: 0o700 });
  const manifest = JSON.parse(readFileSync(new URL('./manifest.json', import.meta.url), 'utf8'));
  Object.assign(manifest, { server_id: serverID, app_id: appID, title, release_version: version });
  for (const [file, data] of [['app.json', manifest], ['src/web/title.json', { title }]]) {
    writeFileSync(join(dest, file), JSON.stringify(data, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  }
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  try {
    const { values } = parseArgs({ options: Object.fromEntries(
      ['destination', 'title', 'server-id', 'app-id', 'release-version'].map((key) => [key, { type: 'string' }])) });
    scaffold(values);
  } catch (error) {
    console.error(`hosted-app scaffold: ${error.message}`);
    process.exitCode = 1;
  }
}
