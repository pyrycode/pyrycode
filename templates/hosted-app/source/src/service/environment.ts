import './runtime.mjs';
import { existsSync, readFileSync, realpathSync, lstatSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

export const releaseRoot = fileURLToPath(new URL('../../', import.meta.url));
function canonical(path: string): string {
  if (existsSync(path)) return realpathSync(path);
  const parent = dirname(path);
  if (parent === path) throw new Error('data path cannot be resolved');
  return join(canonical(parent), relative(parent, path));
}
function inside(root: string, path: string): boolean {
  const rel = relative(root, path);
  return rel === '' || (!rel.startsWith(`..${sep}`) && rel !== '..' && !isAbsolute(rel));
}
export function environment() {
  const value = (key: string) => {
    const v = process.env[key];
    if (!v) throw new Error(`missing ${key}`);
    return v;
  };
  const app = value('PYRY_APP_ID'), server = value('PYRY_SERVER_ID'), release = value('PYRY_APP_RELEASE');
  const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$(?![\s\S])/;
  const version = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$(?![\s\S])/;
  if (!uuid.test(app) || !uuid.test(server)) throw new Error('invalid app/server identity');
  if (!version.test(release) || release.length > 32 || release.split('.').some((n) => Number(n) > 2147483647)) throw new Error('invalid release version');
  const manifest = JSON.parse(readFileSync(join(releaseRoot, 'app.json'), 'utf8'));
  if (manifest.app_id !== app || manifest.server_id !== server || manifest.release_version !== release) throw new Error('environment identity differs from app.json');
  const host = value('PYRY_APP_BIND_HOST'), portText = value('PYRY_APP_PORT');
  if (host !== '127.0.0.1' || !/^[0-9]+$(?![\s\S])/.test(portText) || Number(portText) < 1 || Number(portText) > 65535) throw new Error('invalid loopback host/port');
  if (value('NODE_ENV') !== 'production') throw new Error('NODE_ENV must be production');
  const data = value('PYRY_APP_DATA_DIR'), db = value('PYRY_APP_DB_PATH');
  if (!isAbsolute(data) || !isAbsolute(db) || db !== join(data, 'app.sqlite')) throw new Error('data paths must be absolute; database must be data/app.sqlite');
  const resolvedData = canonical(resolve(data)), resolvedDB = canonical(resolve(db));
  for (const path of [resolve(data), resolve(db), resolvedData, resolvedDB]) {
    if (inside(realpathSync(releaseRoot), path) || path.split(sep).some((part) => ['source', 'staging', 'builds', 'dist'].includes(part))) throw new Error('unsafe data placement in source/staging/build tree');
  }
  if (resolvedDB !== join(resolvedData, 'app.sqlite') || (existsSync(db) && lstatSync(db).isSymbolicLink())) throw new Error('database path redirects outside data directory');
  return { app, server, release, host, port: Number(portText), data: resolvedData, db: resolvedDB };
}
