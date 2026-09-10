import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const config = await readFile(new URL('../../playwright.config.ts', import.meta.url), 'utf8');

test('public-page browser acceptance serves a fresh production artifact', () => {
  assert.match(config, /@ztapi\/console build/);
  assert.match(config, /vite preview/);
  assert.match(config, /--strictPort/);
  assert.match(config, /reuseExistingServer:\s*false/);
  assert.doesNotMatch(config, /@ztapi\/console dev/);
});

test('public-page browser acceptance uses a deterministic low-resource policy', () => {
  assert.match(config, /workers:\s*1/);
  assert.match(config, /timeout:\s*60_000/);
});
