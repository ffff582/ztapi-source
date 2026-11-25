import assert from 'node:assert/strict';
import test from 'node:test';
import { createDarwinProjectReview } from '../../desktop/macos/project-review.mjs';

const data = text => Buffer.from(text).toString('base64');
function fixture(confirm, commit) {
  return createDarwinProjectReview({
    root: '/Users/runner/project',
    rootIdentity: '1:1:1:1',
    files: [{ path: 'calc.mjs', data: data('before'), identity: '1:2:1:1' }],
  }, { confirm, commit });
}
const never = () => assert.fail('unexpected write or approval');

test('only the local approval capability can write', async () => {
  let writes = 0;
  const project = fixture(async ({ path }) => path === 'calc.mjs', async change => {
    assert.equal(change.before.data, data('before'));
    assert.equal(change.after, data('fixed'));
    writes++;
    return { status: 'committed', file: { ...change.before, data: change.after, identity: '1:3:1:1' } };
  });
  assert.equal(project.model.approve, undefined);
  assert.equal(project.model.read('calc.mjs'), 'before');
  project.model.stageEdit('calc.mjs', 'fixed');
  const plan = project.local.prepare('calc.mjs');
  assert.deepEqual([plan.before, plan.after], ['before', 'fixed']);
  assert.equal(writes, 0);
  assert.deepEqual(await project.local.approve(plan.id), { status: 'committed' });
  await assert.rejects(project.local.approve(plan.id), /REVIEW_ALREADY_USED/);
  assert.equal(writes, 1);
});

test('cancel, changed draft and concurrent approval do not reach native writeback', async () => {
  const declined = fixture(async () => false, never);
  declined.model.stageEdit('calc.mjs', 'fixed');
  assert.deepEqual(await declined.local.approve(declined.local.prepare('calc.mjs').id), { status: 'rejected' });

  const wait = Promise.withResolvers();
  const stale = fixture(() => wait.promise, never);
  stale.model.stageEdit('calc.mjs', 'first');
  const plan = stale.local.prepare('calc.mjs');
  const pending = stale.local.approve(plan.id);
  await assert.rejects(stale.local.approve(plan.id), /REVIEW_BUSY/);
  stale.model.stageEdit('calc.mjs', 'changed before approval');
  wait.resolve(true);
  await assert.rejects(pending, /REVIEW_STALE/);
});

test('native conflict leaves the attempted approval consumed, never auto-retries', async () => {
  let calls = 0;
  const project = fixture(async () => true, async () => { calls++; throw Error('PROJECT_CHANGED'); });
  assert.throws(() => project.model.stageEdit('../calc.mjs', 'not allowed'), /PROJECT_FILE_NOT_FOUND/);
  project.model.stageEdit('calc.mjs', 'fixed');
  const { id } = project.local.prepare('calc.mjs');
  await assert.rejects(project.local.approve(id), /PROJECT_CHANGED/);
  await assert.rejects(project.local.approve(id), /REVIEW_ALREADY_USED/);
  assert.equal(calls, 1);
});
