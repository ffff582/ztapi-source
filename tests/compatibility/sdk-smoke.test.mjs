import assert from 'node:assert/strict';
import test from 'node:test';
import {createGuardedSDKFetch, safeSDKError, safeSDKRequestID, sdkUsageEvidence, validateSDKSmokeConfig} from './sdk-smoke.mjs';

const config = {
  base_url: 'http://127.0.0.1:12345',
  api_key: 'local-synthetic-sdk-secret',
  model: 'zt-claude-sonnet-5',
  provider_kind: 'simulated',
  pid: process.pid,
  run_nonce: '1234567890abcdef1234567890abcdef',
};

test('SDK smoke accepts only an explicit simulated loopback bridge', () => {
  assert.doesNotThrow(() => validateSDKSmokeConfig(config));
  assert.doesNotThrow(() => validateSDKSmokeConfig({...config, base_url: 'http://[::1]:12345'}));
});

test('SDK smoke rejects endpoint escape and accidental live execution', () => {
  for (const base_url of ['https://ztapi.vip', 'http://localhost:12345', 'http://user:password@127.0.0.1:12345', 'http://127.0.0.1:12345/v1', 'http://127.0.0.1:12345?api_key=secret', 'http://127.0.0.1:12345#secret']) {
    assert.throws(() => validateSDKSmokeConfig({...config, base_url}));
  }
  for (const field of ['base_url', 'api_key', 'model']) {
    assert.throws(() => validateSDKSmokeConfig({...config, [field]: ''}));
  }
  assert.throws(() => validateSDKSmokeConfig({...config, provider_kind: 'live'}));
  assert.throws(() => validateSDKSmokeConfig({...config, model: 'another-model'}));
});

test('SDK error evidence excludes raw bodies, messages, headers and secrets', () => {
  const error = {name: 'APIError', status: 401, code: 'invalid_api_key', message: config.api_key, headers: {authorization: 'Bearer '+config.api_key}, body: 'private prompt'};
  assert.deepEqual(safeSDKError(error), {type: 'APIError', status: 401, code: 'invalid_api_key'});
  assert.deepEqual(safeSDKError(new Error('Bearer '+config.api_key)), {type: 'Error', status: null, code: null});
  const serialized = JSON.stringify(safeSDKError({...error, code: 'bad\n'+config.api_key, name: config.api_key}));
  assert.ok(!serialized.includes(config.api_key));
});

test('SDK transport pins every inference request and disables redirects', async () => {
  let calls = 0;
  const guarded = createGuardedSDKFetch(config, async (_input, init) => { calls++; assert.equal(init.redirect, 'error'); return new Response('{}'); });
  await guarded(config.base_url+'/v1/responses', {method: 'POST', redirect: 'follow'});
  assert.equal(calls, 1);
  for (const target of ['https://ztapi.vip/v1/responses', config.base_url+'/v1/responses?key=secret', config.base_url+'/unknown']) {
    await assert.rejects(() => guarded(target, {method: 'POST'}));
  }
  assert.equal(calls, 1, 'blocked requests must not reach the transport');
});

test('SDK evidence does not expose arbitrary upstream IDs', () => {
  assert.equal(safeSDKRequestID('bridge-request-1', config.api_key), 'bridge-request-1');
  for (const id of [config.api_key, 'sk-secret-upstream-key', 'Bearer secret', 'unexpected customer content']) assert.equal(safeSDKRequestID(id, config.api_key), null);
  assert.equal(safeSDKRequestID('bridge-request-1', 'bridge-request-1'), null);
});

test('SDK evidence never treats missing usage as zero or a passing case', () => {
  for (const usage of [undefined, {}, {input_tokens: 20}, {input_tokens: 20, output_tokens: -1}, {prompt_tokens: '20', completion_tokens: 2}]) assert.throws(() => sdkUsageEvidence(usage));
  assert.deepEqual(sdkUsageEvidence({input_tokens: 0, output_tokens: 0}), {input_tokens: 0, output_tokens: 0, total_tokens: null, cache_read_tokens: null, cache_write_tokens: null});
});
