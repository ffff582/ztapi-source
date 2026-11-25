const localOrigin = 'http://ztapi-engine.invalid';
const routes = new Map([
  ['/v1/messages', 'model'],
  ['/v1/messages?beta=true', 'model'],
  ['/__ztapi_mcp', 'projectTool'],
]);
const statuses = new Set([200, 202, 400, 401, 402, 403, 404, 409, 429]);
const types = new Set(['application/json', 'text/event-stream']);
const rejected = () => Error('ENGINE_TRANSPORT_REJECTED');

async function readBody(body, limit) {
  if (!body) throw rejected();
  const reader = body.getReader();
  const parts = [];
  let size = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if ((size += value.length) > limit) throw rejected();
      parts.push(Buffer.from(value));
    }
    const text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(parts));
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw rejected();
    return text;
  } catch {
    await reader.cancel().catch(() => {});
    throw rejected();
  } finally { reader.releaseLock(); }
}

export function createMacSandboxFetch({ request }) {
  if (typeof request !== 'function') throw rejected();
  return async (input, init) => {
    const req = new Request(input, init);
    const url = new URL(req.url);
    const channel = url.origin === localOrigin && !url.username && !url.password && !url.hash
      ? routes.get(url.pathname + url.search) : null;
    if (!channel || req.method !== 'POST' || req.headers.get('content-type')?.split(';')[0].trim() !== 'application/json')
      throw rejected();
    req.signal.throwIfAborted();
    const body = await readBody(req.body, channel === 'model' ? 131072 : 65536);
    req.signal.throwIfAborted();
    const result = await request(Object.freeze({ channel, body }), req.signal);
    if (!result || !statuses.has(result.status) ||
        (result.status === 202 ? result.body != null && result.body !== '' :
          !types.has(result.type) || typeof result.body !== 'string' || Buffer.byteLength(result.body) > 1048576))
      throw Error('ENGINE_TRANSPORT_UNCONFIRMED');
    return new Response(result.status === 202 ? null : result.body, {
      status: result.status, headers: result.status === 202 ? {} : { 'content-type': result.type },
    });
  };
}
