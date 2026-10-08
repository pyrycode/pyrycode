#!/usr/bin/env node
// Compatibility entry point. Collection maintenance belongs to pyrycode-agents.
import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const agents = process.env.AGENTS_REPO_PATH || resolve(repo, '../pyrycode-agents');
const script = resolve(agents, 'tools/pyrycode/cmd/qmd-current/setup.mjs');
if (!existsSync(script)) {
  console.error('Install pyrycode-agents beside this checkout or set AGENTS_REPO_PATH.');
  process.exit(2);
}
const args = process.argv.slice(2);
const result = spawnSync(process.execPath, [script, ...(args.length ? args : ['--repo', repo])], { stdio: 'inherit' });
if (result.error) console.error(result.error.message);
process.exit(result.status ?? 1);
