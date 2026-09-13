import test from 'node:test';
import assert from 'node:assert/strict';
import { assertReasoningReportPassed, buildReasoningCases, MAX_OUTPUT_TOKENS, redactSecrets, runReasoningMatrix, validateReasoningConfig } from './ztapi-reasoning-matrix.mjs';

const base = { baseUrl: 'http://127.0.0.1:18081', adminBaseUrl: 'http://127.0.0.1:18081', apiKey: 'sk-customer-secret-123456', adminToken: 'sk-admin-secret-123456', adminUserId: '1', budgetUsd: 0.02, maxCostPerCaseUsd: 0.01, outputPath: 'evidence.json' };

test('reasoning matrix requires explicit positive budget ceilings and fixes output at 64 tokens', () => {
  assert.equal(MAX_OUTPUT_TOKENS, 64);
  assert.throws(() => validateReasoningConfig({ ...base, budgetUsd: 0 }), /budgetUsd/);
  assert.throws(() => validateReasoningConfig({ ...base, maxCostPerCaseUsd: NaN }), /maxCostPerCaseUsd/);
  assert.throws(() => validateReasoningConfig({ ...base, baseUrl: 'https://ztapi.vip' }), /loopback acceptance listener/);
  assert.throws(() => validateReasoningConfig({ ...base, adminBaseUrl: 'https://admin.ztapi.vip' }), /loopback acceptance listener/);
});

test('reasoning cases cover declared levels, exact ultra mapping, and pending capability discovery', () => {
  assert.deepEqual(buildReasoningCases([
    { published: true, modality: 'text', public_name: 'zt-sol', source_model: 'sol', reasoning_efforts: ['none', 'max'] },
    { published: true, modality: 'text', public_name: 'zt-old', source_model: 'old', reasoning_efforts: ['low'] },
    { published: true, modality: 'text', public_name: 'zt-pending', source_model: 'pending', reasoning_efforts: [], reasoning_capability_live_validation_required: true },
    { published: true, modality: 'image', public_name: 'zt-image', source_model: 'image', reasoning_efforts: ['high'] },
  ]), [
    { publicModel: 'zt-sol', sourceModel: 'sol', requestedEffort: 'none', expected: 'accepted', expectedForwarded: 'none', expectedSource: 'request' },
    { publicModel: 'zt-sol', sourceModel: 'sol', requestedEffort: 'max', expected: 'accepted', expectedForwarded: 'max', expectedSource: 'request' },
    { publicModel: 'zt-sol', sourceModel: 'sol', requestedEffort: 'ultra', expected: 'accepted', expectedForwarded: 'max', expectedSource: 'codex_alias_mapping' },
    { publicModel: 'zt-old', sourceModel: 'old', requestedEffort: 'low', expected: 'accepted', expectedForwarded: 'low', expectedSource: 'request' },
    { publicModel: 'zt-old', sourceModel: 'old', requestedEffort: 'ultra', expected: 'unsupported', expectedForwarded: null, expectedSource: null },
    ...['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'].map((requestedEffort) => ({
      publicModel: 'zt-pending', sourceModel: 'pending', requestedEffort, expected: 'discover', expectedForwarded: requestedEffort, expectedSource: 'request',
    })),
  ]);
});

