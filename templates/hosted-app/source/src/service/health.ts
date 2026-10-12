import './runtime.mjs';
import { request } from 'node:http';
import { environment } from './environment.js';
import { diagnostic } from './database.js';

try {
  const config = environment();
  await new Promise<void>((resolve, reject) => {
    const req = request({ host: config.host, port: config.port, path: '/_pyry/health', method: 'GET', agent: false }, (res) => {
      let size = 0;
      const chunks: Buffer[] = [];
      res.on('data', (chunk: Buffer) => {
        size += chunk.length;
        if (size > 4096) req.destroy(new Error('health response exceeds 4096 bytes'));
        else chunks.push(chunk);
      });
      res.on('error', reject);
      res.on('end', () => {
        try {
          const value = JSON.parse(Buffer.concat(chunks).toString('utf8'));
          if (res.statusCode !== 200 || value?.ready !== true || value.app_id !== config.app || value.release_version !== config.release) throw new Error('health status/identity mismatch');
          resolve();
        } catch (error) { reject(error); }
      });
    });
    const deadline = setTimeout(() => req.destroy(new Error('health deadline exceeded')), 1000);
    req.on('error', reject);
    req.on('close', () => clearTimeout(deadline));
    req.end();
  });
} catch (error) { diagnostic('health failed', error); }
