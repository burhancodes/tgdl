import test from 'node:test';
import assert from 'node:assert/strict';
import express from 'express';
import { rpcMiddleware } from '../rpcHandler.js';
import { buildMagnetUrl, extractInfoHash, base32ToHex } from '../lib/magnetHelper.js';

function createTestApp() {
  const app = express();
  app.use(express.json());
  app.post('/rpc', rpcMiddleware);
  return app;
}

async function postRpc(app, body, headers = {}) {
  const server = app.listen(0);
  const port = server.address().port;
  try {
    const res = await fetch(`http://127.0.0.1:${port}/rpc`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...headers,
      },
      body: JSON.stringify(body),
    });
    const json = await res.json();
    return { status: res.status, json };
  } finally {
    server.close();
  }
}

test('magnetHelper utilities', () => {
  const hash = '45a305e26090e543666b6cfa45d6541f486431bd';
  const url = buildMagnetUrl({ infoHash: hash, title: 'Ubuntu Test' });
  assert.ok(url.startsWith('magnet:?xt=urn:btih:45a305e26090e543666b6cfa45d6541f486431bd'));
  assert.ok(url.includes('dn=Ubuntu%20Test'));
  assert.equal(extractInfoHash(url), hash);

  const base32Hash = 'IVRQLYTAYDQUGZT3NL7ULVSUH5DGG4N5';
  const converted = base32ToHex(base32Hash);
  assert.equal(converted, '456305e260c0e143667b6aff45d6543f466371bd');
});

test('RPC torrent.health', async () => {
  const app = createTestApp();
  const res = await postRpc(app, {
    jsonrpc: '2.0',
    method: 'torrent.health',
    id: 101,
  });

  assert.equal(res.status, 200);
  assert.equal(res.json.jsonrpc, '2.0');
  assert.equal(res.json.id, 101);
  assert.equal(res.json.result.status, 'ok');
  assert.equal(res.json.result.service, 'magnetio-scraper');
});

test('RPC torrent.providers', async () => {
  const app = createTestApp();
  const res = await postRpc(app, {
    jsonrpc: '2.0',
    method: 'torrent.providers',
    id: 102,
  });

  assert.equal(res.status, 200);
  assert.equal(res.json.jsonrpc, '2.0');
  assert.equal(res.json.id, 102);
  assert.ok(Array.isArray(res.json.result.providers));
  assert.ok(res.json.result.providers.some(p => p.id === 'thepiratebay'));
});

test('RPC authentication check', async () => {
  process.env.RPC_SHARED_SECRET = 'supersecret';
  const app = createTestApp();

  try {
    const unauth = await postRpc(app, {
      jsonrpc: '2.0',
      method: 'torrent.health',
      id: 1,
    });
    assert.equal(unauth.json.error.code, -32001);

    const authHeader = await postRpc(app, {
      jsonrpc: '2.0',
      method: 'torrent.health',
      id: 2,
    }, { Authorization: 'Bearer supersecret' });
    assert.equal(authHeader.json.result.status, 'ok');

    const authParam = await postRpc(app, {
      jsonrpc: '2.0',
      method: 'torrent.health',
      params: { secret: 'supersecret' },
      id: 3,
    });
    assert.equal(authParam.json.result.status, 'ok');
  } finally {
    delete process.env.RPC_SHARED_SECRET;
  }
});

test('RPC batch request', async () => {
  const app = createTestApp();
  const res = await postRpc(app, [
    { jsonrpc: '2.0', method: 'torrent.health', id: 1 },
    { jsonrpc: '2.0', method: 'torrent.providers', id: 2 },
  ]);

  assert.equal(res.status, 200);
  assert.ok(Array.isArray(res.json));
  assert.equal(res.json.length, 2);
  assert.equal(res.json[0].id, 1);
  assert.equal(res.json[1].id, 2);
});

test('RPC error handling for invalid request and method not found', async () => {
  const app = createTestApp();

  const invalidReq = await postRpc(app, {
    jsonrpc: '1.0',
    method: 'torrent.health',
    id: 1,
  });
  assert.equal(invalidReq.json.error.code, -32600);

  const notFound = await postRpc(app, {
    jsonrpc: '2.0',
    method: 'unknown.method',
    id: 2,
  });
  assert.equal(notFound.json.error.code, -32601);
});
