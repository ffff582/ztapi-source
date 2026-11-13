import assert from 'node:assert/strict';
import {test} from 'node:test';

let module = {};
try { module = await import('./live-fetch.mjs'); }
catch (error) { if (error.code !== 'ERR_MODULE_NOT_FOUND') throw error; }
const config = {baseURL: 'http://127.0.0.1:18081', model: 'zt-claude-sonnet-5',
  apiKey: 'synthetic-test-key', maxRequests: 4, capU: '0.2', attemptPrefix: 'baseline-1'};
const body = {model: config.model, messages: [{role: 'user', content: 'OK'}], max_tokens: 64};
function make(overrides = {}) {
  assert.equal(typeof module.createBoundedLiveFetch, 'function');
  const events = [];
  const guarded = module.createBoundedLiveFetch({...config, ...overrides}, {
    reserve: async value => { events.push(['reserve', value]); },
    receipt: async value => { events.push(['receipt', value]); },
    fetch: async (url, init) => {
      events.push(['fetch', {url: String(url), redirect: init.redirect}]);
      return new Response('{}', {headers: {'x-request-id': '0123456789abcdef0123456789abcdef'}});
    },
  });
  return {guarded, events};
}

test('paid inference reserves durably before fetch and records only safe headers', async () => {
  const {guarded, events} = make();
  const result = await guarded(config.baseURL+'/v1/chat/completions', {method: 'POST', body: JSON.stringify(body)});
  assert.equal(result.status, 200);
  assert.deepEqual(events.map(e => e[0]), ['reserve', 'fetch', 'receipt']);
  assert.equal(events[1][1].redirect, 'error');
  assert.equal(events[0][1].capU, '0.2');
  assert.equal(events[0][1].id, 'baseline-1:zt-claude-sonnet-5:1');
  assert.equal(events[2][1].request_id, '0123456789abcdef0123456789abcdef');
  assert.ok(!JSON.stringify(events).includes(config.apiKey));
});

test('distinct qualification stages cannot silently share an attempt identity', async () => {
  assert.throws(() => make({attemptPrefix: undefined}));
  const a = make({attemptPrefix: 'baseline'}), b = make({attemptPrefix: 'candidate'});
  for (const entry of [a, b]) await entry.guarded(config.baseURL+'/v1/chat/completions', {method:'POST', body:JSON.stringify(body)});
  assert.notEqual(a.events[0][1].id, b.events[0][1].id);
});

test('only the private listener, inference paths and explicit bounded output are accepted', async () => {
  for (const baseURL of ['https://ztapi.vip', 'http://localhost:18081', 'http://127.0.0.1:18081/path',
    'http://127.0.0.1:18081?x=1', 'http://key@127.0.0.1:18081']) assert.throws(() => make({baseURL}));
  const bad = [
    ['http://127.0.0.1:18082/v1/chat/completions', body, 'POST'],
    [config.baseURL+'/api/token/', body, 'POST'],
    [config.baseURL+'/v1/responses/compact', body, 'POST'],
    [config.baseURL+'/v1/chat/completions?extra=true', body, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, model: 'other'}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, max_tokens: 1025}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, max_tokens: undefined}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, max_tokens: 0}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, max_tokens: 64, max_completion_tokens: 1025}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, max_tokens: undefined, max_output_tokens: 1}, 'POST'],
    [config.baseURL+'/v1/responses', {...body, max_tokens: 1}, 'POST'],
    [config.baseURL+'/v1/messages', {...body, max_tokens: undefined, max_output_tokens: 1}, 'POST'],
    [config.baseURL+'/v1/chat/completions', {...body, n: 2}, 'POST'],
    [config.baseURL+'/v1/chat/completions', body, 'GET'],
  ];
  for (const [url, payload, method] of bad) {
    const {guarded, events} = make();
    await assert.rejects(() => guarded(url, {method, body: JSON.stringify(payload)}));
    assert.equal(events.length, 0);
  }
});

test('valid SDK Request objects and Anthropic beta query stay bounded', async () => {
  const {guarded, events} = make();
  await guarded(new Request(config.baseURL+'/v1/messages?beta=true', {method: 'POST', body: JSON.stringify(body)}));
  assert.equal(events.filter(e => e[0] === 'fetch').length, 1);
  await assert.rejects(() => guarded(config.baseURL+'/v1/messages?beta=true&beta=true', {method:'POST', body:JSON.stringify(body)}));
});

test('unknown network cost holds the reservation and calls are never retried', async () => {
  assert.equal(typeof module.createBoundedLiveFetch, 'function');
  const events = [];
  const guarded = module.createBoundedLiveFetch(config, {
    reserve: async value => events.push(['reserve', value]),
    receipt: async value => events.push(['receipt', value]),
    fetch: async () => { events.push(['fetch']); throw new Error('do not persist key or provider body'); },
  });
  await assert.rejects(() => guarded(config.baseURL+'/v1/chat/completions', {method:'POST', body:JSON.stringify(body)}), /live_transport_failed/);
  assert.deepEqual(events.map(e => e[0]), ['reserve', 'fetch', 'receipt']);
  assert.equal(events[2][1].status, 'unknown');
  assert.ok(!JSON.stringify(events).includes('provider body'));
});

test('reservation failure and excess request count never reach the provider', async () => {
  const {guarded, events} = make({maxRequests: 1});
  await guarded(config.baseURL+'/v1/chat/completions', {method:'POST', body:JSON.stringify(body)});
  await assert.rejects(() => guarded(config.baseURL+'/v1/chat/completions', {method:'POST', body:JSON.stringify(body)}));
  assert.equal(events.filter(e => e[0] === 'fetch').length, 1);
  let calls = 0;
  const denied = module.createBoundedLiveFetch(config, {reserve: async () => {throw new Error('budget exhausted');},
    receipt: async () => {}, fetch: async () => {calls++;}});
  await assert.rejects(() => denied(config.baseURL+'/v1/chat/completions', {method:'POST', body:JSON.stringify(body)}));
  assert.equal(calls, 0);
});
