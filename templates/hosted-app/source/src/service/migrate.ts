import './runtime.mjs';
import { environment } from './environment.js';
import { database, diagnostic, schema, transaction } from './database.js';
import { ledger, migrationFiles, migrationSQL } from './migrations.js';

try {
  const config = environment(), files = migrationFiles(), db = database(config, true);
  try {
    transaction(db, () => {
      db.exec('CREATE TABLE IF NOT EXISTS main.pyry_schema_migrations (version INTEGER PRIMARY KEY, sha256 TEXT NOT NULL)');
      for (const file of ledger(db, files)) {
        migrationSQL(db, file.sql);
        db.prepare('INSERT INTO main.pyry_schema_migrations (version, sha256) VALUES (?, ?)').run(file.version, file.sha256);
      }
      schema(db);
    });
  } finally { db.close(); }
} catch (error) { diagnostic('migration failed', error); }
