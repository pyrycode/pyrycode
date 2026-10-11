import '../src/service/runtime.mjs';
import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { build } from 'esbuild-wasm';

rmSync('dist', { recursive: true, force: true });
mkdirSync('dist/web/assets', { recursive: true });
const compiled = spawnSync(process.execPath, ['node_modules/typescript/bin/tsc', '-p', 'tsconfig.service.json'],
  { stdio: 'inherit', timeout: 60000 });
if (compiled.error) throw compiled.error;
if (compiled.status !== 0) throw new Error('Service compilation failed');
await build({ entryPoints: ['src/web/main.tsx'], bundle: true, format: 'esm',
  platform: 'browser', target: 'es2022', jsx: 'automatic', minify: true,
  define: { 'process.env.NODE_ENV': '"production"' },
  outfile: 'dist/web/assets/main.js', legalComments: 'none' });
writeFileSync('dist/web/index.html', `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hosted app</title><link rel="stylesheet" href="/assets/main.css">
</head><body><div id="root"></div><script type="module" src="/assets/main.js"></script></body></html>
`);
