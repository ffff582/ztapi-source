import { execFile, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { lstat, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { isAbsolute, join } from 'node:path';
import { promisify } from 'node:util';
import { validateProjectTestInput } from './project-test-input.mjs';

const exec = promisify(execFile);
const snapshotLimit = 48 * 1024 * 1024;
const fail = () => Error('MAC_PROJECT_PORT_NOT_READY');
const nativeError = code => Error(/^(PROJECT_[A-Z_]+|MAC_TEST_RUNNER_FAILED)$/.test(code ?? '')
  ? code : 'MAC_PROJECT_PORT_NOT_READY');

function readReceipt(output) {
  let value;
  try { value = JSON.parse(output); }
  catch { throw fail(); }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw fail();
  if (value.code) throw nativeError(value.code);
  return value;
}

async function launch(file, args, input, { timeout, maxBuffer }) {
  const command = exec(file, args, { env: {}, timeout, maxBuffer, killSignal: 'SIGKILL' });
  command.child.stdin.on('error', () => {});
  command.child.stdin.end(input);
  try { return readReceipt((await command).stdout); }
  catch (error) {
    if (error.stdout) readReceipt(error.stdout);
    throw nativeError(error.message);
  }
}

export async function createMacProjectPorts({ broker, worker, state, diff }) {
  if (process.platform !== 'darwin' || ![broker, worker, state].every(value => typeof value === 'string' && isAbsolute(value)) ||
      typeof diff !== 'function') throw fail();
  for (const target of [broker, worker]) {
    const entry = await lstat(target);
    if (!entry.isFile() || entry.isSymbolicLink() || await realpath(target) !== target) throw fail();
  }
  if (!(await lstat(state)).isDirectory() || await realpath(state) !== state) throw fail();
  let testing = false;
  return Object.freeze({
    snapshot(root) {
      const result = spawnSync(broker, ['snapshot'], { env: {}, input: JSON.stringify({ root }),
        encoding: 'utf8', timeout: 12_000, maxBuffer: snapshotLimit, killSignal: 'SIGKILL' });
      if (result.error || result.signal) throw fail();
      const receipt = readReceipt(result.stdout);
      if (result.status !== 0 || receipt.root !== root || !Array.isArray(receipt.files)) throw fail();
      return receipt;
    },
    async commit(request) {
      return launch(broker, ['commit-file'], JSON.stringify(request), { timeout: 12_000, maxBuffer: 3 * 1024 * 1024 });
    },
    async test({ name, files }) {
      if (testing) throw Error('PROJECT_TASK_BUSY');
      validateProjectTestInput({ name, files });
      testing = true;
      let temporary;
      let connected = false;
      const canary = createServer(socket => { connected = true; socket.destroy(); });
      try {
        temporary = await mkdtemp(join(state, 'test-canary-'));
        const file = join(temporary, 'probe');
        await writeFile(file, 'sandbox canary', { flag: 'wx', mode: 0o600 });
        canary.listen(0, '127.0.0.1');
        await once(canary, 'listening');
        const value = await launch(worker, [], JSON.stringify({
          name, files, canary: { file, port: canary.address().port },
        }), { timeout: 22_000, maxBuffer: 2 * 1024 * 1024 });
        if (connected || typeof value.output !== 'string' || Buffer.byteLength(value.output) > 262144 ||
            !Number.isInteger(value.exitCode) || value.exitCode < 0 || value.exitCode > 255) throw fail();
        return { output: value.output, exitCode: value.exitCode };
      } finally {
        try {
          if (canary.listening) {
            const closed = once(canary, 'close');
            canary.close();
            await closed;
          }
          if (temporary) await rm(temporary, { recursive: true, force: true });
        } finally { testing = false; }
      }
    },
    diff,
  });
}
