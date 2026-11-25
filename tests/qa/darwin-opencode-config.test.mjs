import assert from 'node:assert/strict';
import test from 'node:test';
import { openCodeSandboxEnvironment } from '../../desktop/macos/engine-config.mjs';

const containerId = 'com.ztapi.workbench.qa.opencode';
const workspace = '/Users/runner/Library/Containers/' + containerId + '/Data/tmp/ztapi-task-test';
const plugin = new URL('file:///private/tmp/Workbench%20QA.app/Contents/Resources/opencode-pipe-plugin.mjs');
test('allows the specified App Sandbox container and one bundled plugin', () => {
  const env = openCodeSandboxEnvironment({ workspace, mode: 'text', containerId, plugin });
  assert.equal(env.HOME, workspace);
  assert.deepEqual(JSON.parse(env.OPENCODE_CONFIG_CONTENT).plugin, [plugin.href]);
});
test('rejects another container or a plugin outside the application resources', () => {
  assert.throws(() => openCodeSandboxEnvironment({ workspace, mode: 'text', containerId: 'com.example.other', plugin }));
  assert.throws(() => openCodeSandboxEnvironment({ workspace, mode: 'text', containerId,
    plugin: new URL('file:///Users/runner/project/opencode-pipe-plugin.mjs') }));
});
