import assert from 'node:assert/strict';
import { access, mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { assemblePublishedClient, replaceOnce } from '../../desktop/macos/assemble-published-client.mjs';

test('overlay changes one exact anchor and refuses source drift or overlap', () => {
  assert.equal(replaceOnce('a-b', '-', '+'), 'a+b');
  assert.throws(() => replaceOnce('abc', '-', '+'), /MAC_CLIENT_SOURCE_DRIFT/);
  assert.throws(() => replaceOnce('a--b', '-', '+'), /MAC_CLIENT_SOURCE_DRIFT/);
});

test('unverified payloads do not create a candidate directory', async () => {
  const root = await mkdtemp(join(tmpdir(), 'ztapi-assembly-'));
  try {
    const payload = join(root, 'payload'), out = join(root, 'candidate');
    await mkdir(payload);
    await writeFile(join(payload, 'MANIFEST.json'), '{}');
    await assert.rejects(assemblePublishedClient(payload, out), /MAC_CLIENT_BASELINE_REJECTED/);
    await assert.rejects(access(out), { code: 'ENOENT' });
  } finally { await rm(root, { recursive: true, force: true }); }
});
