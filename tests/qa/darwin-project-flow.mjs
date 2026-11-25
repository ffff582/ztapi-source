import assert from 'node:assert/strict';
import { execFile, execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { copyFile, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { createDarwinProjectReview } from '../../desktop/macos/project-review.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.version, 'v22.23.5');
const exec = promisify(execFile);
const source = fileURLToPath(new URL('../../desktop/macos/', import.meta.url));
const tmp = await realpath(await mkdtemp(join(tmpdir(), 'ztapi-qa-project-flow-')));
const root = await realpath(await mkdtemp(join(homedir(), 'ztapi-qa-original-')));
const canary = join(root, 'probe');
const target = join(root, 'src');
const broker = join(tmp, 'project-broker');
const bundle = join(tmp, 'ZTAPI Test Worker.app');
const macOS = join(bundle, 'Contents', 'MacOS');
const resources = join(bundle, 'Contents', 'Resources');
const host = join(macOS, 'ZTAPITestWorker');
const node = join(macOS, 'node');
const id = 'com.ztapi.workbench.qa.projecttest';
let hits = 0;
const listener = createServer(socket => { hits++; socket.end(); });

const plist = body => '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' +
  body + '</dict></plist>';
const field = (key, value) => `<key>${key}</key><string>${value}</string>`;
const sandbox = '<key>com.apple.security.app-sandbox</key><true/>';
const shell = (cmd, args) => execFileSync(cmd, args, { timeout: 110_000, stdio: 'inherit' });
try {
  await mkdir(macOS, { recursive: true });
  await mkdir(resources);
  await mkdir(target);
  await writeFile(join(bundle, 'Contents', 'Info.plist'), plist(
    field('CFBundleIdentifier', id) + field('CFBundleExecutable', 'ZTAPITestWorker') +
    field('CFBundlePackageType', 'APPL') + field('CFBundleName', 'ZTAPI Test Worker') +
    field('CFBundleVersion', '1')));
  await writeFile(join(tmp, 'worker-entitlements.plist'), plist(sandbox));
  await writeFile(join(tmp, 'node-entitlements.plist'), plist(sandbox + '<key>com.apple.security.inherit</key><true/>'));
  await copyFile(await realpath(process.execPath), node);
  for (const leaf of ['project-test-worker.mjs', 'project-test-input.mjs']) {
    await copyFile(join(source, leaf), join(resources, leaf));
  }
  shell('/usr/bin/clang', ['-std=c11', '-Wall', '-Werror', join(source, 'ProjectTestHost.c'), '-o', host]);
  shell('/usr/bin/swiftc', ['-O', join(source, 'ProjectBroker.swift'), '-o', broker]);
  shell('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(tmp, 'node-entitlements.plist'), node]);
  shell('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(tmp, 'worker-entitlements.plist'), bundle]);
  shell('/usr/bin/codesign', ['--verify', '--deep', '--strict', bundle]);
  await writeFile(canary, 'synthetic outside file', { flag: 'wx' });
  await writeFile(join(target, 'calc.mjs'), 'export const sum = (a, b) => a - b;');
  await writeFile(join(target, 'calc.test.mjs'), `
    import assert from 'node:assert/strict';
    import test from 'node:test';
    import { sum } from './calc.mjs';
    test('addition', () => assert.equal(sum(3, 4), 7));
  `);
  listener.listen(0, '127.0.0.1');
  await once(listener, 'listening');
  const call = async (program, args, input) => {
    const command = exec(program, args, { env: {}, timeout: 22_000, maxBuffer: 3 * 1024 * 1024 });
    command.child.stdin.on('error', () => {});
    command.child.stdin.end(JSON.stringify(input));
    try { return JSON.parse((await command).stdout); }
    catch (error) {
      if (error.stdout) throw Error(JSON.parse(error.stdout).code);
      throw error;
    }
  };
  let approved = false;
  const project = createDarwinProjectReview(await call(broker, ['snapshot'], { root: target }), {
    confirm: async ({ path }) => { assert.equal(path, 'calc.mjs'); return approved; },
    commit: input => call(broker, ['commit-file'], input),
  });
  const run = () => call(host, [], {
    name: 'calc.test.mjs', files: project.model.testSnapshot(), canary: { file: canary, port: listener.address().port },
  });
  assert.match(project.model.read('calc.mjs'), /a - b/);
  const failed = await run();
  assert.equal(failed.exitCode, 1);
  assert.match(failed.output, /not ok 1 - addition/);
  project.model.stageEdit('calc.mjs', 'export const sum = (a, b) => a + b;');
  const passed = await run();
  assert.equal(passed.exitCode, 0);
  assert.match(passed.output, /pass 1\b/);
  const preview = project.local.prepare('calc.mjs');
  assert.match(preview.before, /a - b/);
  assert.match(preview.after, /a \+ b/);
  assert.equal((await project.local.approve(preview.id)).status, 'rejected');
  assert.match(await readFile(join(target, 'calc.mjs'), 'utf8'), /a - b/);
  approved = true;
  assert.equal((await project.local.approve(project.local.prepare('calc.mjs').id)).status, 'committed');
  assert.match(await readFile(join(target, 'calc.mjs'), 'utf8'), /a \+ b/);
  assert.equal(hits, 0);
  console.log(`Native staged edit, sandbox test red/green, cancel and approve verified on ${process.arch}`);
} finally {
  if (listener.listening) {
    const closed = once(listener, 'close');
    listener.close();
    await closed;
  }
  await rm(tmp, { recursive: true, force: true });
  await rm(root, { recursive: true, force: true });
}
