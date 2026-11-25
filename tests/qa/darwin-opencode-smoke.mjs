import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { chmod, copyFile, mkdir, mkdtemp, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { invokeMacEngine } from '../../desktop/macos/engine-host.mjs';
import { createProjectRpc } from '../../desktop/macos/engine-project-rpc.mjs';
import { createDarwinQaProject } from './darwin-project-flow.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.version, 'v22.23.5');
const mode = process.argv[3] ?? 'text';
assert.ok(['text', 'project'].includes(mode));

const marker = 'ZTAPI_OPEN_CODE_OFFLINE_OK';
const source = fileURLToPath(new URL('../../desktop/macos/', import.meta.url));
const supplied = await realpath(process.argv[2]);
assert.equal(execFileSync(supplied, ['--version'], { timeout: 15_000, encoding: 'utf8' }).trim(), '1.19.6');
const root = await realpath(await mkdtemp(join(tmpdir(), 'ztapi-qa-opencode-')));
const outside = await realpath(await mkdtemp(join(homedir(), 'ztapi-qa-opencode-outside-')));
const bundle = join(root, 'Workbench OpenCode QA.app');
const mac = join(bundle, 'Contents', 'MacOS');
const res = join(bundle, 'Contents', 'Resources');
const host = join(mac, 'ZTAPIOpenCodeQA');
const id = 'com.ztapi.workbench.qa.opencode';
let projectFixture;
const sandbox = '<key>com.apple.security.app-sandbox</key><true/>';
const plist = body => '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' + body + '</dict></plist>';
const field = (key, val) => `<key>${key}</key><string>${val}</string>`;
const run = (bin, args) => execFileSync(bin, args, { timeout: 45_000, stdio: 'inherit' });
function fixtureMessage(turn, tool) {
  const block = tool ? { type: 'tool_use', id: 'toolu_ztapi_qa_' + turn, name: 'mcp__ztapi_' + tool.name, input: {} }
    : { type: 'text', text: '' };
  const delta = tool ? { type: 'input_json_delta', partial_json: JSON.stringify(tool.arguments) }
    : { type: 'text_delta', text: marker };
  const events = [
    { type: 'message_start', message: { id: 'msg_ztapi_offline_fixture_' + turn, type: 'message', role: 'assistant',
      model: 'ztapi-task-model', content: [], stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 4, output_tokens: 0 } } },
    { type: 'content_block_start', index: 0, content_block: block },
    { type: 'content_block_delta', index: 0, delta },
    { type: 'content_block_stop', index: 0 },
    { type: 'message_delta', delta: { stop_reason: tool ? 'tool_use' : 'end_turn', stop_sequence: null }, usage: { output_tokens: 6 } },
    { type: 'message_stop' },
  ];
  return events.map(row => `event: ${row.type}\ndata: ${JSON.stringify(row)}\n\n`).join('');
}

