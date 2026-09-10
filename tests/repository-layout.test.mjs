import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

test('repository layout keeps the imported server isolated from ZTAPI-owned boundaries', () => {
  const requiredPaths = [
    'server/go.mod',
    'server/router/api-router.go',
    'web/console/package.json',
    'deploy/docker/docker-compose.dev.yml',
    'UPSTREAM.md'
  ];

  for (const relativePath of requiredPaths) {
    assert.equal(existsSync(resolve(repositoryRoot, relativePath)), true, relativePath);
  }

  const upstream = readFileSync(resolve(repositoryRoot, 'UPSTREAM.md'), 'utf8');
  assert.match(upstream, /https:\/\/github\.com\/QuantumNous\/new-api/);
});

test('console scaffold keeps clean installs and the default language explicit', () => {
  const workspace = readFileSync(resolve(repositoryRoot, 'pnpm-workspace.yaml'), 'utf8');
  const consoleHtml = readFileSync(resolve(repositoryRoot, 'web/console/index.html'), 'utf8');
  const rootPackage = JSON.parse(
    readFileSync(resolve(repositoryRoot, 'package.json'), 'utf8'),
  );
  const consolePackage = JSON.parse(
    readFileSync(resolve(repositoryRoot, 'web/console/package.json'), 'utf8'),
  );

  assert.match(workspace, /onlyBuiltDependencies:\s*\r?\n\s*-\s+esbuild/);
  assert.doesNotMatch(workspace, /set this to true or false/);
  assert.equal(rootPackage.name, 'ztapi');
  assert.equal(rootPackage.scripts['test:web'], 'pnpm --filter @ztapi/console test');
  assert.equal(rootPackage.scripts['lint:web'], 'pnpm --filter @ztapi/console lint');
  assert.equal(consolePackage.name, '@ztapi/console');
  assert.match(consoleHtml, /<html lang="zh-CN">/);
  assert.match(
    consoleHtml,
    /<link rel="icon" type="image\/png" href="\/brand\/ztapi-favicon\.png"\s*\/>/,
  );
  assert.match(consoleHtml, /<title>ZTAPI<\/title>/);
});
