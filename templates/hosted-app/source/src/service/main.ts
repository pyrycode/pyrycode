import './runtime.mjs';
import { createServer, IncomingMessage, ServerResponse } from 'node:http';
import { environment } from './environment.js';
import { database, diagnostic, preferences, schema, transaction } from './database.js';
import { ledger, migrationFiles } from './migrations.js';

function reply(res: ServerResponse, status: number, value?: unknown) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(value === undefined ? undefined : JSON.stringify(value));
}
async function body(req: IncomingMessage): Promise<unknown> {
  if (Number(req.headers['content-length']) > 16384) throw new Error('overflow');
  let size = 0;
  const chunks: Buffer[] = [];
  for await (const chunk of req.iterator({ destroyOnReturn: false })) {
    size += chunk.length;
    if (size > 16384) throw new Error('overflow');
    chunks.push(chunk);
  }
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks)));
}
try {
  const config = environment(), db = database(config);
  const ready = () => { schema(db); ledger(db, migrationFiles(), true); };
  try { ready(); } catch (error) { db.close(); throw error; }
  let stopping = false;
  const server = createServer({ headersTimeout: 5000, requestTimeout: 30000, keepAliveTimeout: 30000, connectionsCheckingInterval: 1000 }, async (req, res) => {
    const deadline = setTimeout(() => res.destroy(), 30000);
    res.on('close', () => clearTimeout(deadline));
    try {
      if (stopping) { reply(res, 503); return; }
      if (req.method === 'GET' && req.url === '/_pyry/health') {
        ready(); reply(res, 200, { ready: true, app_id: config.app, release_version: config.release });
      } else if (req.method === 'GET' && req.url === '/api/preferences') {
        reply(res, 200, preferences(db.prepare('SELECT version, selected_tab, period FROM preferences WHERE id = ?').get(1) ?? { version: 1 }));
      } else if (req.method === 'POST' && req.url === '/api/preferences') {
        let value;
        try { value = preferences(await body(req)); } catch (error) {
          res.setHeader('Connection', 'close'); req.resume();
          reply(res, error instanceof Error && error.message === 'overflow' ? 413 : 400);
          return;
        }
        if (stopping || res.destroyed) return;
        transaction(db, () => db.prepare('INSERT OR REPLACE INTO preferences (id, version, selected_tab, period) VALUES (?, ?, ?, ?)')
          .run(1, value.version, value.selected_tab as string, value.period as string));
        reply(res, 204);
      } else { reply(res, 404); }
    } catch { if (!res.destroyed) reply(res, 503); }
  });
  server.setTimeout(30000);
  const stop = () => {
    if (stopping) return;
    stopping = true;
    server.close(); server.closeAllConnections(); db.close();
  };
  process.once('SIGTERM', stop);
  server.once('error', (error) => { stop(); diagnostic('service failed', error); });
  server.listen(config.port, config.host);
} catch (error) { diagnostic('service failed', error); }
