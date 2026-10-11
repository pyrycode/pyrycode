import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { constants as c, DatabaseSync } from 'node:sqlite';
import { releaseRoot } from './environment.js';

export function migrationFiles() {
  const directory = join(releaseRoot, 'sql/migrations');
  if (lstatSync(join(releaseRoot, 'sql')).isSymbolicLink() || !lstatSync(directory).isDirectory()) throw new Error('unsafe migrations directory');
  const files = readdirSync(directory).map((name) => {
    if (!/^[0-9]{4}_[a-z0-9_]+\.sql$(?![\s\S])/.test(name) || !lstatSync(join(directory, name)).isFile()) throw new Error(`invalid migration file: ${name}`);
    const bytes = readFileSync(join(directory, name));
    return { version: Number(name.slice(0, 4)), sha256: createHash('sha256').update(bytes).digest('hex'), sql: bytes.toString('utf8') };
  }).sort((a, b) => a.version - b.version);
  if (new Set(files.map((f) => f.version)).size !== files.length) throw new Error('duplicate migration prefix');
  return files;
}
export function ledger(db: DatabaseSync, files: ReturnType<typeof migrationFiles>, complete = false) {
  const applied = db.prepare('SELECT version, sha256 FROM pyry_schema_migrations').all();
  for (const row of applied) {
    if (!files.some((f) => f.version === row.version && f.sha256 === row.sha256)) throw new Error(`missing/changed applied migration: ${row.version}`);
  }
  const pending = files.filter((f) => !applied.some((r) => r.version === f.version));
  if (complete && pending.length) throw new Error('unmigrated schema');
  return pending;
}
const forbidden = new Set([c.SQLITE_TRANSACTION, c.SQLITE_SAVEPOINT, c.SQLITE_ATTACH, c.SQLITE_DETACH,
  c.SQLITE_PRAGMA, c.SQLITE_CREATE_VTABLE, c.SQLITE_DROP_VTABLE, c.SQLITE_COPY, c.SQLITE_CREATE_TRIGGER, c.SQLITE_CREATE_TEMP_TRIGGER]);
export function migrationSQL(db: DatabaseSync, sql: string) {
  // Authorization applies to SQLite operations, including obfuscated SQL and nested statements.
  db.setAuthorizer((action, first, second) => {
    if (forbidden.has(action) || (first === 'pyry_schema_migrations' || second === 'pyry_schema_migrations') ||
        (action === c.SQLITE_FUNCTION && !['abs', 'coalesce', 'length', 'lower', 'upper'].includes(second ?? ''))) return c.SQLITE_DENY;
    return c.SQLITE_OK;
  });
  try { db.exec(sql); } finally { db.setAuthorizer(null); }
}
