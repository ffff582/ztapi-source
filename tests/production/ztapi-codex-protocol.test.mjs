import test from 'node:test';
import assert from 'node:assert/strict';
import { runCodexProtocolAcceptance, validateCodexProtocolConfig } from './ztapi-codex-protocol.mjs';

const config = {
  baseUrl: 'http://127.0.0.1:18081',
  apiKey: 'sk-zt-secret-value',
  model: 'gpt-5.6-sol',
  budgetUsd: 1,
  maxCostPerRequestUsd: 0.25,
};

test('Codex protocol acceptance requires explicit credentials and budget', () => {
  assert.throws(() => validateCodexProtocolConfig({ ...config, apiKey: '' }), /apiKey/);
  assert.throws(() => validateCodexProtocolConfig({ ...config, budgetUsd: 0 }), /budgetUsd/);
  assert.throws(() => validateCodexProtocolConfig({ ...config, baseUrl: 'https://ztapi.vip' }), /loopback acceptance listener/);
});

test('Codex protocol acceptance verifies apply_patch round trip and compacted context', async () => {
  const requests = [];
  const marker = 'ZTAPI-CONTEXT-TEST';
  const fetchImpl = async (url, options) => {
    const body = JSON.parse(options.body);
    requests.push({ url, body });
    if (url.endsWith('/v1/responses/compact')) {
      assert.equal(body.model, 'gpt-5.6-sol');
      assert.match(JSON.stringify(body.input), new RegExp(marker));
      return new Response(JSON.stringify({ id: 'cmp_1', output: [{ type: 'compaction', encrypted_content: 'opaque' }], usage: { input_tokens: 20, output_tokens: 4 } }), { status: 200 });
    }
    if (requests.filter((entry) => entry.url.endsWith('/v1/responses')).length === 1) {
      assert.equal(body.max_output_tokens, 64);
      assert.equal(body.tool_choice.name, 'apply_patch');
      return new Response(JSON.stringify({ id: 'resp_tool', output: [{ type: 'function_call', call_id: 'call_1', name: 'apply_patch', arguments: JSON.stringify({ patch: '*** Begin Patch\n*** Add File: hello.py\n+print("hello")\n*** End Patch' }) }] }), { status: 200 });
    }
    if (body.previous_response_id === 'resp_tool') {
      assert.equal(body.input[0].type, 'function_call_output');
      assert.equal(body.input[0].call_id, 'call_1');
      return new Response(JSON.stringify({ id: 'resp_done', output_text: '文件修改完成' }), { status: 200 });
    }
    assert.deepEqual(body.input[0], { type: 'compaction', encrypted_content: 'opaque' });
    return new Response(JSON.stringify({ id: 'resp_context', output_text: marker }), { status: 200 });
  };

  const report = await runCodexProtocolAcceptance(config, fetchImpl, marker);
  assert.equal(report.passed, true);
  assert.equal(report.requestsExecuted, 4);
  assert.equal(report.toolRoundTrip.passed, true);
  assert.equal(report.contextCompaction.passed, true);
  assert.equal(report.reservedUsd, 1);
});
