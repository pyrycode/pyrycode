import assert from 'node:assert/strict';
import { readFileSync, writeFileSync, unlinkSync } from 'node:fs';
import test from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { build } from 'esbuild-wasm';
import { assertRuntime } from '../src/service/runtime.mjs';

test('Node range rejects unsupported versions', () => {
  for (const version of ['24.15.0', '24.15.1', '24.20.0']) assertRuntime(version);
  for (const version of ['22.20.0', '24.14.9', '25.0.0', '24.15.0-rc.1', 'invalid']) {
    assert.throws(() => assertRuntime(version), /requires Node >=24\.15\.0 <25\.0\.0/);
  }
});

test('React renders caller title as literal text', async () => {
  const result = await build({ entryPoints: ['src/web/App.tsx'], bundle: true,
    platform: 'node', format: 'esm', write: false, jsx: 'automatic', packages: 'external' });
  const { App } = await import('file://' + await writeModule(result.outputFiles[0].text));
  unlinkSync('test/.app-test.mjs');
  const { title } = JSON.parse(readFileSync('src/web/title.json', 'utf8'));
  for (const value of [title, 'A "title" </script><img src=x onerror=alert(1)>']) {
    const rendered = renderToStaticMarkup(App({ title: value }));
    const escaped = value.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#x27;' })[c]);
    assert.ok(rendered.includes(`<h1>${escaped}</h1>`));
    assert.doesNotMatch(rendered, /<img|<script/);
  }
});

async function writeModule(source) {
  const path = new URL('./.app-test.mjs', import.meta.url);
  writeFileSync(path, source);
  return path.pathname;
}

test('preference shell is labeled and polite', async () => {
  const result = await build({ entryPoints: ['src/web/App.tsx'], bundle: true,
    platform: 'node', format: 'esm', write: false, jsx: 'automatic', packages: 'external' });
  const { App } = await import('file://' + await writeModule(result.outputFiles[0].text));
  unlinkSync('test/.app-test.mjs');
  const html = renderToStaticMarkup(createElement(App, { title: 'Example' }));
  assert.match(html, /aria-live="polite"/);
  assert.match(html, /Overview/);
  assert.match(html, /Preferences/);
  assert.match(html, /Save/);
  assert.match(html, /label[^>]*for="period"/);
});
