import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdtempSync, realpathSync, rmdirSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';

assert.equal(process.platform, 'darwin');
assert.equal(process.env.GITHUB_ACTIONS, 'true');
assert.equal(process.env.RUNNER_ENVIRONMENT, 'github-hosted');
const [program, ...args] = process.argv.slice(2);
assert.ok(program && isAbsolute(process.env.RUNNER_TEMP));

function security(...args) {
  const result = spawnSync('/usr/bin/security', args, { encoding: 'utf8', timeout: 8_000 });
  if (result.error || result.status !== 0) {
    throw Error(`security ${args[0]} failed (${result.error?.code ?? result.status})`);
  }
  return result.stdout.trim();
}

function keychainPaths(command) {
  const output = security(command, '-d', 'user');
  if (!output) return [];
  return output.split('\n').map(line => {
    const path = JSON.parse(line.trim());
    assert.ok(typeof path === 'string' && isAbsolute(path));
    return path;
  });
}

const originalList = keychainPaths('list-keychains');
const originalDefault = keychainPaths('default-keychain');
assert.ok(originalList.length > 0 && originalDefault.length === 1);
const root = realpathSync(mkdtempSync(join(process.env.RUNNER_TEMP, 'ztapi-keychain-')));
const temporary = join(root, 'acceptance.keychain-db');
const password = randomBytes(24).toString('hex');
let created = false;
try {
  security('create-keychain', '-p', password, temporary);
  created = true;
  security('unlock-keychain', '-p', password, temporary);
  security('list-keychains', '-d', 'user', '-s', temporary);
  security('default-keychain', '-d', 'user', '-s', temporary);
  assert.deepEqual(keychainPaths('list-keychains'), [temporary]);
  assert.deepEqual(keychainPaths('default-keychain'), [temporary]);
  console.log('QA_DISPOSABLE_KEYCHAIN_READY');
  const result = spawnSync(process.execPath, [program, ...args], {
    stdio: 'inherit', timeout: 80_000, killSignal: 'SIGKILL',
  });
  if (result.error || result.status !== 0) {
    throw Error(`QA child failed (${result.error?.code ?? result.status ?? result.signal})`);
  }
} finally {
  if (created) {
    security('list-keychains', '-d', 'user', '-s', ...originalList);
    security('default-keychain', '-d', 'user', '-s', originalDefault[0]);
    security('delete-keychain', temporary);
    assert.deepEqual(keychainPaths('list-keychains'), originalList);
    assert.deepEqual(keychainPaths('default-keychain'), originalDefault);
    console.log('QA_DISPOSABLE_KEYCHAIN_RESTORED');
  }
  rmdirSync(root);
}
