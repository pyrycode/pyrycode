import { DatabaseSync } from 'node:sqlite';
import { chmodSync, closeSync, constants, lstatSync, mkdirSync, openSync, readFileSync } from 'node:fs';
import { environment, releaseRoot } from './environment.js';
import { join } from 'node:path';

export function database(config: ReturnType<typeof environment>, create = false) {
  if (create) {
    mkdirSync(config.data, { recursive: true, mode: 0o700 });
    chmodSync(config.data, 0o700);
    const fd = openSync(config.db, constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW, 0o600);
    closeSync(fd);
  }
  if (!lstatSync(config.db).isFile()) throw new Error('existing migrated database required');
  chmodSync(config.db, 0o600);
  return new DatabaseSync(config.db, { enableForeignKeyConstraints: true, defensive: true, allowExtension: false, timeout: 1000 });
}
export function transaction(db: DatabaseSync, work: () => void) {
  db.exec('BEGIN IMMEDIATE');
  try { work(); db.exec('COMMIT'); } catch (error) {
    if (db.isTransaction) db.exec('ROLLBACK');
    throw error;
  }
}
export function preferences(input: unknown) {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) throw new Error('preferences must be an object');
  const v = input as Record<string, unknown>;
  if (v.version !== 1 || ('selected_tab' in v && !['overview', 'preferences'].includes(v.selected_tab as string)) ||
      ('period' in v && !['1h', '24h', '7d'].includes(v.period as string))) throw new Error('invalid preference version/tab/period');
  return { version: 1, selected_tab: v.selected_tab ?? 'overview', period: v.period ?? '24h' };
}
export function schema(db: DatabaseSync) {
  const expected = readFileSync(join(releaseRoot, 'sql/migrations/0001_preferences.sql'), 'utf8').trim().replace(/;$/, '');
  const definition = db.prepare("SELECT sql FROM main.sqlite_schema WHERE type = 'table' AND name = ?").get('preferences')?.sql;
  const normalize = (sql: string) => sql.replace(/\s+/g, ' ').trim();
  if (typeof definition !== 'string' || normalize(definition) !== normalize(expected)) throw new Error('unavailable preference schema definition');
  const tables = { preferences: ['id:INTEGER:1', 'version:INTEGER:0', 'selected_tab:TEXT:0', 'period:TEXT:0'],
    pyry_schema_migrations: ['version:INTEGER:1', 'sha256:TEXT:0'] };
  for (const [table, expected] of Object.entries(tables)) {
    const columns = db.prepare(`PRAGMA main.table_info(${table})`).all();
    if (columns.map((c) => `${c.name}:${c.type}:${c.pk}`).join() !== expected.join() ||
        columns.some((c) => c.pk !== 1 && c.notnull !== 1)) throw new Error(`unavailable schema: ${table}`);
  }
  const rows = db.prepare('SELECT id, version, selected_tab, period FROM main.preferences').all();
  if (rows.length > 1 || rows.some((r) => r.id !== 1)) throw new Error('invalid preference record');
  for (const row of rows) preferences(row);
  if (Object.values(db.prepare('PRAGMA main.quick_check').get() ?? {})[0] !== 'ok' || db.prepare('PRAGMA main.foreign_key_check').all().length) throw new Error('database integrity check failed');
}
export function diagnostic(entry: string, error: unknown) {
  console.error(`${entry}: ${error instanceof Error ? error.message : 'failed'}`);
  process.exitCode = 1;
}
