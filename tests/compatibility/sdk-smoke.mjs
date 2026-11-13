import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {safeClientError as safeSDKError, validateSimulatedBridgeConfig as validateSDKSmokeConfig, verifySimulatedBridge} from './client-bridge.mjs';
export {safeSDKError, validateSDKSmokeConfig};

export function createGuardedSDKFetch(config, fetchImpl = fetch) {
  validateSDKSmokeConfig(config);
  const origin = new URL(config.base_url).origin;
  const paths = new Set(['/v1/chat/completions', '/v1/responses', '/v1/messages']);
  return async (input, init = {}) => {
    const request = input instanceof Request ? input : null;
    const url = new URL(request ? request.url : String(input));
    assert.equal(url.origin, origin, 'SDK request escaped the owned listener');
    assert.ok(!url.username && !url.password && !url.search && !url.hash && paths.has(url.pathname), 'unexpected SDK endpoint');
    assert.equal(String(init.method ?? request?.method ?? 'GET').toUpperCase(), 'POST');
    return fetchImpl(input, {...init, redirect: 'error'});
  };
}

export function safeSDKRequestID(id, apiKey) {
  if (typeof id !== 'string' || (apiKey && id.includes(apiKey))) return null;
  return /^(?:bridge-request-[0-9]+|[a-f0-9]{32}|[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})$/i.test(id) ? id : null;
}

export function sdkUsageEvidence(usage) {
  const count = value => Number.isSafeInteger(value) && value >= 0 ? value : null;
  const input = count(usage?.input_tokens ?? usage?.prompt_tokens);
  const output = count(usage?.output_tokens ?? usage?.completion_tokens);
  assert.ok(input !== null && output !== null, 'valid input/output usage is required');
  return {
    input_tokens: input,
    output_tokens: output,
    total_tokens: count(usage?.total_tokens),
    cache_read_tokens: count(usage?.cache_read_input_tokens ?? usage?.prompt_tokens_details?.cached_tokens),
    cache_write_tokens: count(usage?.cache_creation_input_tokens),
  };
}

