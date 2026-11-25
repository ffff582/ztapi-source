import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { linkSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { createDarwinProjectReview } from '../../desktop/macos/project-review.mjs';

const src = fileURLToPath(new URL('../../desktop/macos/ProjectBroker.swift', import.meta.url));
readFileSync(src);
const isMac = process.platform === 'darwin';
let tmp, broker;
before(() => {
  if (!isMac) return;
  tmp = realpathSync(mkdtempSync(join(tmpdir(), 'workbench-broker-')));
  broker = join(tmp, 'project-broker');
  execFileSync('/usr/bin/swiftc', ['-O', src, '-o', broker], { timeout: 110_000, stdio: 'inherit' });
});
after(() => { if (tmp) rmSync(tmp, { recursive: true, force: true }); });

function call(mode, input) {
  const result = spawnSync(broker, [mode], { input: JSON.stringify(input), encoding: 'utf8', timeout: 8000 });
  assert.ifError(result.error);
  assert.equal(result.signal, null);
  return { exitCode: result.status, ...JSON.parse(result.stdout) };
}
function project(name) {
  const root = join(tmp, name);
  mkdirSync(root);
  writeFileSync(join(root, 'calc.mjs'), 'before');
  return root;
}
const data = value => Buffer.from(value).toString('base64');

test('imports actual project paths but skips links, credentials, and dependencies', { skip: !isMac }, () => {
  const root = project('project with spaces');
  const outside = join(tmp, 'private-sentinel');
  writeFileSync(outside, 'secret');
  mkdirSync(join(root, 'sub'));
  writeFileSync(join(root, 'sub', '\u8ba1\u7b97.mjs'), 'ok');
  mkdirSync(join(root, 'node_modules'));
  writeFileSync(join(root, 'node_modules', 'data'), 'secret');
  writeFileSync(join(root, '.env.local'), 'secret');
  symlinkSync(outside, join(root, 'symlink.txt'));
  linkSync(outside, join(root, 'hardlink.txt'));
  const value = call('snapshot', { root });
  assert.equal(value.exitCode, 0);
  assert.equal(value.root, root);
  assert.deepEqual(value.files.map(file => file.path), ['calc.mjs', 'sub/\u8ba1\u7b97.mjs']);
  assert.equal(value.files[0].data, data('before'));
  const alias = join(tmp, 'linked-root');
  symlinkSync(root, alias);
  assert.equal(call('snapshot', { root: alias }).code, 'PROJECT_ROOT_REJECTED');
});

test('commits the checked file and refuses stale or substituted targets', { skip: !isMac }, () => {
  const root = project('approval');
  const snap = call('snapshot', { root });
  const request = { root, rootIdentity: snap.rootIdentity, before: snap.files[0], after: data('fixed') };
  writeFileSync(join(root, 'calc.mjs'), 'external');
  assert.equal(call('commit-file', request).code, 'PROJECT_CHANGED');
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'external');
  const fresh = call('snapshot', { root });
  const approved = { ...request, before: fresh.files[0] };
  const committed = call('commit-file', approved);
  assert.equal(committed.exitCode, 0);
  assert.equal(committed.status, 'committed');
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'fixed');
  assert.equal(call('commit-file', approved).code, 'PROJECT_CHANGED');
  renameSync(root, root + '-old');
  mkdirSync(root);
  writeFileSync(join(root, 'calc.mjs'), 'lookalike');
  assert.equal(call('commit-file', approved).code, 'PROJECT_CHANGED');
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'lookalike');
});

test('writeback refuses a replaced parent directory or a sensitive target', { skip: !isMac }, () => {
  const root = project('redirected');
  const inside = join(root, 'sub');
  const target = project('other project');
  mkdirSync(inside);
  writeFileSync(join(inside, 'calc.mjs'), 'before');
  const snap = call('snapshot', { root });
  const before = snap.files.find(file => file.path === 'sub/calc.mjs');
  renameSync(inside, inside + '-original');
  symlinkSync(target, inside);
  const request = { root, rootIdentity: snap.rootIdentity, before, after: data('fixed') };
  assert.equal(call('commit-file', request).code, 'PROJECT_CHANGED');
  assert.equal(readFileSync(join(target, 'calc.mjs'), 'utf8'), 'before');
  assert.equal(call('commit-file', { ...request, before: { ...before, path: '.env' } }).code, 'PROJECT_REQUEST_INVALID');
});

test('local approval reaches the native broker, while cancellation only changes the preview', { skip: !isMac }, async () => {
  const root = project('native approval');
  let approved = false;
  const reviewed = createDarwinProjectReview(call('snapshot', { root }), {
    confirm: async details => { assert.equal(details.root, root); return approved; },
    commit: async input => {
      const result = call('commit-file', input);
      if (result.status !== 'committed') throw Error(result.code);
      return result;
    },
  });
  reviewed.model.stageEdit('calc.mjs', 'staged');
  const first = reviewed.local.prepare('calc.mjs');
  assert.deepEqual(await reviewed.local.approve(first.id), { status: 'rejected' });
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'before');
  approved = true;
  const second = reviewed.local.prepare('calc.mjs');
  assert.deepEqual(await reviewed.local.approve(second.id), { status: 'committed' });
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'staged');
});
