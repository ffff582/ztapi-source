import assert from 'node:assert/strict';
import test from 'node:test';
import { seatbeltArgs } from './macos-sandbox-policy.mjs';

const base = { workspace: '/private/tmp/sample project', program: '/private/tmp/probe', args: ['--fixture'] };

test('engine profile opens just the pinned executable and exact task broker', () => {
  const args = seatbeltArgs({ ...base, brokerPort: 49623 });
  const policy = args[args.indexOf('-p') + 1];
  assert.match(policy, /\(deny default\)/);
  assert.match(policy, /\(allow process-exec \(literal \(param "PROGRAM"\)\)\)/);
  assert.match(policy, /\(allow network-outbound \(remote ip \(param "BROKER"\)\)\)/);
  assert.ok(args.includes('BROKER=127.0.0.1:49623'));
  assert.ok(!policy.includes(base.workspace));
  assert.deepEqual(args.slice(-2), [base.program, '--fixture']);
});

test('test-runner profile has no network rule, even for loopback', () => {
  const args = seatbeltArgs(base);
  assert.ok(!args.some(arg => /network-|BROKER/.test(arg)));
});

test('missing or overbroad paths and invalid broker ports fail closed', () => {
  for (const input of [
    { workspace: '/' }, { workspace: '/private/tmp/../' }, { workspace: 'relative' },
    { workspace: base.program }, { program: '../relative' }, { brokerPort: '' },
    { brokerPort: 0 }, { brokerPort: '80' }, { brokerPort: 65536 },
  ]) assert.throws(() => seatbeltArgs({ ...base, ...input }));
});
