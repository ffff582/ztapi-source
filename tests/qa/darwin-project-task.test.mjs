import assert from 'node:assert/strict';
import test from 'node:test';
import { MacProjectTask } from '../../desktop/macos/project-task.mjs';

const root = '/Users/runner/synthetic-project';
const encoded = value => Buffer.from(value).toString('base64');
function session({ confirm, restore, snapshot } = {}) {
  const writes = [];
  const input = snapshot ?? { root, rootIdentity: '1:1:1:1', files: [
    { path: 'calc.mjs', data: encoded('broken'), identity: '1:2:1:1' },
    { path: 'calc.test.mjs', data: encoded('test'), identity: '1:3:1:1' },
  ] };
  const task = new MacProjectTask({ source: root, accountId: 1, restore, confirm: confirm ?? (() => true) }, {
    snapshot: () => input,
    test: async request => {
      assert.equal(request.name, 'calc.test.mjs');
      return { exitCode: request.files[0].data === encoded('fixed') ? 0 : 1, output: 'synthetic result' };
    },
    commit: async request => {
      writes.push(request);
      return { status: 'committed', file: { ...request.before, data: request.after, identity: '1:4:1:1' } };
    },
    diff: (before, after) => '-' + Buffer.from(before, 'base64') + '\n+' + Buffer.from(after, 'base64'),
  });
  return { task, writes };
}

test('task draft runs a staged test without exposing approval, stage commit remains local', async () => {
  const { task, writes } = session();
  const undecided = task.openDraft();
  undecided.draft.stageEdit('calc.mjs', 'fixed');
  assert.equal(undecided.draft.approve, undefined);
  assert.equal(await undecided.draft.runTest('calc.test.mjs').then(result => result.exitCode), 0);
  undecided.discard();
  assert.equal(task.read('calc.mjs'), 'broken');
  const draft = task.openDraft();
  draft.draft.stageEdit('calc.mjs', 'fixed');
  draft.commit();
  const review = task.prepareReview();
  assert.deepEqual(review.files, [{ path: 'calc.mjs', kind: 'modified' }]);
  assert.equal(task.diff(review.id, 'calc.mjs', 0).text, '-broken\n+fixed');
  assert.equal(writes.length, 0);
  assert.equal((await task.approve(review.id, ['calc.mjs'])).status, 'committed');
  assert.equal(writes.length, 1);
  assert.equal(task.changeStatus().count, 0);
});

test('checkpoints restore staged edits but not on a different file identity', () => {
  const { task } = session();
  task.stageEdit('calc.mjs', 'fixed');
  const restore = task.checkpoint();
  assert.equal(session({ restore }).task.read('calc.mjs'), 'fixed');
  assert.throws(() => session({ restore, snapshot: { root, rootIdentity: '1:5:1:1', files: [] } }), /SESSION_PROJECT_CHANGED/);
});

test('stale reviews and multi-file requests do not write any file', async () => {
  const { task, writes } = session();
  task.stageEdit('calc.mjs', 'one');
  task.stageEdit('calc.test.mjs', 'two');
  const plan = task.prepareReview();
  await assert.rejects(task.approve(plan.id, ['calc.mjs', 'calc.test.mjs']), /REVIEW_FILE_LIMIT/);
  task.stageEdit('calc.mjs', 'three');
  await assert.rejects(task.approve(plan.id, ['calc.mjs']), /REVIEW_STALE/);
  assert.equal(writes.length, 0);
});
