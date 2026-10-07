#!/usr/bin/env node
// Reconcile QMD's default index.yml; run qmd update, then qmd embed separately.
import { constants, accessSync, existsSync, mkdirSync, mkdtempSync, readFileSync,
  realpathSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { homedir } from 'node:os';
import { delimiter, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const usage = 'Usage: node /checkout/cmd/qmd-current/setup.mjs [--repo /checkout]\n'
  + 'Uses QMD_CONFIG_DIR/index.yml (or XDG_CONFIG_HOME/qmd, then ~/.config/qmd).\n'
  + 'Keep QMD_CONFIG_DIR and INDEX_PATH set for subsequent qmd update and qmd embed.';

function setup(args) {
  if (args.length === 1 && args[0] === '--help') {
    console.log(usage);
    return;
  }
  if (args.length !== 0 && (args.length !== 2 || args[0] !== '--repo' || !args[1])) {
    throw new Error(usage);
  }
  const repo = args.length ? resolve(args[1]) : resolve(dirname(fileURLToPath(import.meta.url)), '../..');
  const root = realpathSync(join(repo, 'docs/knowledge'));
  for (const section of ['features', 'decisions']) {
    if (!statSync(join(root, section)).isDirectory()) {
      throw new Error(`Not a documentation directory: ${join(root, section)}`);
    }
  }

  // Use the YAML parser shipped with the installed QMD, without private QMD APIs.
  const executable = (process.env.PATH || '').split(delimiter)
    .map(dir => resolve(dir, 'qmd')).find(path => {
      try { accessSync(path, constants.X_OK); return statSync(path).isFile(); }
      catch { return false; }
    });
  if (!executable) throw new Error('Install QMD and put qmd on PATH before setup');
  const YAML = createRequire(realpathSync(executable))('yaml');
  const configDir = process.env.QMD_CONFIG_DIR || join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'qmd');
  const configPath = join(configDir, 'index.yml');
  const original = existsSync(configPath) ? readFileSync(configPath, 'utf8') : '';
  const document = YAML.parseDocument(original);
  if (document.errors.length) throw new Error(`Invalid ${configPath}: ${document.errors[0].message}`);
  if (document.contents === null) document.contents = document.createNode({});
  if (!YAML.isMap(document.contents)) throw new Error(`Expected a mapping in ${configPath}`);
  if (!document.has('collections')) document.set('collections', document.createNode({}));
  if (!YAML.isMap(document.get('collections', true))) {
    throw new Error(`Expected a collections mapping in ${configPath}`);
  }
  const key = ['collections', 'pyrycode-current'];
  if (!document.hasIn(key)) document.setIn(key, document.createNode({}));
  if (!YAML.isMap(document.getIn(key, true))) {
    throw new Error(`Expected a pyrycode-current mapping in ${configPath}`);
  }
  const pattern = '{features,decisions}/**/*.md';
  if (document.getIn([...key, 'path']) === root && document.getIn([...key, 'pattern']) === pattern) {
    console.log(`pyrycode-current already configured: ${root}`);
    return;
  }
  document.setIn([...key, 'path'], root);
  document.setIn([...key, 'pattern'], pattern);

  mkdirSync(configDir, { recursive: true });
  const scratch = mkdtempSync(join(configDir, '.pyrycode-current-'));
  try {
    const temporary = join(scratch, 'index.yml');
    const mode = existsSync(configPath) ? statSync(configPath).mode & 0o777 : 0o600;
    writeFileSync(temporary, document.toString(), { mode, flush: true });
    renameSync(temporary, configPath);
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
  console.log(`Configured pyrycode-current: ${root} (${pattern})`);
  console.log('Run qmd update, then qmd embed with the same configuration/index environment.');
}

try {
  setup(process.argv.slice(2));
} catch (error) {
  console.error(`QMD setup: ${error.message}`);
  process.exitCode = 1;
}
