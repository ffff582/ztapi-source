import { posix } from 'node:path';

const model = 'ztapi-task-model';
const transportOrigin = 'http://ztapi-engine.invalid';
const tools = ['ztapi_read_file', 'ztapi_stage_edit', 'ztapi_run_test', 'ztapi_investigate_in_parallel'];

export function openCodeSandboxEnvironment({ workspace, mode, containerId, plugin }) {
  const standardTemp = /^\/private\/(?:tmp|var\/folders)\/(?:[A-Za-z0-9 ._-]+\/)*ztapi-task-[A-Za-z0-9-]+$/;
  const container = /^\/Users\/[^/]+\/Library\/Containers\/([^/]+)\/Data\/tmp\/ztapi-task-[A-Za-z0-9-]+$/;
  const safeWorkspace = typeof workspace === 'string' && (standardTemp.test(workspace) ||
    (typeof containerId === 'string' && /^com\.ztapi\.workbench\.[a-z0-9.]+$/.test(containerId) &&
      container.exec(workspace)?.[1] === containerId));
  if (!safeWorkspace || /[\x00-\x1f]/.test(workspace) ||
      posix.normalize(workspace) !== workspace || !['text', 'project'].includes(mode))
    throw Error('MAC_ENGINE_SANDBOX_NOT_READY');
  if (plugin !== undefined && (!(plugin instanceof URL) || plugin.protocol !== 'file:' || plugin.hostname ||
      plugin.search || plugin.hash || !plugin.pathname.endsWith('.app/Contents/Resources/opencode-pipe-plugin.mjs')))
    throw Error('MAC_ENGINE_PLUGIN_INVALID');
  const project = mode === 'project';
  const permission = {
    '*': 'deny',
    ...Object.fromEntries(project ? tools.map(name => [name, 'allow']) : []),
  };
  const config = {
    enabled_providers: ['ztapi'],
    provider: {
      ztapi: {
        npm: '@ai-sdk/anthropic',
        name: 'ZTAPI',
        options: { baseURL: transportOrigin + '/v1', apiKey: 'local-pipe-only' },
        models: { [model]: { name: 'ZTAPI task model', limit: { context: 32768, output: 2048 } } },
      },
    },
    model: 'ztapi/' + model,
    small_model: 'ztapi/' + model,
    permission,
    ...(plugin ? { plugin: [plugin.href] } : {}),
    ...(project ? { mcp: { ztapi: { type: 'remote', url: transportOrigin + '/__ztapi_mcp', oauth: false, timeout: 30000 } } } : {}),
    agent: { title: { disable: true } },
    share: 'disabled',
    snapshot: false,
    autoupdate: false,
  };
  return Object.freeze({
    HOME: workspace,
    TMPDIR: workspace,
    XDG_CONFIG_HOME: posix.join(workspace, 'config'),
    XDG_DATA_HOME: posix.join(workspace, 'data'),
    XDG_STATE_HOME: posix.join(workspace, 'state'),
    XDG_CACHE_HOME: posix.join(workspace, 'cache'),
    OPENCODE_DISABLE_AUTOUPDATE: '1',
    OPENCODE_DISABLE_MODELS_FETCH: '1',
    OPENCODE_DISABLE_DEFAULT_PLUGINS: '1',
    OPENCODE_DISABLE_PROJECT_CONFIG: '1',
    OPENCODE_DISABLE_LSP_DOWNLOAD: '1',
    OPENCODE_CONFIG_CONTENT: JSON.stringify(config),
  });
}
