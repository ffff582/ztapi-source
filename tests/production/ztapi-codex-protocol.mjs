import { chmod, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

const maxOutputTokens = 64;

export function validateCodexProtocolConfig(config) {
  for (const name of ['baseUrl', 'apiKey', 'model']) {
    if (!String(config[name] ?? '').trim()) throw new Error(`missing required setting: ${name}`);
  }
  if (!Number.isFinite(config.budgetUsd) || config.budgetUsd <= 0) throw new Error('an explicit positive budgetUsd ceiling is required');
  if (!Number.isFinite(config.maxCostPerRequestUsd) || config.maxCostPerRequestUsd <= 0) throw new Error('an explicit positive maxCostPerRequestUsd ceiling is required');
  if (config.budgetUsd < 4 * config.maxCostPerRequestUsd) throw new Error('budgetUsd cannot cover the four required acceptance requests');
  const hostname = new URL(config.baseUrl).hostname;
  if (hostname !== '127.0.0.1' && hostname !== '[::1]') {
    throw new Error('baseUrl must use the loopback acceptance listener through an SSH tunnel');
  }
}

function outputText(body) {
  if (typeof body?.output_text === 'string') return body.output_text;
  const fragments = [];
  for (const item of body?.output ?? []) {
    for (const content of item?.content ?? []) {
      if (typeof content?.text === 'string') fragments.push(content.text);
    }
  }
  return fragments.join('\n');
}

async function postJSON(fetchImpl, url, apiKey, body) {
  const response = await fetchImpl(url, {
    method: 'POST',
    headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const raw = await response.text();
  let parsed = {};
  try { parsed = raw ? JSON.parse(raw) : {}; } catch { parsed = {}; }
  if (!response.ok) {
    const code = String(parsed?.error?.code ?? parsed?.error?.type ?? 'unknown').slice(0, 128);
    throw new Error(`protocol acceptance request failed: HTTP ${response.status}, code ${code}`);
  }
  return parsed;
}

function buildToolRequest(model) {
  return {
    model,
    input: [{ role: 'system', content: '你是一个简洁、客观的编程助手。' }, { role: 'user', content: '请使用 apply_patch 工具创建 hello.py，内容为打印 hello。' }],
    tools: [{
      type: 'function',
      name: 'apply_patch',
      description: 'Apply a repository patch.',
      parameters: {
        type: 'object',
        properties: { patch: { type: 'string' } },
        required: ['patch'],
        additionalProperties: false,
      },
      strict: true,
    }],
    tool_choice: { type: 'function', name: 'apply_patch' },
    reasoning: { effort: 'high' },
    max_output_tokens: maxOutputTokens,
  };
}

function buildLongContext(marker) {
  const input = [{ role: 'system', content: '你是一个简洁、客观的助手。请保留对话中的验证标记。' }];
  input.push({ role: 'user', content: `验证标记是 ${marker}。` });
  input.push({ role: 'assistant', content: `已记录 ${marker}。` });
  for (let index = 1; index <= 12; index += 1) {
    input.push({ role: 'user', content: `这是第 ${index} 条中性上下文，请继续记住验证标记。` });
    input.push({ role: 'assistant', content: `已处理第 ${index} 条。` });
  }
  return input;
}

export async function runCodexProtocolAcceptance(config, fetchImpl = fetch, marker = `ZTAPI-CONTEXT-${Date.now()}`) {
  validateCodexProtocolConfig(config);
  const base = String(config.baseUrl).replace(/\/$/, '');
  let requestsExecuted = 0;
  const call = async (path, body) => {
    requestsExecuted += 1;
    return postJSON(fetchImpl, `${base}${path}`, config.apiKey, body);
  };

  const toolStart = await call('/v1/responses', buildToolRequest(config.model));
  const functionCall = (toolStart.output ?? []).find((item) => item?.type === 'function_call' && item?.name === 'apply_patch');
  let argumentsBody = {};
  try { argumentsBody = JSON.parse(functionCall?.arguments ?? '{}'); } catch { argumentsBody = {}; }
  const patch = String(argumentsBody.patch ?? '');
  const toolRequestValid = Boolean(functionCall?.call_id) && patch.includes('*** Add File: hello.py') && patch.includes('print');
  if (!toolRequestValid) throw new Error('apply_patch tool call was missing or malformed');

  const toolDone = await call('/v1/responses', {
    model: config.model,
    previous_response_id: toolStart.id,
    input: [{ type: 'function_call_output', call_id: functionCall.call_id, output: 'Patch applied successfully.' }],
    tools: buildToolRequest(config.model).tools,
    reasoning: { effort: 'high' },
    max_output_tokens: maxOutputTokens,
  });
  if (!outputText(toolDone).trim()) throw new Error('tool completion response was empty');

  const compact = await call('/v1/responses/compact', { model: config.model, input: buildLongContext(marker) });
  if (!Array.isArray(compact.output) || compact.output.length === 0) throw new Error('compaction response did not contain reusable output');
  const continued = await call('/v1/responses', {
    model: config.model,
    input: [...compact.output, { role: 'user', content: '请只回复之前的验证标记。' }],
    reasoning: { effort: 'high' },
    max_output_tokens: maxOutputTokens,
  });
  const continuedText = outputText(continued);
  if (!continuedText.includes(marker)) throw new Error('context marker was lost after compaction');

  return {
    generatedAt: new Date().toISOString(),
    model: config.model,
    requestsExecuted,
    reservedUsd: requestsExecuted * config.maxCostPerRequestUsd,
    passed: true,
    toolRoundTrip: { passed: true, responseId: String(toolDone.id ?? '').slice(0, 255) },
    contextCompaction: { passed: true, compactResponseId: String(compact.id ?? '').slice(0, 255), responseId: String(continued.id ?? '').slice(0, 255) },
  };
}

async function main() {
  const config = {
    baseUrl: process.env.ZTAPI_CODEX_BASE_URL,
    apiKey: process.env.ZTAPI_CODEX_API_KEY,
    model: process.env.ZTAPI_CODEX_MODEL,
    budgetUsd: Number(process.env.ZTAPI_CODEX_BUDGET_USD),
    maxCostPerRequestUsd: Number(process.env.ZTAPI_CODEX_MAX_REQUEST_COST_USD),
  };
  try {
    const report = await runCodexProtocolAcceptance(config);
    const outputPath = process.env.ZTAPI_CODEX_OUTPUT;
    if (!outputPath) throw new Error('missing required setting: outputPath');
    await writeFile(outputPath, `${JSON.stringify(report, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 });
    await chmod(outputPath, 0o600);
    process.stdout.write(`Codex protocol acceptance passed: ${report.requestsExecuted} requests\n`);
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
