import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createConnection } from 'node:net';
import { join } from 'node:path';
import test from 'node:test';

const denied = error => ['EPERM', 'EACCES'].includes(error?.code);

test('runs from a staged test directory without original-project or network access', async () => {
  const scratch = await mkdtemp(join(process.cwd(), 'project-qa-'));
  try {
    const file = join(scratch, 'sample.mjs');
    await writeFile(file, 'export const sum = (a, b) => a + b;', { flag: 'wx' });
    const { sum } = await import(file);
    assert.equal(sum(3, 4), 7);
    await assert.rejects(readFile(process.env.ZTAPI_QA_OUTSIDE), denied);
    await assert.rejects(writeFile(process.env.ZTAPI_QA_OUTSIDE, 'x'), denied);
    await assert.rejects(writeFile(process.env.ZTAPI_QA_NEW_OUTSIDE, 'x', { flag: 'wx' }), denied);
    await assert.rejects(new Promise((resolve, reject) => {
      const socket = createConnection({ host: '127.0.0.1', port: Number(process.env.ZTAPI_QA_PORT) });
      socket.once('error', reject);
      socket.once('connect', () => { socket.destroy(); resolve(); });
      socket.setTimeout(1500, () => { socket.destroy(); reject(Error('Network check timed out')); });
    }), denied);
  } finally {
    await rm(scratch, { recursive: true });
  }
});
