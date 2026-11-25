import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { chmod, copyFile, mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { serveEnginePipe } from '../../desktop/macos/engine-pipe.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.version, 'v22.23.5');
assert.equal(process.argv.length, 3);

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
let hits = 0, child, closed;
const listener = createServer(socket => { hits++; socket.end(); });
const sandbox = '<key>com.apple.security.app-sandbox</key><true/>';
const plist = body => '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' + body + '</dict></plist>';
const field = (key, val) => `<key>${key}</key><string>${val}</string>`;
const run = (bin, args) => execFileSync(bin, args, { timeout: 45_000, stdio: 'inherit' });
function fixtureMessage() {
  const events = [
    { type: 'message_start', message: { id: 'msg_ztapi_offline_fixture', type: 'message', role: 'assistant',
      model: 'ztapi-task-model', content: [], stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 4, output_tokens: 0 } } },
    { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
    { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: marker } },
    { type: 'content_block_stop', index: 0 },
    { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 6 } },
    { type: 'message_stop' },
  ];
  return events.map(row => `event: ${row.type}\ndata: ${JSON.stringify(row)}\n\n`).join('');
}

try {
  await mkdir(mac, { recursive: true });
  await mkdir(res);
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
  run('/usr/bin/clang', ['-Wall', '-Werror', '-std=c11', '-DZTAPI_WORKER_ENTRY="opencode-launch.mjs"',
    join(source, 'ProjectTestHost.c'), '-o', host]);
  for (const leaf of ['node', 'opencode']) {
    run('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'inherit.plist'), join(mac, leaf)]);
  }
  run('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'app.plist'), bundle]);
  run('/usr/bin/codesign', ['--verify', '--deep', '--strict', bundle]);
  const canary = join(outside, 'file');
  await writeFile(canary, 'synthetic file outside the sandbox', { flag: 'wx' });
  listener.listen(0, '127.0.0.1');
  await once(listener, 'listening');
  child = spawn(host, [], { detached: true, stdio: ['pipe', 'pipe', 'inherit', 'pipe', 'pipe'],
    env: { ZTAPI_ENGINE_MODE: 'text', ZTAPI_ENGINE_CANARY: canary, ZTAPI_ENGINE_TEST_PORT: String(listener.address().port),
      ZTAPI_ENGINE_QA_TRACE: '1' } });
  const kill = () => {
    if (!child?.pid) return;
    try { process.kill(-child.pid, 'SIGKILL'); }
    catch (error) { if (error.code !== 'ESRCH') throw error; }
  };
  const alarm = setTimeout(kill, 38_000);
  closed = once(child, 'close').finally(() => clearTimeout(alarm));
  child.stdin.on('error', () => {});
  child.stdin.end('Respond to the synthetic Workbench acceptance prompt.');
  let out = '', calls = 0;
  child.stdout.on('data', chunk => { out += chunk; if (out.length > 262144) kill(); });
  const relay = serveEnginePipe(child.stdio[4], child.stdio[3], ({ channel, body }) => {
    assert.equal(channel, 'model');
    assert.equal(++calls, 1);
    const wire = JSON.parse(body);
    assert.equal(wire.model, 'ztapi-task-model');
    assert.equal(wire.stream, true);
    assert.ok(wire.max_tokens > 0 && wire.max_tokens <= 2048);
    assert.match(JSON.stringify(wire.messages), /synthetic Workbench acceptance prompt/);
    return { status: 200, type: 'text/event-stream', body: fixtureMessage() };
  }).catch(err => { kill(); throw err; });
  const [count, [code, signal]] = await Promise.all([relay, closed]);
  if (code !== 0 || count !== 1) console.error(out.slice(0, 8192));
  assert.equal(code, 0, `Engine exited with ${signal ?? code}; model frames=${count}`);
  assert.equal(count, 1);
  assert.equal(hits, 0);
  const events = out.trim().split(/\r?\n/).map(line => JSON.parse(line));
  assert.equal(events.filter(event => event.type === 'text').map(event => event.part?.text ?? '').join('').trim(), marker);
  console.log(`OpenCode 1.19.6 produced a real CLI response through local sandbox IPC on ${process.arch}; no paid request`);
} finally {
  if (child?.pid && child.exitCode === null && child.signalCode === null) {
    try { process.kill(-child.pid, 'SIGKILL'); }
    catch (error) { if (error.code !== 'ESRCH') throw error; }
    await closed?.catch(() => {});
  }
  if (listener.listening) {
    const done = once(listener, 'close');
    listener.close();
    await done;
  }
  await rm(root, { recursive: true, force: true });
  await rm(outside, { recursive: true, force: true });
}
