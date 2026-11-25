import { spawn } from 'node:child_process';
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { isAbsolute, dirname, join } from 'node:path';
import { createConnection } from 'node:net';
import { validateProjectTestInput } from './project-test-input.mjs';

async function readInput() {
  const parts = [];
  let size = 0;
  for await (const part of process.stdin) {
    if ((size += part.length) > 46 * 1024 * 1024) throw Error('PROJECT_LIMIT');
    parts.push(part);
  }
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(parts)));
}

const denied = error => ['EACCES', 'EPERM'].includes(error?.code);
async function proveSandbox({ file, port } = {}) {
  if (typeof file !== 'string' || !isAbsolute(file) ||
      !Number.isInteger(port) || port <= 0 || port > 65535) throw Error('MAC_TEST_SANDBOX_NOT_READY');
  const rejectedFile = await readFile(file).then(() => false, denied);
  const rejectedNetwork = await new Promise(resolve => {
    const socket = createConnection({ host: '127.0.0.1', port });
    socket.once('connect', () => { socket.destroy(); resolve(false); });
    socket.once('error', error => resolve(denied(error)));
    socket.setTimeout(1500, () => { socket.destroy(); resolve(false); });
  });
  if (!rejectedFile || !rejectedNetwork) throw Error('MAC_TEST_SANDBOX_NOT_READY');
}

function nodeTest(root, name) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['--test', '--experimental-test-isolation=none', join(root, name)], {
      cwd: root, detached: true, env: { HOME: root, TMPDIR: root }, stdio: ['ignore', 'pipe', 'pipe'],
    });
    const chunks = [];
    let bytes = 0, failed = false;
    const killGroup = () => {
      if (child.pid) {
        try { process.kill(-child.pid, 'SIGKILL'); }
        catch (error) { if (error.code !== 'ESRCH') failed = true; }
      }
    };
    const stop = () => { failed = true; killGroup(); };
    const timeout = setTimeout(stop, 12_000);
    const add = chunk => {
      if ((bytes += chunk.length) <= 256 * 1024) chunks.push(chunk);
      else stop();
    };
    child.stdout.on('data', add);
    child.stderr.on('data', add);
    child.once('error', () => { clearTimeout(timeout); reject(Error('MAC_TEST_RUNNER_FAILED')); });
    child.once('exit', killGroup);
    child.once('close', (code, signal) => {
      clearTimeout(timeout);
      if (failed || signal || !Number.isSafeInteger(code)) reject(Error('MAC_TEST_RUNNER_FAILED'));
      else resolve({ exitCode: code, output: Buffer.concat(chunks).toString('utf8') });
    });
  });
}

async function main() {
  if (process.platform !== 'darwin' || !process.env.ZTAPI_TEST_TMPDIR) throw Error('MAC_TEST_SANDBOX_NOT_READY');
  const input = await readInput();
  await proveSandbox(input?.canary);
  const { name, files } = validateProjectTestInput(input);
  const tmp = await realpath(process.env.ZTAPI_TEST_TMPDIR);
  const root = await mkdtemp(join(tmp, 'ztapi-project-test-'));
  try {
    for (const file of files) {
      const target = join(root, file.path);
      await mkdir(dirname(target), { recursive: true, mode: 0o700 });
      await writeFile(target, file.data, { flag: 'wx', mode: 0o600 });
    }
    return await nodeTest(root, name);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

try { process.stdout.write(JSON.stringify(await main())); }
catch {
  process.stdout.write(JSON.stringify({ code: 'MAC_TEST_RUNNER_FAILED' }));
  process.exitCode = 1;
}