export async function runSDKSmoke(config, sdkRoot) {
  validateSDKSmokeConfig(config);
  await verifySimulatedBridge(config);
  assert.ok(path.isAbsolute(sdkRoot), 'absolute SDK runtime directory required');
  const versions = {};
  for (const [name, version] of [['openai', '7.45.1'], ['@anthropic-ai/sdk', '1.3.0']]) {
    const pkg = JSON.parse(await readFile(path.join(sdkRoot, 'node_modules', name, 'package.json'), 'utf8'));
    assert.equal(pkg.version, version, 'SDK version differs from the qualification plan');
    versions[name] = pkg.version;
  }
  const require = createRequire(path.join(sdkRoot, 'package.json'));
  const OpenAI = (await import(pathToFileURL(require.resolve('openai')).href)).default;
  const Anthropic = (await import(pathToFileURL(require.resolve('@anthropic-ai/sdk')).href)).default;
  const origin = new URL(config.base_url).origin;
  const guardedFetch = createGuardedSDKFetch(config);
  const openai = new OpenAI({apiKey: config.api_key, baseURL: origin+'/v1', maxRetries: 0, timeout: 20000, fetch: guardedFetch});
  const anthropic = new Anthropic({apiKey: config.api_key, baseURL: origin, maxRetries: 0, timeout: 20000, fetch: guardedFetch});
  const model = config.model;
  const rows = [];
  let toolExecutions = 0;
  const text = response => (response.output ?? []).flatMap(item => item.content ?? []).map(part => part.text ?? '').join('');
  const messageText = response => response.content.filter(part => part.type === 'text').map(part => part.text).join('');
  const record = async (name, endpoint, execute) => {
    const started = performance.now();
    try {
      const response = await execute();
      rows.push({name, endpoint, passed: true, elapsed_ms: Math.round(performance.now()-started), request_id: safeSDKRequestID(response?._request_id, config.api_key), usage: sdkUsageEvidence(response?.usage)});
    } catch (error) {
      rows.push({name, endpoint, passed: false, elapsed_ms: Math.round(performance.now()-started), error: safeSDKError(error)});
      return false;
    }
    return true;
  };
  const user = {role: 'user', content: 'Return the synthetic acceptance marker.'};
  const base = {model, messages: [user], max_tokens: 1024};
  if (!await record('openai-chat-text', '/v1/chat/completions', async () => {
    const response = await openai.chat.completions.create(base);
    assert.equal(response.choices[0].message.content, 'ZTAPI_SDK_OK');
    assert.equal(response.choices[0].finish_reason, 'stop');
    return response;
  })) return finish();
  if (!await record('openai-chat-stream', '/v1/chat/completions', async () => {
    const stream = await openai.chat.completions.create({...base, stream: true, stream_options: {include_usage: true}});
    let content = '', terminal = false, usage;
    for await (const chunk of stream) {
      content += chunk.choices[0]?.delta?.content ?? '';
      terminal ||= chunk.choices[0]?.finish_reason === 'stop';
      if (chunk.usage) usage = chunk.usage;
    }
    assert.equal(content, 'ZTAPI_SDK_OK'); assert.ok(terminal); assert.ok(usage);
    return {usage};
  })) return finish();
  const responsesBase = {model, input: [user], max_output_tokens: 1024, store: false};
  if (!await record('openai-responses-text', '/v1/responses', async () => {
    const response = await openai.responses.create(responsesBase);
    assert.equal(text(response), 'ZTAPI_SDK_OK'); assert.equal(response.status, 'completed');
    return response;
  })) return finish();
  if (!await record('openai-responses-stream-helper', '/v1/responses', async () => {
    const stream = openai.responses.stream(responsesBase);
    let completed = false;
    for await (const event of stream) completed ||= event.type === 'response.completed';
    const response = await stream.finalResponse();
    assert.ok(completed); assert.equal(response.output_text, 'ZTAPI_SDK_OK'); assert.equal(response.status, 'completed');
    return response;
  })) return finish();
  if (!await record('anthropic-messages-text', '/v1/messages', async () => {
    const response = await anthropic.messages.create(base);
    assert.equal(messageText(response), 'ZTAPI_SDK_OK'); assert.equal(response.stop_reason, 'end_turn');
    return response;
  })) return finish();
  if (!await record('anthropic-messages-stream-helper', '/v1/messages', async () => {
    const stream = anthropic.messages.stream(base);
    let stopped = false;
    for await (const event of stream) stopped ||= event.type === 'message_stop';
    const response = await stream.finalMessage();
    assert.ok(stopped); assert.equal(messageText(response), 'ZTAPI_SDK_OK'); assert.equal(response.stop_reason, 'end_turn');
    return response;
  })) return finish();
  const schema = {type: 'object', properties: {value: {type: 'string'}}, required: ['value'], additionalProperties: false};
  const executeTool = input => { assert.deepEqual(input, {value: 'ZTAPI_TOOL_VALUE'}); toolExecutions++; return 'VALUE:ZTAPI_TOOL_VALUE'; };
  let chatStart;
  const chatTool = {type: 'function', function: {name: 'lookup', parameters: schema, strict: true}};
  if (!await record('openai-chat-forced-tool', '/v1/chat/completions', async () => {
    chatStart = await openai.chat.completions.create({...base, tools: [chatTool], tool_choice: {type: 'function', function: {name: 'lookup'}}, parallel_tool_calls: false});
    assert.equal(chatStart.choices[0].finish_reason, 'tool_calls');
    return chatStart;
  })) return finish();
  if (!await record('openai-chat-tool-result', '/v1/chat/completions', async () => {
    const message = chatStart.choices[0].message, call = message.tool_calls[0];
    assert.equal(call.function.name, 'lookup'); assert.ok(call.id);
    const response = await openai.chat.completions.create({...base, tools: [chatTool], tool_choice: 'none', messages: [user, message, {role: 'tool', tool_call_id: call.id, content: executeTool(JSON.parse(call.function.arguments))}]});
    assert.equal(response.choices[0].message.content, 'ZTAPI_TOOL_DONE');
    return response;
  })) return finish();
  let responsesStart;
  const responseTool = {type: 'function', name: 'lookup', parameters: schema, strict: true};
  if (!await record('openai-responses-forced-tool', '/v1/responses', async () => {
    responsesStart = await openai.responses.create({...responsesBase, tools: [responseTool], tool_choice: {type: 'function', name: 'lookup'}, parallel_tool_calls: false});
    assert.ok(responsesStart.output.some(item => item.type === 'function_call' && item.name === 'lookup'));
    return responsesStart;
  })) return finish();
  if (!await record('openai-responses-full-history-tool-result', '/v1/responses', async () => {
    const call = responsesStart.output.find(item => item.type === 'function_call');
    assert.ok(call.call_id);
    const response = await openai.responses.create({...responsesBase, tools: [responseTool], tool_choice: 'none', input: [user, ...responsesStart.output, {type: 'function_call_output', call_id: call.call_id, output: executeTool(JSON.parse(call.arguments))}]});
    assert.equal(text(response), 'ZTAPI_TOOL_DONE');
    return response;
  })) return finish();
  let claudeStart;
  const claudeTool = {name: 'lookup', input_schema: schema};
  if (!await record('anthropic-messages-forced-tool', '/v1/messages', async () => {
    claudeStart = await anthropic.messages.create({...base, tools: [claudeTool], tool_choice: {type: 'tool', name: 'lookup', disable_parallel_tool_use: true}});
    assert.equal(claudeStart.stop_reason, 'tool_use');
    return claudeStart;
  })) return finish();
  if (!await record('anthropic-messages-tool-result', '/v1/messages', async () => {
    const call = claudeStart.content.find(item => item.type === 'tool_use'); assert.ok(call?.id); assert.equal(call.name, 'lookup');
    const response = await anthropic.messages.create({...base, tools: [claudeTool], tool_choice: {type: 'none'}, messages: [user, {role: 'assistant', content: claudeStart.content}, {role: 'user', content: [{type: 'tool_result', tool_use_id: call.id, content: executeTool(call.input)}]}]});
    assert.equal(messageText(response), 'ZTAPI_TOOL_DONE');
    return response;
  })) return finish();
  assert.equal(toolExecutions, 3);
  return finish();

  function finish() {
    return {generated_at: new Date().toISOString(), provider_kind: 'simulated', sdk_versions: versions, planned_cases: 12, executed_cases: rows.length, tool_executions: toolExecutions, passed: rows.length === 12 && rows.every(row => row.passed), paid_calls: 0, results: rows};
  }
}

async function main() {
  const args = process.argv.slice(2);
  const option = name => { const i = args.indexOf(name); assert.ok(i >= 0 && args[i+1], `missing ${name}`); return path.resolve(args[i+1]); };
  const ready = option('--ready'), sdkRoot = option('--sdk-root'), outputDir = option('--output-dir');
  await mkdir(outputDir, {recursive: true});
  const config = JSON.parse(await readFile(ready, 'utf8'));
  try {
    const report = await runSDKSmoke(config, sdkRoot);
    await writeFile(path.join(outputDir, 'sdk-report.json'), JSON.stringify(report, null, 2), {mode: 0o600});
    process.stdout.write(JSON.stringify({passed: report.passed, executed_cases: report.executed_cases, paid_calls: 0})+'\n');
    if (!report.passed) process.exitCode = 1;
  } catch (error) {
    const report = {passed: false, provider_kind: 'simulated', paid_calls: 0, error: safeSDKError(error)};
    await writeFile(path.join(outputDir, 'sdk-report.json'), JSON.stringify(report, null, 2), {mode: 0o600});
    process.stderr.write(JSON.stringify(report)+'\n'); process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