try {
  await mkdir(mac, { recursive: true });
  await mkdir(res);
  const configHome = join(res, 'engine-config');
  const config = join(configHome, 'opencode');
  await mkdir(config, { recursive: true });
  // OpenCode skips runtime npm installs when the pre-created config directory is read-only.
  await writeFile(join(config, '.gitignore'), 'node_modules\n', { mode: 0o444, flag: 'wx' });
  await chmod(config, 0o555);
  await chmod(configHome, 0o555);
  for (const leaf of ['engine-config.mjs', 'engine-pipe.mjs', 'engine-transport.mjs', 'opencode-pipe-plugin.mjs', 'opencode-launch.mjs']) {
    await copyFile(join(source, leaf), join(res, leaf));
  }
  await copyFile(await realpath(process.execPath), join(mac, 'node'));
  await copyFile(supplied, join(mac, 'opencode'));
  await chmod(join(mac, 'opencode'), 0o755);
  await writeFile(join(bundle, 'Contents', 'Info.plist'), plist(field('CFBundleIdentifier', id) +
    field('CFBundleExecutable', 'ZTAPIOpenCodeQA') + field('CFBundlePackageType', 'APPL') + field('CFBundleVersion', '1')));
  await writeFile(join(root, 'app.plist'), plist(sandbox));
  await writeFile(join(root, 'inherit.plist'), plist(sandbox + '<key>com.apple.security.inherit</key><true/>'));
  if (mode === 'project') projectFixture = await createDarwinQaProject();
  run('/usr/bin/clang', ['-Wall', '-Werror', '-std=c11', '-DZTAPI_WORKER_ENTRY="opencode-launch.mjs"',
    join(source, 'ProjectTestHost.c'), '-o', host]);
  for (const leaf of ['node', 'opencode']) {
    run('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'inherit.plist'), join(mac, leaf)]);
  }
  run('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'app.plist'), bundle]);
  run('/usr/bin/codesign', ['--verify', '--deep', '--strict', bundle]);
  const hostOptions = { host, state: outside, bundleId: id, timeoutMs: 35_000,
    prompt: 'Respond to the synthetic Workbench acceptance prompt.' };
  assert.deepEqual(await invokeMacEngine({ ...hostOptions, mode: 'probe' }), { summary: '', frames: 0 });
  let calls = 0;
  const draft = projectFixture?.project.openDraft();
  const corrected = 'export const sum = (a, b) => a + b;';
  const actions = [
    { name: 'read_file', arguments: { path: 'calc.mjs' } },
    { name: 'stage_edit', arguments: { path: 'calc.mjs', content: corrected } },
    { name: 'run_test', arguments: { path: 'calc.test.mjs' } },
  ];
  const invoked = [];
  const projectRpc = draft ? createProjectRpc({
    tools: actions.map(({ name, arguments: args }) => ({ name, description: name, inputSchema: {
      type: 'object', properties: Object.fromEntries(Object.keys(args).map(key => [key, { type: 'string' }])),
      required: Object.keys(args), additionalProperties: false,
    } })),
    limit: 3,
    async execute(name, args) {
      const next = actions[invoked.length];
      assert.ok(next);
      assert.equal(name, next.name);
      assert.deepEqual(args, next.arguments);
      invoked.push(next.name);
      if (name === 'read_file') {
        assert.match(draft.draft.read(args.path), /a - b/);
        return { receipt: 'ZTAPI_QA_FILE_READ' };
      }
      if (name === 'stage_edit') {
        draft.draft.stageEdit(args.path, args.content);
        return { receipt: 'ZTAPI_QA_FILE_STAGED' };
      }
      const tested = await draft.draft.runTest(args.path);
      assert.equal(tested.exitCode, 0);
      assert.match(tested.output, /pass 2\b/);
      return { receipt: 'ZTAPI_QA_TESTS_PASSED' };
    },
  }) : null;
  const dispatch = async ({ channel, body }) => {
    const wire = JSON.parse(body);
    if (channel === 'projectTool') {
      assert.ok(projectRpc);
      return projectRpc(body);
    }
    assert.equal(channel, 'model');
    assert.ok(++calls <= (draft ? 4 : 1));
    assert.equal(wire.model, 'ztapi-task-model');
    assert.equal(wire.stream, true);
    assert.ok(wire.max_tokens > 0 && wire.max_tokens <= 2048);
    assert.match(JSON.stringify(wire.messages), /synthetic Workbench acceptance prompt/);
    if (draft && calls > 1) {
      const receipt = ['ZTAPI_QA_FILE_READ', 'ZTAPI_QA_FILE_STAGED', 'ZTAPI_QA_TESTS_PASSED'][calls - 2];
      if (!JSON.stringify(wire.messages).includes(receipt))
        console.error('Synthetic last tool response: %s', JSON.stringify(wire.messages.at(-1)).slice(0, 1400));
      assert.ok(JSON.stringify(wire.messages).includes(receipt), 'Missing synthetic tool receipt: ' + receipt);
    }
    const action = draft ? actions[calls - 1] : undefined;
    if (action) assert.ok(wire.tools.some(tool => tool.name === 'mcp__ztapi_' + action.name));
    return { status: 200, type: 'text/event-stream', body: fixtureMessage(calls, action) };
  };
  const result = await invokeMacEngine({ ...hostOptions, mode, dispatch });
  assert.equal(calls, draft ? 4 : 1);
  assert.equal(result.summary, marker);
  assert.deepEqual(await readdir(outside), []);
  if (draft) {
    assert.deepEqual(invoked, actions.map(action => action.name));
    assert.match(await projectFixture.original(), /a - b/);
    draft.commit();
    const project = projectFixture.project;
    assert.match(project.diff(project.prepareReview().id, 'calc.mjs').text, /a - b[\s\S]*a \+ b/);
    projectFixture.allowWriteback();
    assert.equal((await project.approve(project.prepareReview().id, ['calc.mjs'])).status, 'committed');
    assert.equal(await projectFixture.original(), corrected);
  } else {
    const controller = new AbortController();
    let cancelledCalls = 0;
    await assert.rejects(invokeMacEngine({ ...hostOptions, mode, signal: controller.signal, async dispatch() {
      assert.equal(++cancelledCalls, 1);
      controller.abort();
      throw Error('synthetic cancellation');
    } }), /PROJECT_TASK_CANCELLED/);
    assert.equal(cancelledCalls, 1);
    assert.deepEqual(await readdir(outside), []);
  }
  console.log(`OpenCode 1.19.6 verified ${mode} through local sandbox IPC on ${process.arch}; no paid request`);
} finally {
  await projectFixture?.dispose();
  const configHome = join(res, 'engine-config');
  await chmod(configHome, 0o755).catch(() => {});
  await chmod(join(configHome, 'opencode'), 0o755).catch(() => {});
  await rm(root, { recursive: true, force: true });
  await rm(outside, { recursive: true, force: true });
}
