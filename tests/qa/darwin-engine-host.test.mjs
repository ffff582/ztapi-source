import assert from 'node:assert/strict';
import test from 'node:test';
import { engineOutput, invokeMacEngine } from '../../desktop/macos/engine-host.mjs';

const ready = { type: 'ztapi-sandbox', phase: 'verified' };
const stopped = { type: 'ztapi-sandbox', phase: 'cleaned' };
const part = text => ({ type: 'text', part: { text } });
const output = (...events) => events.map(event => JSON.stringify(event)).join('\n') + '\n';

test('accepts output only after sandbox verification and cleanup', () => {
  assert.equal(engineOutput(output(ready, part('synthetic'), part(' receipt'), stopped)), 'synthetic receipt');
  for (const value of [output(part('no sandbox'), stopped), output(ready, part('no cleanup')),
    output(ready, part('synthetic'), { type: 'error' }, stopped),
    output(ready, ready, part('synthetic'), stopped), output(ready, stopped), output(ready, part('x'.repeat(65_537)), stopped)])
    assert.throws(() => engineOutput(value), /MAC_ENGINE_OUTPUT_INVALID/);
});

test('never launches a task on an already-cancelled request or on another operating system', async () => {
  const signal = AbortSignal.abort();
  await assert.rejects(invokeMacEngine({ signal }), /PROJECT_TASK_CANCELLED/);
  if (process.platform !== 'darwin') await assert.rejects(invokeMacEngine({}), /MAC_ENGINE_SANDBOX_NOT_READY/);
});
