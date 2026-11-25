import assert from 'node:assert/strict';
import test from 'node:test';
import { createProjectRpc } from '../../desktop/macos/engine-project-rpc.mjs';

const tool = { name: 'read_file', description: 'Read staged file', inputSchema: {
  type: 'object', properties: { path: { type: 'string' } }, required: ['path'], additionalProperties: false,
} };
const req = (method, id, params) => JSON.stringify({ jsonrpc: '2.0', id, method, params });
const init = req('initialize', 0, { protocolVersion: '2025-11-25' });

test('uses existing local project schema and executes only a bounded, non-replayed tool request', async () => {
  const calls = [];
  const rpc = createProjectRpc({ tools: [tool], limit: 1,
    execute: async (name, args) => { calls.push([name, args]); return { data: 'synthetic' }; },
  });
  assert.equal((await rpc(init)).status, 200);
  assert.equal((await rpc(req('notifications/initialized'))).status, 202);
  assert.deepEqual(JSON.parse((await rpc(req('tools/list', 1))).body).result.tools, [tool]);
  const read = req('tools/call', 2, { name: 'read_file', arguments: { path: 'calc.mjs' } });
  const result = JSON.parse((await rpc(read)).body);
  assert.equal(JSON.parse(result.result.content[0].text).data, 'synthetic');
  assert.equal((await rpc(req('notifications/cancelled', undefined, { requestId: 2 }))).status, 202);
  await assert.rejects(rpc(read), /MAC_ENGINE_PROJECT_RPC_REJECTED/);
  await assert.rejects(rpc(req('tools/call', 3, { name: 'read_file', arguments: { path: 'calc.mjs' } })),
    /MAC_ENGINE_PROJECT_RPC_REJECTED/);
  assert.deepEqual(calls, [['read_file', { path: 'calc.mjs' }]]);
});

test('cannot negotiate a remote capability, execute arbitrary tools or use production data in errors', async () => {
  const rpc = createProjectRpc({ tools: [tool], limit: 4, execute: () => { throw Error('secret'); } });
  await assert.rejects(rpc(req('tools/list', 99)), /MAC_ENGINE_PROJECT_RPC_REJECTED/);
  await rpc(init);
  await rpc(req('notifications/initialized'));
  await assert.rejects(rpc(req('tools/call', 1, { name: 'approve', arguments: {} })), /MAC_ENGINE_PROJECT_RPC_REJECTED/);
  await assert.rejects(rpc(req('tools/call', 2, { name: 'read_file', arguments: { path: 'x' } })),
    /MAC_ENGINE_PROJECT_TOOL_FAILED/);
  await assert.rejects(rpc(req('resources/read', 3, { uri: 'file:///private/data' })), /MAC_ENGINE_PROJECT_RPC_REJECTED/);
});
