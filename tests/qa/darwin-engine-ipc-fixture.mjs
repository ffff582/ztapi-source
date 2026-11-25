import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { connect, Socket } from 'node:net';
import { createEnginePipeClient } from './engine-pipe.mjs';
import { createMacSandboxFetch } from './engine-transport.mjs';

const rejected = error => ['EACCES', 'EPERM'].includes(error?.code);
await assert.rejects(readFile(process.env.WORKBENCH_QA_CANARY), rejected);
await assert.rejects(new Promise((resolve, reject) => {
  const socket = connect({ host: '127.0.0.1', port: Number(process.env.WORKBENCH_QA_PORT) });
  socket.once('error', reject);
  socket.once('connect', () => { socket.destroy(); resolve(); });
  socket.setTimeout(1500, () => { socket.destroy(); reject(Error('timeout')); });
}), error => rejected(error) || (Boolean(process.versions.bun) && error.code === 'ECONNREFUSED'));
const input = new Socket({ fd: 3, readable: true, writable: false });
const output = new Socket({ fd: 4, readable: false, writable: true });
const request = createEnginePipeClient(input, output);
const send = createMacSandboxFetch({ request });
try {
  const options = body => ({ method: 'POST', headers: { 'content-type': 'application/json' }, body });
  const messages = await send('http://ztapi-engine.invalid/v1/messages', options('{"mode":"synthetic"}'));
  assert.match(await messages.text(), /message_stop/);
  const tools = await send('http://ztapi-engine.invalid/__ztapi_mcp', options('{"name":"read_fixture"}'));
  assert.deepEqual(await tools.json(), { contents: 'fixture' });
  console.log('Restricted model and tool IPC passed');
} finally {
  input.destroy();
  output.destroy();
}
