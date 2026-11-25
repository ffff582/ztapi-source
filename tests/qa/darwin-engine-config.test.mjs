import assert from 'node:assert/strict';
import test from 'node:test';
import { openCodeSandboxEnvironment } from '../../desktop/macos/engine-config.mjs';

const workspace = '/private/var/folders/qa/T/ztapi-task-123';
test('Mac project engine sees one local provider and a narrow, explicit tool list', () => {
  const env = openCodeSandboxEnvironment({ workspace, mode: 'project' });
  const cfg = JSON.parse(env.OPENCODE_CONFIG_CONTENT);
  assert.equal(env.HOME, workspace);
  assert.deepEqual(cfg.enabled_providers, ['ztapi']);
  assert.equal(cfg.provider.ztapi.options.baseURL, 'http://ztapi-engine.invalid/v1');
  assert.equal(cfg.permission.bash, undefined);
  assert.equal(cfg.permission['*'], 'deny');
  assert.deepEqual(Object.keys(cfg.permission).filter(x => cfg.permission[x] === 'allow').sort(),
    ['ztapi_investigate_in_parallel', 'ztapi_read_file', 'ztapi_run_test', 'ztapi_stage_edit']);
  assert.equal(env.OPENCODE_DISABLE_PROJECT_CONFIG, '1');
  assert.equal(env.OPENCODE_DISABLE_DEFAULT_PLUGINS, '1');
  assert.equal(env.OPENCODE_DISABLE_LSP_DOWNLOAD, '1');
  assert.ok(!Object.keys(env).some(key => /PROXY|API_KEY|^PATH$/.test(key)));
});
test('text mode has no tools and workspace cannot point at the real project', () => {
  assert.equal(JSON.parse(openCodeSandboxEnvironment({ workspace, mode: 'text' }).OPENCODE_CONFIG_CONTENT).mcp, undefined);
  assert.throws(() => openCodeSandboxEnvironment({ workspace: '/Users/runner/project', mode: 'project' }));
  assert.throws(() => openCodeSandboxEnvironment({ workspace: workspace + '/../', mode: 'project' }));
});
