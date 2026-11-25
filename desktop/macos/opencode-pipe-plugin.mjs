import { fstatSync } from 'node:fs';
import { connect } from 'node:net';
import { createEnginePipeClient } from './engine-pipe.mjs';
import { createMacSandboxFetch } from './engine-transport.mjs';

export default async function pipePlugin() {
  const trace = process.env.ZTAPI_ENGINE_QA_TRACE === '1';
  if (trace) console.error('ZTAPI QA plugin initialized');
  if (process.platform !== 'darwin' || !process.versions.bun ||
      !fstatSync(3).isSocket() || !fstatSync(4).isSocket()) throw Error('MAC_ENGINE_SANDBOX_NOT_READY');
  const input = connect({ fd: 3 });
  const output = connect({ fd: 4 });
  const fetch = createMacSandboxFetch({ request: createEnginePipeClient(input, output) });
  input.unref();
  output.unref();
  return {
    async config(conf) {
      const provider = conf.provider?.ztapi;
      if (conf.enabled_providers?.length !== 1 || conf.enabled_providers[0] !== 'ztapi' ||
          provider?.options?.baseURL !== 'http://ztapi-engine.invalid/v1') throw Error('MAC_ENGINE_CONFIG_INVALID');
      provider.options.fetch = fetch;
      // OpenCode's in-process SDK supplies its own fetch; MCP uses the scoped pipe fetch.
      globalThis.fetch = fetch;
      if (trace) console.error('ZTAPI QA plugin configured');
    },
    async dispose() {
      input.destroy();
      output.destroy();
    },
  };
}
