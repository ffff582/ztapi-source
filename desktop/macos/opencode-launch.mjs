import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, lstat, readFile, realpath, rm } from 'node:fs/promises';
import { connect } from 'node:net';
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
  if (!await readFile(canary).then(() => false, canaryError)) throw setupError();
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
  const binary = await prepare();
  const root = await mkdtemp(join(await realpath(process.env.ZTAPI_TEST_TMPDIR), 'ztapi-task-'));
  try {
    const env = openCodeSandboxEnvironment({
      workspace: root, mode: process.env.ZTAPI_ENGINE_MODE,
      containerId: 'com.ztapi.workbench.qa.opencode',
      plugin: new URL('./opencode-pipe-plugin.mjs', import.meta.url),
    });
    const trace = process.env.ZTAPI_ENGINE_QA_TRACE === '1';
    const flags = trace ? ['--print-logs', '--log-level', 'DEBUG'] : [];
    const child = spawn(binary, [...flags, 'run', '--format', 'json', '--model', 'ztapi/ztapi-task-model'], {
      cwd: root, env: { ...env, ...(trace ? { ZTAPI_ENGINE_QA_TRACE: '1' } : {}) }, stdio: ['inherit', 'inherit', 'inherit', 3, 4],
    });
    const timeout = setTimeout(() => child.kill('SIGKILL'), 30_000);
    const forward = () => child.kill('SIGTERM');
    process.once('SIGTERM', forward);
    try {
      const [code, signal] = await once(child, 'close');
      if (trace) console.error(`OpenCode QA subprocess stopped: ${signal ?? code}`);
      if (signal || code !== 0) throw Error('MAC_OPENCODE_FAILED');
    } finally {
      clearTimeout(timeout);
      process.removeListener('SIGTERM', forward);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

try { await run(); }
catch (err) {
  console.error(/^MAC_[A-Z_]+$/.test(err?.message ?? '') ? err.message : 'MAC_OPENCODE_FAILED');
  process.exitCode = 1;
}
