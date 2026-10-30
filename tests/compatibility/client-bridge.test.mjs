import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import test from 'node:test';
import {validateSimulatedBridgeConfig, verifySimulatedBridge} from './client-bridge.mjs';

const config = {base_url: 'http://127.0.0.1:12345', api_key: 'local-synthetic-sdk-secret', model: 'zt-claude-sonnet-5', provider_kind: 'simulated', pid: process.pid, run_nonce: '1234567890abcdef1234567890abcdef'};

async function endpoint(t, handler) {
  const server = createServer(handler);
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  return `http://127.0.0.1:${server.address().port}`;
}

test('simulated bridge requires an instance identity, not only a self-declared mode', () => {
  assert.doesNotThrow(() => validateSimulatedBridgeConfig(config));
  for (const patch of [{pid: 0}, {pid: '1'}, {run_nonce: ''}, {run_nonce: 'not-a-nonce'}]) {
    assert.throws(() => validateSimulatedBridgeConfig({...config, ...patch}));
  }
});

test('bridge handshake is a nonbillable GET tied to the owned listener instance', async t => {
  let gets = 0, posts = 0;
  const base_url = await endpoint(t, (req, res) => {
    if (req.method === 'POST') posts++;
    assert.equal(req.method, 'GET'); assert.equal(req.url, '/__ztapi_client_bridge');
    assert.equal(req.headers.authorization, 'Bearer '+config.api_key); gets++;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({provider_kind: 'simulated', pid: config.pid, run_nonce: config.run_nonce}));
  });
  await verifySimulatedBridge({...config, base_url});
  assert.equal(gets, 1); assert.equal(posts, 0);
});

test('wrong, live, redirected or oversized identities never authorize inference', async t => {
  for (const scenario of ['live', 'pid', 'nonce', 'redirect', 'not-json', 'oversized']) {
    let requests = 0;
    const base_url = await endpoint(t, (req, res) => {
      requests++;
      assert.equal(req.method, 'GET');
      const identity = {provider_kind: 'simulated', pid: config.pid, run_nonce: config.run_nonce};
      if (scenario === 'live') identity.provider_kind = 'live';
      if (scenario === 'pid') identity.pid++;
      if (scenario === 'nonce') identity.run_nonce = '0'.repeat(32);
      if (scenario === 'redirect') { res.writeHead(302, {Location: '/v1/responses'}); res.end(); return; }
      res.setHeader('Content-Type', 'application/json');
      res.end(scenario === 'not-json' ? 'invalid' : scenario === 'oversized' ? JSON.stringify({...identity, extra: 'x'.repeat(8192)}) : JSON.stringify(identity));
    });
    await assert.rejects(() => verifySimulatedBridge({...config, base_url}), scenario);
    assert.equal(requests, 1, 'redirects and inference routes must not be followed');
  }
});
