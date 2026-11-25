import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import test from 'node:test';
import { createEnginePipeClient, serveEnginePipe } from '../../desktop/macos/engine-pipe.mjs';

test('host dispatches only the two bound capabilities with monotonic request numbers', async () => {
  const out = new PassThrough(), incoming = new PassThrough();
  const calls = [];
  const done = serveEnginePipe(out, incoming, async frame => {
    calls.push(frame);
    return { status: 200, type: 'application/json', body: '{}' };
  });
  const send = createEnginePipeClient(incoming, out);
  await send({ channel: 'model', body: '{"model":"local"}' });
  await send({ channel: 'projectTool', body: '{"name":"read"}' });
  out.end();
  assert.equal(await done, 2);
  assert.deepEqual(calls.map(item => item.channel), ['model', 'projectTool']);
});

test('a partial or replayed request is not dispatched or retried', async () => {
  for (const wire of ['{"id":1', '{"id":2,"channel":"model","body":"{}"}\n', '{"id":1,"channel":"proxy","body":"{}"}\n']) {
    const out = new PassThrough(), incoming = new PassThrough();
    const done = serveEnginePipe(out, incoming, () => assert.fail('untrusted frame dispatched'));
    out.end(wire);
    await assert.rejects(done, /ENGINE_PIPE_UNCONFIRMED/);
  }
});
