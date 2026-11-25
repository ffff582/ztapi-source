import assert from 'node:assert/strict';
import test from 'node:test';
import { createMacSandboxFetch } from '../../desktop/macos/engine-transport.mjs';

const post = path => ['http://ztapi-engine.invalid' + path, {
  method: 'POST', headers: { 'content-type': 'application/json', authorization: 'local-placeholder' }, body: '{"model":"local"}',
}];

test('sends only the local Messages and tool operations over the broker capability', async () => {
  const calls = [];
  const send = createMacSandboxFetch({ request: async frame => {
    calls.push(frame);
    return { status: 200, type: 'text/event-stream', body: 'event: message_stop\ndata: {}\n\n' };
  } });
  const reply = await send(...post('/v1/messages'));
  assert.equal(reply.headers.get('content-type'), 'text/event-stream');
  assert.match(await reply.text(), /message_stop/);
  assert.deepEqual(calls, [{ channel: 'model', body: '{"model":"local"}' }]);
});

test('does not forward arbitrary hosts, redirects, oversized bodies, or method overrides', async () => {
  let dispatched = 0;
  const send = createMacSandboxFetch({ request: async () => { dispatched++; return { status: 302 }; } });
  const [uri, init] = post('/v1/messages');
  for (const [url, options] of [
    ['https://example.com/v1/messages', init], [uri + '?url=outside', init], post('/session'),
    [uri, { ...init, body: 'x'.repeat(135000) }], [uri, { ...init, method: 'PUT' }],
  ]) await assert.rejects(send(url, options), /ENGINE_TRANSPORT_REJECTED/);
  assert.equal(dispatched, 0);
  await assert.rejects(send(uri, init), /ENGINE_TRANSPORT_UNCONFIRMED/);
  assert.equal(dispatched, 1);
});
