import assert from 'node:assert/strict';

export function validateSimulatedBridgeConfig(config) {
  for (const field of ['base_url', 'api_key', 'model']) assert.ok(typeof config[field] === 'string' && config[field].trim(), `missing ${field}`);
  assert.equal(config.provider_kind, 'simulated', 'this runner does not authorize live requests');
  assert.equal(config.model, 'zt-claude-sonnet-5', 'unexpected fixture model');
  assert.ok(Number.isSafeInteger(config.pid) && config.pid > 0, 'owned bridge PID required');
  assert.match(config.run_nonce ?? '', /^[a-f0-9]{32}$/, 'owned bridge nonce required');
  const url = new URL(config.base_url);
  assert.equal(url.protocol, 'http:');
  assert.ok(['127.0.0.1', '[::1]'].includes(url.hostname), 'loopback IP required');
  assert.ok(Number(url.port) > 0, 'an explicit listener port is required');
  assert.ok(!url.username && !url.password && !url.search && !url.hash, 'endpoint decoration is not permitted');
  assert.equal(url.pathname, '/', 'bridge root required');
}

const errorTypes = new Set(['Error', 'APIError', 'AuthenticationError', 'PermissionDeniedError', 'NotFoundError', 'RateLimitError', 'APIConnectionError', 'APIConnectionTimeoutError', 'InternalServerError', 'BadRequestError', 'UnprocessableEntityError', 'APIUserAbortError']);
const errorCodes = new Set(['invalid_api_key', 'invalid_request_error', 'rate_limit_exceeded', 'insufficient_quota', 'upstream_http_error', 'upstream_auth', 'model_not_found', 'model_temporarily_unavailable']);

export function safeClientError(error) {
  return {
    type: errorTypes.has(error?.name) ? error.name : 'UnknownError',
    status: Number.isInteger(error?.status) && error.status >= 100 && error.status <= 599 ? error.status : null,
    code: errorCodes.has(error?.code) ? error.code : null,
  };
}

export async function verifySimulatedBridge(config) {
  validateSimulatedBridgeConfig(config);
  try {
    const response = await fetch(new URL('/__ztapi_client_bridge', config.base_url), {
      method: 'GET', headers: {Authorization: 'Bearer '+config.api_key},
      redirect: 'error', signal: AbortSignal.timeout(2000),
    });
    assert.equal(response.status, 200);
    assert.ok(response.headers.get('content-type')?.includes('application/json'));
    const reader = response.body.getReader();
    const chunks = [];
    let bytes = 0;
    try {
      while (true) {
        const {done, value} = await reader.read();
        if (done) break;
        bytes += value.byteLength;
        assert.ok(bytes <= 4096);
        chunks.push(value);
      }
    } finally {
      await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
    const identity = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    assert.equal(identity.provider_kind, 'simulated');
    assert.equal(identity.pid, config.pid);
    assert.equal(identity.run_nonce, config.run_nonce);
  } catch {
    throw new Error('simulated_bridge_identity_unverified');
  }
}