test('pending discovery accepts only audited success or an explicit effort rejection', async () => {
  const fetchImpl = async (url, options = {}) => {
    if (url.includes('/api/models/ztapi/')) {
      return new Response(JSON.stringify({ success: true, data: { items: [{ published: true, modality: 'text', public_name: 'zt-pending', source_model: 'pending', reasoning_efforts: [], reasoning_capability_live_validation_required: true }] } }), { status: 200 });
    }
    if (url.includes('/api/admin/request-logs')) {
      const requestId = new URL(url).searchParams.get('request_id');
      const effort = requestId.endsWith('-0') ? 'none' : null;
      return new Response(JSON.stringify({ success: true, data: { items: effort ? [{ metadata: { reasoning_effort_received: effort, reasoning_effort_forwarded: effort, reasoning_effort_source: 'request' } }] : [] } }), { status: 200 });
    }
    const effort = JSON.parse(options.body).reasoning.effort;
    if (effort === 'none') return new Response(JSON.stringify({ id: 'accepted', status: 'completed' }), { status: 200, headers: { 'X-Request-ID': options.headers['X-Request-ID'] } });
    return new Response(JSON.stringify({ error: { code: 'invalid_request_error', message: `reasoning effort ${effort} is not supported` } }), { status: 400 });
  };
  const report = await runReasoningMatrix({ ...base, budgetUsd: 0.07 }, fetchImpl);
  assert.equal(report.executedCases, 7);
  assert.deepEqual(report.results.map((result) => result.discovery), ['accepted', 'unsupported', 'unsupported', 'unsupported', 'unsupported', 'unsupported', 'unsupported']);
  assert.doesNotThrow(() => assertReasoningReportPassed(report));
});

test('secret redaction covers bearer and sk forms', () => {
  const redacted = redactSecrets({ authorization: 'Bearer abc.DEF-123', apiKey: 'sk-test-secret-123456' });
  assert.doesNotMatch(redacted, /abc\.DEF|test-secret/);
  assert.match(redacted, /REDACTED/);
});

test('reasoning acceptance fails when any case failed or the budget stopped the matrix early', () => {
	assert.throws(() => assertReasoningReportPassed({ plannedCases: 2, executedCases: 1, results: [{ passed: true }] }), /incomplete/);
	assert.throws(() => assertReasoningReportPassed({ plannedCases: 1, executedCases: 1, results: [{ passed: false }] }), /failed/);
	assert.doesNotThrow(() => assertReasoningReportPassed({ plannedCases: 1, executedCases: 1, results: [{ passed: true }] }));
});

test('matrix stops before budget ceiling and sends neutral 64-token requests', async () => {
  const outbound = [];
  const fetchImpl = async (url, options = {}) => {
    outbound.push({ url, options });
    if (url.includes('/api/models/ztapi/')) return new Response(JSON.stringify({ success: true, data: { items: [{ published: true, modality: 'text', public_name: 'zt-sol', source_model: 'sol', reasoning_efforts: ['low', 'high', 'max'] }] } }), { status: 200 });
    if (url.includes('/api/admin/request-logs')) {
	  const requestId = new URL(url).searchParams.get('request_id');
	  assert.match(requestId, /^server-ztapi-reasoning-/);
	  const clientRequestId = requestId.replace(/^server-/, '');
	  const request = outbound.find((entry) => entry.url.endsWith('/v1/responses') && entry.options.headers['X-Request-ID'] === clientRequestId);
	  assert.ok(request, `audit lookup must bind to request ${requestId}`);
      const effort = JSON.parse(request.options.body).reasoning.effort;
      return new Response(JSON.stringify({ success: true, data: { items: [{ metadata: { reasoning_effort_received: effort, reasoning_effort_forwarded: effort, reasoning_effort_source: 'request' } }] } }), { status: 200 });
    }
    const request = JSON.parse(options.body);
    assert.equal(request.max_output_tokens, 64);
    assert.match(request.input[0].content, /简洁、客观/);
    return new Response(JSON.stringify({ id: 'response-safe', status: 'completed', usage: { output_tokens_details: { reasoning_tokens: 3 } } }), {
      status: 200,
      headers: { 'X-Request-ID': `server-${options.headers['X-Request-ID']}` },
    });
  };
  const report = await runReasoningMatrix({ ...base, budgetUsd: 0.02 }, fetchImpl);
  assert.equal(report.plannedCases, 4);
  assert.equal(report.executedCases, 2);
  assert.equal(report.reservedUsd, 0.02);
	assert.deepEqual(report.results.map((result) => [result.requestedEffort, result.passed]), [['low', true], ['high', true]]);
	assert.ok(report.results.every((result) => result.requestId.startsWith('server-ztapi-reasoning-')));
});
