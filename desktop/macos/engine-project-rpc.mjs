import { projectToolNames } from './engine-config.mjs';

const versions = new Set(['2025-11-25', '2025-06-18', '2025-03-26']);
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const validId = value => Number.isSafeInteger(value) && value >= 0 ||
  typeof value === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(value);
const reject = () => Error('MAC_ENGINE_PROJECT_RPC_REJECTED');

export function createProjectRpc({ tools, execute, limit }) {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 16 || typeof execute !== 'function' ||
      !Array.isArray(tools) || tools.length < 1 || tools.length > projectToolNames.length ||
      tools.some(tool => !projectToolNames.includes(tool?.name) || tool.inputSchema?.type !== 'object') ||
      new Set(tools.map(tool => tool.name)).size !== tools.length) throw reject();
  const catalog = structuredClone(tools);
  const allowed = new Set(catalog.map(tool => tool.name));
  const ids = new Set();
  const operations = new Set();
  let initialized = false, ready = false, active = false;
  return async raw => {
    if (typeof raw !== 'string' || Buffer.byteLength(raw) > 65536 || active) throw reject();
    let body;
    try { body = JSON.parse(raw); } catch { throw reject(); }
    if (!object(body) || body.jsonrpc !== '2.0' ||
        Object.keys(body).some(key => !['jsonrpc', 'method', 'id', 'params'].includes(key))) throw reject();
    const { id, method, params } = body;
    if (id === undefined) {
      if (method === 'notifications/initialized' && initialized && !ready) ready = true;
      else if (!(method === 'notifications/cancelled' && ready && operations.has(params?.requestId))) throw reject();
      return { status: 202 };
    }
    if (!validId(id) || ids.has(id) || ids.size >= 32) throw reject();
    ids.add(id);
    let result;
    if (!initialized && method === 'initialize' && versions.has(params?.protocolVersion)) {
      initialized = true;
      result = { protocolVersion: params.protocolVersion, capabilities: { tools: {} },
        serverInfo: { name: 'ztapi-local-project', version: '1.0.0' } };
    } else if (ready && method === 'tools/list' && params?.cursor === undefined) {
      result = { tools: catalog };
    } else if (ready && method === 'tools/call' && allowed.has(params?.name) &&
        object(params.arguments) && operations.size < limit) {
      operations.add(id);
      active = true;
      try {
        const text = JSON.stringify(await execute(params.name, params.arguments));
        if (typeof text !== 'string' || Buffer.byteLength(text) > 65536) throw reject();
        result = { content: [{ type: 'text', text }] };
      } catch { throw Error('MAC_ENGINE_PROJECT_TOOL_FAILED'); }
      finally { active = false; }
    } else throw reject();
    return { status: 200, type: 'application/json', body: JSON.stringify({ jsonrpc: '2.0', id, result }) };
  };
}
