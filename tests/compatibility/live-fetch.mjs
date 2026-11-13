import assert from 'node:assert/strict';
import {safeSDKRequestID} from './sdk-smoke.mjs';

// This adapter does not release budget. Settlement and supplier evidence do.
export function createBoundedLiveFetch(config, dependencies) {
  assert.equal(config.baseURL, 'http://127.0.0.1:18081', 'private acceptance listener required');
  assert.match(config.model, /^zt-[a-z0-9][a-z0-9.-]{1,100}$/);
  assert.ok(typeof config.attemptPrefix === 'string' && /^[a-z0-9-]{1,80}$/.test(config.attemptPrefix), 'unique stage prefix required');
  assert.ok(typeof config.apiKey === 'string' && config.apiKey.length >= 8);
  assert.ok(Number.isSafeInteger(config.maxRequests) && config.maxRequests >= 1 && config.maxRequests <= 6);
  assert.ok(typeof config.capU === 'string' && /^(?:0|1)(?:\.[0-9]{1,8})?$/.test(config.capU)
    && Number(config.capU) > 0 && Number(config.capU) <= 1);
  for (const name of ['reserve', 'receipt', 'fetch']) assert.equal(typeof dependencies[name], 'function');
  let count = 0;
  let busy = false;
  return async (input, init = {}) => {
    const request = input instanceof Request ? input : null;
    const url = new URL(request ? request.url : String(input));
    assert.equal(url.origin, config.baseURL, 'inference destination changed');
    assert.ok(!url.username && !url.password && !url.hash);
    assert.ok(['/v1/chat/completions', '/v1/responses', '/v1/messages'].includes(url.pathname));
    assert.ok(!url.search || url.pathname === '/v1/messages' && url.search === '?beta=true');
    assert.equal(String(init.method ?? request?.method ?? 'GET').toUpperCase(), 'POST');
    const raw = init.body ?? (request ? await request.clone().text() : null);
    assert.ok(typeof raw === 'string' && Buffer.byteLength(raw) <= 32768, 'bounded JSON body required');
    const body = JSON.parse(raw);
    assert.equal(body.model, config.model);
    const effectiveFields = url.pathname === '/v1/responses' ? ['max_output_tokens']
      : url.pathname === '/v1/messages' ? ['max_tokens'] : ['max_tokens', 'max_completion_tokens'];
    assert.ok(effectiveFields.some(key => body[key] !== undefined), 'endpoint-specific token bound required');
    assert.ok(body.n === undefined || body.n === 1, 'one completion per paid attempt required');
    const limits = ['max_tokens', 'max_completion_tokens', 'max_output_tokens'].filter(key => body[key] !== undefined);
    assert.ok(limits.length > 0 && limits.every(key => Number.isSafeInteger(body[key]) && body[key] >= 1 && body[key] <= 1024),
      'explicit output token bounds required');
    assert.ok(!busy && count < config.maxRequests, 'live request limit reached');
    busy = true;
    const id = `${config.attemptPrefix}:${config.model}:${++count}`;
    try {
      // reserve must persist its event before resolving; a failure never dispatches.
      await dependencies.reserve({id, model: config.model, capU: config.capU});
      const started = Date.now();
      let response;
      try {
        const signal = init.signal ?? request?.signal;
        response = await dependencies.fetch(input, {...init, redirect: 'error',
          signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(300000)]) : AbortSignal.timeout(300000)});
      } catch {
        await dependencies.receipt({id, model: config.model, status: 'unknown', elapsed_ms: Date.now()-started});
        throw new Error('live_transport_failed');
      }
      await dependencies.receipt({id, model: config.model, status: 'headers_received', http: response.status,
        elapsed_ms: Date.now()-started, request_id: safeSDKRequestID(response.headers.get('x-request-id'), config.apiKey)});
      return response;
    } finally { busy = false; }
  };
}
