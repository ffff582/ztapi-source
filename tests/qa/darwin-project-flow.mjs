import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { copyFile, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createMacProjectPorts } from '../../desktop/macos/project-ports.mjs';
import { MacProjectTask } from '../../desktop/macos/project-task.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.version, 'v22.23.5');

export async function createDarwinQaProject() {
  const source = fileURLToPath(new URL('../../desktop/macos/', import.meta.url));
  const tmp = await realpath(await mkdtemp(join(tmpdir(), 'ztapi-qa-project-flow-')));
  const root = await realpath(await mkdtemp(join(homedir(), 'ztapi-qa-original-')));
  const target = join(root, 'src');
  const broker = join(tmp, 'project-broker');
  const bundle = join(tmp, 'ZTAPI Test Worker.app');
  const macOS = join(bundle, 'Contents', 'MacOS');
  const resources = join(bundle, 'Contents', 'Resources');
  const host = join(macOS, 'ZTAPITestWorker');
  const node = join(macOS, 'node');
  const id = 'com.ztapi.workbench.qa.projecttest';

  const plist = body => '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' +
    body + '</dict></plist>';
  const field = (key, value) => `<key>${key}</key><string>${value}</string>`;
  const sandbox = '<key>com.apple.security.app-sandbox</key><true/>';
  const shell = (cmd, args) => execFileSync(cmd, args, { timeout: 110_000, stdio: 'inherit' });
  const dispose = async () => {
    await rm(tmp, { recursive: true, force: true });
    await rm(root, { recursive: true, force: true });
  };
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
    await writeFile(join(target, 'calc.mjs'), 'export const sum = (a, b) => a - b;');
    await writeFile(join(target, 'calc.test.mjs'), `
      import assert from 'node:assert/strict';
      import { spawnSync } from 'node:child_process';
      import test from 'node:test';
      import { sum } from './calc.mjs';
      test('addition', () => assert.equal(sum(3, 4), 7));
      test('project tests cannot start a child process through Node', () => {
        assert.throws(() => spawnSync('/usr/bin/true'), { code: 'ERR_ACCESS_DENIED' });
      });
    `);
    let approved = false;
    const ports = await createMacProjectPorts({ broker, worker: host, state: root,
      diff: (before, after) => Buffer.from(before, 'base64') + '\n' + Buffer.from(after, 'base64'),
    });
    const project = new MacProjectTask({ source: target, accountId: 1,
      confirm: async ({ files }) => { assert.deepEqual(files, ['calc.mjs']); return approved; },
    }, ports);
    return { project, dispose, original: () => readFile(join(target, 'calc.mjs'), 'utf8'),
      allowWriteback: () => { approved = true; } };
  } catch (error) {
    await dispose();
    throw error;
  }
}

async function verifyProjectFlow() {
  const { project, dispose, original, allowWriteback } = await createDarwinQaProject();
  try {
    assert.match(project.read('calc.mjs'), /a - b/);
    const failed = await project.runTest('calc.test.mjs');
    assert.equal(failed.exitCode, 1);
    assert.match(failed.output, /not ok 1 - addition/);
    const edit = project.openDraft();
    edit.draft.stageEdit('calc.mjs', 'export const sum = (a, b) => a + b;');
    const passed = await edit.draft.runTest('calc.test.mjs');
    assert.equal(passed.exitCode, 0);
    assert.match(passed.output, /pass 2\b/);
    edit.commit();
    const preview = project.prepareReview();
    assert.match(project.diff(preview.id, 'calc.mjs').text, /a - b[\s\S]*a \+ b/);
    assert.equal((await project.approve(preview.id, ['calc.mjs'])).status, 'rejected');
    assert.match(await original(), /a - b/);
    allowWriteback();
    assert.equal((await project.approve(project.prepareReview().id, ['calc.mjs'])).status, 'committed');
    assert.match(await original(), /a \+ b/);
    assert.equal(project.changeStatus().count, 0);
    console.log(`Native staged edit, sandbox test red/green, cancel and approve verified on ${process.arch}`);
  } finally { await dispose(); }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) await verifyProjectFlow();
