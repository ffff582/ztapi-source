import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { copyFile, mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { serveEnginePipe } from '../../desktop/macos/engine-pipe.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.version, 'v22.23.5');
const source = fileURLToPath(new URL('../../desktop/macos/', import.meta.url));
const root = await realpath(await mkdtemp(join(tmpdir(), 'ztapi-qa-engine-')));
const outside = await realpath(await mkdtemp(join(homedir(), 'ztapi-qa-engine-outside-')));
const bundle = join(root, 'Workbench Engine Probe.app');
const mac = join(bundle, 'Contents', 'MacOS');
const res = join(bundle, 'Contents', 'Resources');
const bunSource = process.argv[2];
const runtime = bunSource ? 'bun' : 'node';
const binary = join(mac, runtime);
const host = join(mac, 'ZTAPIEngineProbe');
const id = 'com.ztapi.workbench.qa.engineprobe.' + runtime;
const fixture = 'darwin-engine-ipc-fixture.mjs';
const sourceFixture = fileURLToPath(new URL('./' + fixture, import.meta.url));
let hits = 0;
const listener = createServer(s => { hits++; s.end(); });
const sandbox = '<key>com.apple.security.app-sandbox</key><true/>';
const enc = body => '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' + body + '</dict></plist>';
const kv = (key, value) => `<key>${key}</key><string>${value}</string>`;
const call = (cmd, args) => execFileSync(cmd, args, { timeout: 40_000, stdio: 'inherit' });
let child;
try {
  await mkdir(mac, { recursive: true });
  await mkdir(res);
  const sourceBinary = await realpath(bunSource ?? process.execPath);
  if (bunSource) assert.equal(execFileSync(sourceBinary, ['--version'], { encoding: 'utf8' }).trim(), '1.4.4');
  await copyFile(sourceBinary, binary);
  await copyFile(sourceFixture, join(res, fixture));
  for (const file of ['engine-transport.mjs', 'engine-pipe.mjs']) await copyFile(join(source, file), join(res, file));
  await writeFile(join(bundle, 'Contents', 'Info.plist'), enc(kv('CFBundleIdentifier', id) +
    kv('CFBundleExecutable', 'ZTAPIEngineProbe') + kv('CFBundlePackageType', 'APPL') + kv('CFBundleVersion', '1')));
  await writeFile(join(root, 'app.plist'), enc(sandbox));
  await writeFile(join(root, 'helper.plist'), enc(sandbox + '<key>com.apple.security.inherit</key><true/>'));
  call('/usr/bin/clang', ['-Wall', '-Werror', '-std=c11', '-DZTAPI_WORKER_ENTRY="' + fixture + '"',
    '-DZTAPI_WORKER_RUNTIME="' + runtime + '"',
    join(source, 'ProjectTestHost.c'), '-o', host]);
  call('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'helper.plist'), binary]);
  call('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', join(root, 'app.plist'), bundle]);
  call('/usr/bin/codesign', ['--verify', '--deep', '--strict', bundle]);
  const canary = join(outside, 'file');
  await writeFile(canary, 'synthetic');
  listener.listen(0, '127.0.0.1');
  await once(listener, 'listening');
  child = spawn(host, [], {
    stdio: ['ignore', 'pipe', 'inherit', 'pipe', 'pipe'],
    env: { WORKBENCH_QA_CANARY: canary, WORKBENCH_QA_PORT: String(listener.address().port) },
  });
  const timer = setTimeout(() => child.kill('SIGKILL'), 12_000);
  const done = once(child, 'close').finally(() => clearTimeout(timer));
  let stdout = '';
  child.stdout.on('data', value => { stdout += value; if (stdout.length > 2048) child.kill('SIGKILL'); });
  const receipt = serveEnginePipe(child.stdio[4], child.stdio[3], frame => {
    if (frame.channel === 'model') {
      assert.equal(frame.body, '{"mode":"synthetic"}');
      return { status: 200, type: 'text/event-stream', body: 'event: message_stop\ndata: {}\n\n' };
    }
    assert.equal(frame.body, '{"name":"read_fixture"}');
    return { status: 200, type: 'application/json', body: '{"contents":"fixture"}' };
  });
  const [count, [code]] = await Promise.all([receipt, done]);
  assert.equal(code, 0);
  assert.equal(count, 2);
  assert.equal(hits, 0);
  assert.match(stdout, /Restricted model and tool IPC passed/);
  console.log(`Dedicated sandbox engine IPC verified on ${process.arch}/${runtime}; no API request`);
} finally {
  if (child?.pid && child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
  if (listener.listening) {
    const closed = once(listener, 'close');
    listener.close();
    await closed;
  }
  await rm(root, { recursive: true, force: true });
  await rm(outside, { recursive: true, force: true });
}
