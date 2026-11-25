import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { lstat, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { isAbsolute, join } from 'node:path';
import { serveEnginePipe } from './engine-pipe.mjs';

const setup = () => Error('MAC_ENGINE_SANDBOX_NOT_READY');
const malformed = () => Error('MAC_ENGINE_OUTPUT_INVALID');
const cancelled = () => Error('PROJECT_TASK_CANCELLED');
const outputCap = 2 * 1024 * 1024;
const containerIds = ['com.ztapi.workbench.engine', 'com.ztapi.workbench.qa.opencode'];

export function engineOutput(raw, probe = false) {
  let rows;
  try {
    rows = raw.trim().split(/\r?\n/).map(JSON.parse);
  } catch { throw malformed(); }
  const mark = (row, phase) => row?.type === 'ztapi-sandbox' && row.phase === phase;
  if (!mark(rows[0], 'verified') || !mark(rows.at(-1), 'cleaned')) throw malformed();
  const events = rows.slice(1, -1);
  if (events.some(row => !row || typeof row.type !== 'string' || ['error', 'ztapi-sandbox'].includes(row.type)))
    throw malformed();
  if (probe) {
    if (events.length !== 0) throw malformed();
    return '';
  }
  const output = events.filter(row => row.type === 'text').map(row => {
    if (typeof row.part?.text !== 'string') throw malformed();
    return row.part.text;
  }).join('').trim();
  if (!output || Buffer.byteLength(output) > 65536) throw malformed();
  return output;
}

export async function invokeMacEngine({ host, state, bundleId, prompt, mode, signal, dispatch,
  timeoutMs = 15 * 60_000 } = {}) {
  if (signal?.aborted) throw cancelled();
  const probe = mode === 'probe';
  if (process.platform !== 'darwin' || !containerIds.includes(bundleId) || !['probe', 'text', 'project'].includes(mode) ||
      ![host, state].every(value => typeof value === 'string' && isAbsolute(value)) ||
      (!probe && (typeof dispatch !== 'function' || typeof prompt !== 'string' ||
        !prompt.trim() || Buffer.byteLength(prompt) > 96000)) ||
      !Number.isSafeInteger(timeoutMs) || timeoutMs < 1000 || timeoutMs > 20 * 60_000) throw setup();
  const bin = await lstat(host);
  if (!bin.isFile() || bin.isSymbolicLink() || await realpath(host) !== host ||
      !(await lstat(state)).isDirectory() || await realpath(state) !== state) throw setup();

  const life = new AbortController();
  const directory = await mkdtemp(join(state, 'engine-canary-'));
  let hit = false, child, exited, abortTimer, timeout, stopped = false;
  const listener = createServer(socket => { hit = true; socket.destroy(); });
  const stop = () => {
    if (stopped) return;
    stopped = true;
    life.abort();
    child?.stdio[5]?.destroy();
    if (child) {
      abortTimer = setTimeout(() => {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
      }, 5000);
      abortTimer.unref();
    }
  };
  try {
    const file = join(directory, 'probe');
    await writeFile(file, 'sandbox canary', { flag: 'wx', mode: 0o600 });
    listener.listen(0, '127.0.0.1');
    await once(listener, 'listening');
    if (signal?.aborted) throw cancelled();
    child = spawn(host, [], { env: {
      ZTAPI_ENGINE_BUNDLE_ID: bundleId, ZTAPI_ENGINE_MODE: mode, ZTAPI_ENGINE_CANARY: file,
      ZTAPI_ENGINE_TEST_PORT: String(listener.address().port), ZTAPI_ENGINE_DEADLINE_MS: String(timeoutMs),
      ...(bundleId === 'com.ztapi.workbench.qa.opencode' ? { ZTAPI_ENGINE_QA_TRACE: '1' } : {}),
    }, stdio: ['pipe', 'pipe', bundleId.endsWith('.qa.opencode') ? 'inherit' : 'ignore', 'pipe', 'pipe', 'pipe'] });
    child.on('error', stop);
    exited = new Promise(resolve => child.once('close', (code, term) => {
      if (code !== 0 || term) life.abort();
      resolve({ code, term });
    }));
    for (const stream of [child.stdin, child.stdio[3], child.stdio[4], child.stdio[5]]) stream.on('error', stop);
    signal?.addEventListener('abort', stop, { once: true });
    if (signal?.aborted) stop();
    timeout = setTimeout(stop, timeoutMs + 1000);
    const parts = [];
    let bytes = 0;
    child.stdout.on('data', part => {
      if ((bytes += part.length) > outputCap) stop();
      else parts.push(part);
    });
    child.stdin.end(probe ? '' : prompt);
    const relay = serveEnginePipe(child.stdio[4], child.stdio[3], (frame) => {
      if (probe || hit || stopped) throw setup();
      return dispatch(frame, life.signal);
    }).catch(error => { stop(); throw error; });
    const [bridge, processResult] = await Promise.allSettled([relay, exited]);
    if (signal?.aborted) throw cancelled();
    if (bridge.status === 'rejected') throw bridge.reason;
    const { code, term } = processResult.value;
    if (hit || stopped || code !== 0 || term) throw Error('MAC_ENGINE_RUN_FAILED');
    return { summary: engineOutput(Buffer.concat(parts).toString('utf8'), probe), frames: bridge.value };
  } finally {
    signal?.removeEventListener('abort', stop);
    clearTimeout(timeout);
    if (child?.exitCode === null && child.signalCode === null) { stop(); await exited; }
    clearTimeout(abortTimer);
    try {
      if (listener.listening) {
        const closed = once(listener, 'close');
        listener.close();
        await closed;
      }
    } finally { await rm(directory, { recursive: true, force: true }); }
  }
}
