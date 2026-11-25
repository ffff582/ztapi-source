import assert from 'node:assert/strict';
import { execFileSync, execFile } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readdir, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { seatbeltArgs } from './macos-sandbox-policy.mjs';

assert.equal(process.platform, 'darwin');
assert.equal(process.arch, process.argv[2]);
const root = await realpath(await mkdtemp(join(tmpdir(), 'sandbox qa ')));
const work = join(root, 'staged project');
const executable = join(root, 'workbench-sandbox-fixture');
const original = join(root, 'original');
const forbidden = join(root, 'outside output');
const source = fileURLToPath(new URL('./macos-sandbox-fixture.c', import.meta.url));
const servers = [];
const hit = [0, 0];
const exec = promisify(execFile);
const appSandbox = process.argv[3] === 'app-sandbox';
const systemCacheRoots = ['/System/Cryptexes/OS/System/Library/dyld',
  '/System/Volumes/Preboot/Cryptexes/OS/System/Library/dyld', '/System/Library/dyld'];

async function sharedCache() {
  const cachePrefix = process.arch === 'arm64' ? 'dyld_shared_cache_arm64' : 'dyld_shared_cache_x86_64';
  for (const dir of systemCacheRoots) {
    const entries = await readdir(dir).catch(error => {
      if (error.code === 'ENOENT') return [];
      throw error;
    });
    if (entries.some(entry => entry.startsWith(cachePrefix))) {
      const canonical = await realpath(dir);
      console.log(`System loader cache: ${canonical}`);
      return canonical;
    }
  }
  console.log('No mach cache directory came from the known system roots');
  return null;
}

async function listener(index) {
  const server = createServer(socket => { hit[index]++; socket.end(); });
  servers.push(server);
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return String(server.address().port);
}

try {
  await mkdir(work);
  const staged = join(work, 'fixture');
  const result = join(work, 'output');
  await writeFile(staged, 'synthetic staged content', { flag: 'wx' });
  await writeFile(original, 'synthetic original content', { flag: 'wx' });
  execFileSync('/usr/bin/clang', ['-std=c11', '-Wall', '-Werror', source, '-o', executable], { timeout: 45_000 });
  const broker = await listener(0);
  const unrelated = await listener(1);
  if (appSandbox) {
    const homeFixture = await mkdtemp(join(homedir(), 'workbench-qa-fixture-'));
    try {
      const source = join(homeFixture, 'original');
      const outside = join(homeFixture, 'outside');
      const sandbox = join(root, 'sandbox.plist');
      await writeFile(source, 'synthetic original content', { flag: 'wx' });
      await writeFile(sandbox, `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>com.apple.security.app-sandbox</key><true/></dict></plist>`);
      execFileSync('/usr/bin/codesign', ['--force', '--sign', '-', '--entitlements', sandbox,
        '--identifier', 'com.ztapi.workbench.sandboxqa', executable], { timeout: 10_000 });
      const { stdout } = await exec(executable, ['--app-sandbox', source, outside, broker, unrelated], {
        timeout: 8_000, maxBuffer: 4096,
      });
      assert.equal(stdout.trim(), 'fixture pass; App Sandbox temp, file and network boundaries');
      assert.equal(await readFile(source, 'utf8'), 'synthetic original content');
      await assert.rejects(readFile(outside), { code: 'ENOENT' });
      assert.deepEqual(hit, [0, 0]);
      console.log(`Synthetic App Sandbox test runner verified on darwin/${process.arch}`);
    } finally {
      await rm(homeFixture, { recursive: true, force: true });
    }
  } else {
  const loaderCache = await sharedCache();
  for (const needsBroker of [true, false]) {
    const { stdout } = await exec('/usr/bin/sandbox-exec', seatbeltArgs({
      workspace: work, program: executable, args: [staged, result, original, forbidden, broker, unrelated],
      brokerPort: needsBroker ? Number(broker) : null, loaderCache,
    }), {
      cwd: work, env: { HOME: work, TMPDIR: work },
      timeout: 8_000, maxBuffer: 4096,
    });
    assert.equal(stdout.trim(), `fixture pass; broker ${needsBroker ? 'allowed' : 'denied'}`);
    assert.equal(await readFile(result, 'utf8'), 'ok');
    assert.equal(await readFile(original, 'utf8'), 'synthetic original content');
    await assert.rejects(readFile(forbidden), { code: 'ENOENT' });
  }
  assert.deepEqual(hit, [1, 0]);
  console.log(`Synthetic kernel sandbox verified on darwin/${process.arch}`);
  }
} finally {
  for (const server of servers) {
    const closed = once(server, 'close');
    server.close();
    await closed;
  }
  await rm(root, { recursive: true, force: true });
}
