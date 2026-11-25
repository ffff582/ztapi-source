import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { access, mkdtemp, lstat, readFile, realpath, rm } from 'node:fs/promises';
import { constants, fstatSync } from 'node:fs';
import { connect, Socket } from 'node:net';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { openCodeSandboxEnvironment } from './engine-config.mjs';

const canaryError = error => ['EPERM', 'EACCES'].includes(error?.code);
const setupError = () => Error('MAC_ENGINE_SANDBOX_NOT_READY');

async function prepare() {
  if (process.platform !== 'darwin' || !process.env.ZTAPI_TEST_TMPDIR) throw setupError();
  const canary = process.env.ZTAPI_ENGINE_CANARY;
  const port = Number(process.env.ZTAPI_ENGINE_TEST_PORT);
  if (!canary?.startsWith('/') || !Number.isInteger(port) || port < 1 || port > 65535) throw setupError();
  if (!fstatSync(5).isSocket()) throw setupError();
  if (!await readFile(canary).then(() => false, canaryError)) throw setupError();
  const config = fileURLToPath(new URL('./engine-config/opencode/', import.meta.url));
  if (!await access(config, constants.W_OK).then(() => false, canaryError)) throw setupError();
  if (!await new Promise(resolve => {
    const probe = connect({ host: '127.0.0.1', port });
    probe.once('connect', () => { probe.destroy(); resolve(false); });
    probe.once('error', error => resolve(canaryError(error)));
    probe.setTimeout(1500, () => { probe.destroy(); resolve(false); });
  })) throw setupError();
  const binary = fileURLToPath(new URL('../MacOS/opencode', import.meta.url));
  const metadata = await lstat(binary);
  if (!metadata.isFile() || metadata.isSymbolicLink()) throw setupError();
  return binary;
}

async function run() {
  const { ZTAPI_ENGINE_MODE: mode, ZTAPI_ENGINE_BUNDLE_ID: containerId } = process.env;
  const deadline = Number(process.env.ZTAPI_ENGINE_DEADLINE_MS);
  if (!['probe', 'text', 'project'].includes(mode) ||
      !['com.ztapi.workbench.engine', 'com.ztapi.workbench.qa.opencode'].includes(containerId) ||
      !Number.isSafeInteger(deadline) || deadline < 1000 || deadline > 20 * 60_000) throw setupError();
  const control = new Socket({ fd: 5, readable: true, writable: false });
  let stopped = false, child;
  const stop = () => { stopped = true; child?.kill('SIGKILL'); };
  control.once('end', stop).once('error', stop).once('data', stop).resume();
  const timeout = setTimeout(stop, deadline);
  process.once('SIGTERM', stop);
  let root;
  try {
    const binary = await prepare();
    if (stopped) throw setupError();
    const marker = phase => console.log(JSON.stringify({ type: 'ztapi-sandbox', phase }));
    if (mode === 'probe') { marker('verified'); marker('cleaned'); return; }
    root = await mkdtemp(join(await realpath(process.env.ZTAPI_TEST_TMPDIR), 'ztapi-task-'));
    const env = openCodeSandboxEnvironment({
      workspace: root, mode, containerId,
      plugin: new URL('./opencode-pipe-plugin.mjs', import.meta.url),
    });
    if (stopped) throw setupError();
    marker('verified');
    const trace = process.env.ZTAPI_ENGINE_QA_TRACE === '1';
    const flags = trace ? ['--print-logs', '--log-level', 'DEBUG'] : [];
    child = spawn(binary, [...flags, 'run', '--format', 'json', '--model', 'ztapi/ztapi-task-model'], {
      cwd: root, env: { ...env, ...(trace ? { ZTAPI_ENGINE_QA_TRACE: '1' } : {}) }, stdio: ['inherit', 'inherit', 'inherit', 3, 4],
    });
    const [code, signal] = await once(child, 'close');
    if (trace) console.error(`OpenCode QA subprocess stopped: ${signal ?? code}`);
    if (stopped || signal || code !== 0) throw Error('MAC_OPENCODE_FAILED');
    await rm(root, { recursive: true, force: true });
    root = undefined;
    marker('cleaned');
  } finally {
    clearTimeout(timeout);
    process.removeListener('SIGTERM', stop);
    control.destroy();
    if (root) await rm(root, { recursive: true, force: true });
  }
}

try { await run(); }
catch (err) {
  console.error(/^MAC_[A-Z_]+$/.test(err?.message ?? '') ? err.message : 'MAC_OPENCODE_FAILED');
  process.exitCode = 1;
}
