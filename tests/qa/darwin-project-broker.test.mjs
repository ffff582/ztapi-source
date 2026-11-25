import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { linkSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';

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
  return { status: result.status, ...JSON.parse(result.stdout) };
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
  assert.equal(value.status, 0);
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
  assert.equal(call('commit-file', approved).status, 0);
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'fixed');
  assert.equal(call('commit-file', approved).code, 'PROJECT_CHANGED');
  renameSync(root, root + '-old');
  mkdirSync(root);
  writeFileSync(join(root, 'calc.mjs'), 'lookalike');
  assert.equal(call('commit-file', approved).code, 'PROJECT_CHANGED');
  assert.equal(readFileSync(join(root, 'calc.mjs'), 'utf8'), 'lookalike');
});
