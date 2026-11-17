import {readFileSync} from 'node:fs';
import test from 'node:test';
import assert from 'node:assert/strict';

const read = name => readFileSync(new URL('../../' + name, import.meta.url), 'utf8');

test('release runtime starts from one fixed clean base, never a previous application image', () => {
  const source = read('deploy/docker/Dockerfile.runtime');
  assert.match(source, /^FROM alpine@sha256:[a-f0-9]{64}\r?\n/);
  assert.equal((source.match(/^FROM /gm) ?? []).length, 1);
  assert.match(source, /USER ztapi/);
  assert.match(source, /COPY ztapi-linux-amd64 \/usr\/local\/bin\/ztapi/);
});

test('all compose services have bounded logs without changing another project', () => {
  const source = read('deploy/docker/docker-compose.prod.yml');
  assert.match(source, /max-size: "50m"/);
  assert.match(source, /max-file: "3"/);
  for (const name of ['nginx', 'server', 'mysql', 'redis']) {
    assert.match(source, new RegExp('  ' + name + ':\\r?\\n    logging: \\*ztapi-logging'));
  }
  assert.doesNotMatch(source, /deploy-api-1/);
});

test('release finalization uses the protected cleanup policy instead of deleting rollback tags', () => {
  const source = read('deploy/scripts/ztapi-release-control.sh');
  assert.match(source, /ztapi_cleanup\.py/);
  assert.doesNotMatch(source, /docker image rm "\$rollback_/);
});
