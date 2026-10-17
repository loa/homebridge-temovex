import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createServer, type Server } from 'node:http';
import test from 'node:test';

import { parseProxyUrl, parseTemperatures, ProxyClient, ProxyError, proxyLabel } from './proxy.js';

function listen(server: Server): Promise<void> {
  server.listen(0, '127.0.0.1');
  return once(server, 'listening').then(() => undefined);
}

function close(server: Server): Promise<void> {
  server.close();
  return once(server, 'close').then(() => undefined);
}

const sample = {
  unit: 'celsius',
  temperatures: [
    { name: 'set', description: 'Supply-air setpoint', celsius: 18 },
    { name: 'al', description: 'Exhaust air', celsius: 21.8 },
    { name: 'fl', description: 'Extract air', celsius: 22.1 },
    { name: 'extra', description: 'Ignored', celsius: 1 },
    { name: 'ul', description: 'Outdoor air', celsius: -4.5 },
    { name: 'tl', description: 'Supply air', celsius: 19.2 },
  ],
};

test('parseProxyUrl requires a protocol and host', () => {
  const url = parseProxyUrl(' http://192.168.1.10:8080/base/ ');
  assert.equal(url.protocol, 'http:');
  assert.equal(url.hostname, '192.168.1.10');
  assert.equal(url.port, '8080');
  assert.equal(proxyLabel(url), 'http://192.168.1.10:8080/base');
  assert.equal(parseProxyUrl('https://proxy.example').protocol, 'https:');

  assert.throws(() => parseProxyUrl(''), /protocol/);
  assert.throws(() => parseProxyUrl('192.168.1.10:8080'), /protocol/);
  assert.throws(() => parseProxyUrl('ftp://192.168.1.10'), /http or https/);
  assert.throws(() => parseProxyUrl('http://'), /host/);
});

test('parseTemperatures keeps the five known readings', () => {
  const values = parseTemperatures(JSON.stringify(sample));
  assert.equal(values.get('set'), 18);
  assert.equal(values.get('ul'), -4.5);
  assert.equal(values.get('tl'), 19.2);
  assert.equal(values.size, 5);
  assert.throws(() => parseTemperatures('{"unit":"celsius","temperatures":[]}'), ProxyError);
  assert.throws(() => parseTemperatures('not-json'), /not JSON/);
});

test('proxy client reads when asked and writes the setpoint', async () => {
  const hits: string[] = [];
  const bodies: string[] = [];
  const server = createServer((request, response) => {
    hits.push(`${request.method} ${request.url}`);
    const chunks: Buffer[] = [];
    request.on('data', (chunk: Buffer) => chunks.push(chunk));
    request.on('end', () => {
      bodies.push(Buffer.concat(chunks).toString('utf8'));
      if (request.url === '/base/temperatures' && request.method === 'GET') {
        response.setHeader('content-type', 'application/json');
        response.end(JSON.stringify(sample));
        return;
      }
      if (request.url === '/base/temperatures/set' && request.method === 'PUT') {
        response.setHeader('content-type', 'application/json');
        response.end('{"name":"set","description":"Supply-air setpoint","celsius":21.5}');
        return;
      }
      response.statusCode = 502;
      response.end('{"error":"upstream failed"}');
    });
  });
  await listen(server);
  try {
    const address = server.address();
    if (address === null || typeof address === 'string') {
      throw new Error('test server did not bind a port');
    }
    const client = new ProxyClient(parseProxyUrl(`http://127.0.0.1:${address.port}/base/`), 60_000);

    const [first, second] = await Promise.all([client.temperatures(), client.temperatures()]);
    assert.equal(first.get('fl'), 22.1);
    assert.equal(second.get('al'), 21.8);
    assert.deepEqual(hits, ['GET /base/temperatures']);

    const third = await client.temperatures();
    assert.equal(third.get('set'), 18);
    assert.equal(hits.length, 1);

    await client.setSetpoint(21.5);
    assert.equal(hits.at(-1), 'PUT /base/temperatures/set');
    assert.equal(bodies.at(-1), '{"celsius":21.5}');

    await client.temperatures();
    assert.equal(hits.filter((hit) => hit.startsWith('GET')).length, 2);

    const failing = new ProxyClient(parseProxyUrl(`http://127.0.0.1:${address.port}/missing`), 0);
    await assert.rejects(failing.temperatures(), (error: unknown) => {
      assert.ok(error instanceof ProxyError);
      assert.equal(error.status, 502);
      assert.match(error.message, /upstream failed/);
      return true;
    });
  } finally {
    await close(server);
  }
});
