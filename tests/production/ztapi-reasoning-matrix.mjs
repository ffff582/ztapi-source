import { chmod, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

export const MAX_OUTPUT_TOKENS = 64;
const neutralInput = '请简洁回答：35 加 42 等于多少？';
const discoveryEfforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

export function redactSecrets(value) {
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  return text
    .replace(/Bearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer [REDACTED]')
    .replace(/\bsk-[A-Za-z0-9_-]{8,}\b/g, 'sk-[REDACTED]');
}

export function validateReasoningConfig(config) {
  for (const name of ['baseUrl', 'adminBaseUrl', 'apiKey', 'adminToken', 'adminUserId', 'outputPath']) {
    if (!String(config[name] ?? '').trim()) throw new Error(`missing required setting: ${name}`);
  }
  if (!Number.isFinite(config.budgetUsd) || config.budgetUsd <= 0) {
    throw new Error('an explicit positive budgetUsd ceiling is required');
  }
  if (!Number.isFinite(config.maxCostPerCaseUsd) || config.maxCostPerCaseUsd <= 0) {
    throw new Error('an explicit positive maxCostPerCaseUsd ceiling is required');
  }
  for (const name of ['baseUrl', 'adminBaseUrl']) {
    const hostname = new URL(config[name]).hostname;
    if (hostname !== '127.0.0.1' && hostname !== '[::1]') {
      throw new Error(`${name} must use the loopback acceptance listener through an SSH tunnel`);
    }
  }
}

export function buildReasoningCases(models) {
  const cases = [];
  for (const model of models) {
    if (!model.published || model.modality !== 'text') continue;
    const efforts = [...new Set(model.reasoning_efforts ?? [])];
    for (const effort of efforts) {
      cases.push({ publicModel: model.public_name, sourceModel: model.source_model, requestedEffort: effort, expected: 'accepted', expectedForwarded: effort, expectedSource: 'request' });
    }
    if (efforts.includes('max')) {
      cases.push({ publicModel: model.public_name, sourceModel: model.source_model, requestedEffort: 'ultra', expected: 'accepted', expectedForwarded: 'max', expectedSource: 'codex_alias_mapping' });
    } else if (efforts.length > 0) {
      cases.push({ publicModel: model.public_name, sourceModel: model.source_model, requestedEffort: 'ultra', expected: 'unsupported', expectedForwarded: null, expectedSource: null });
    } else if (model.reasoning_capability_live_validation_required === true) {
      for (const effort of discoveryEfforts) {
        cases.push({ publicModel: model.public_name, sourceModel: model.source_model, requestedEffort: effort, expected: 'discover', expectedForwarded: effort, expectedSource: 'request' });
      }
    }
  }
  return cases;
}

function headers(token, userId) {
  return { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', ...(userId ? { 'New-API-User': String(userId) } : {}) };
}

async function jsonRequest(fetchImpl, url, options) {
  const response = await fetchImpl(url, options);
  const raw = await response.text();
  let body = {};
  try { body = raw ? JSON.parse(raw) : {}; } catch { body = {}; }
  return { response, body };
}

function safeResponseEvidence(status, body) {
  const reasoningTokens = body?.usage?.output_tokens_details?.reasoning_tokens ?? body?.usage?.completion_tokens_details?.reasoning_tokens;
  return {
    httpStatus: status,
    responseId: String(body?.id ?? body?.request_id ?? '').slice(0, 255),
    errorCode: String(body?.error?.code ?? body?.error?.type ?? '').slice(0, 128),
    errorMessage: String(body?.error?.message ?? '').slice(0, 255),
    terminalStatus: String(body?.status ?? body?.choices?.[0]?.finish_reason ?? '').slice(0, 64),
    reasoningTokensReported: Number.isInteger(reasoningTokens) && reasoningTokens >= 0 ? reasoningTokens : null,
  };
}

function explicitlyRejectsReasoningEffort(status, body) {
  if (status !== 400 && status !== 422) return false;
  const text = `${body?.error?.code ?? ''} ${body?.error?.type ?? ''} ${body?.error?.message ?? ''}`;
  return /(reasoning|effort|thinking)/i.test(text) && /(invalid|unsupported|not\s+supported|不支持|无效)/i.test(text);
}

async function waitForAudit(fetchImpl, config, requestId) {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const url = `${config.adminBaseUrl}/api/admin/request-logs?request_id=${encodeURIComponent(requestId)}&p=1&page_size=1`;
    const { response, body } = await jsonRequest(fetchImpl, url, { headers: headers(config.adminToken, config.adminUserId) });
    const item = body?.data?.items?.[0];
    if (response.ok && item) {
      const metadata = item.metadata ?? item.other ?? {};
      return {
        received: metadata.reasoning_effort_received ?? null,
        forwarded: metadata.reasoning_effort_forwarded ?? null,
        source: metadata.reasoning_effort_source ?? null,
        reasoningTokensReported: metadata.reasoning_tokens_reported ?? null,
        upstreamRequestId: item.upstream_request_id ?? metadata.upstream_request_id ?? null,
      };
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  return null;
}

export async function runReasoningMatrix(config, fetchImpl = fetch) {
  validateReasoningConfig(config);
  const catalog = await jsonRequest(fetchImpl, `${config.adminBaseUrl}/api/models/ztapi/?page_size=100`, {
    headers: headers(config.adminToken, config.adminUserId),
  });
  if (!catalog.response.ok || catalog.body?.success !== true) throw new Error('failed to load the admin model catalog');
  const models = catalog.body?.data?.items ?? catalog.body?.data ?? [];
  const cases = buildReasoningCases(Array.isArray(models) ? models : []);
  const results = [];
  let reservedUsd = 0;

  for (const [index, testCase] of cases.entries()) {
    if (reservedUsd + config.maxCostPerCaseUsd > config.budgetUsd) break;
    reservedUsd += config.maxCostPerCaseUsd;
    const clientRequestId = `ztapi-reasoning-${Date.now()}-${index}`;
    const { response, body } = await jsonRequest(fetchImpl, `${config.baseUrl}/v1/responses`, {
      method: 'POST',
      headers: { ...headers(config.apiKey), 'X-Request-ID': clientRequestId },
      body: JSON.stringify({
        model: testCase.publicModel,
        input: [{ role: 'system', content: '你是一个简洁、客观的助手。' }, { role: 'user', content: neutralInput }],
        reasoning: { effort: testCase.requestedEffort },
        max_output_tokens: MAX_OUTPUT_TOKENS,
      }),
    });
    const requestId = String(response.headers.get('x-request-id') ?? '').trim() || clientRequestId;
    const evidence = safeResponseEvidence(response.status, body);
    const audit = response.ok ? await waitForAudit(fetchImpl, config, requestId) : null;
    let discovery = null;
    if (testCase.expected === 'discover') {
      if (response.ok
        && audit?.received === testCase.requestedEffort
        && audit?.forwarded === testCase.expectedForwarded
        && audit?.source === testCase.expectedSource) {
        discovery = 'accepted';
      } else if (explicitlyRejectsReasoningEffort(response.status, body)) {
        discovery = 'unsupported';
      } else {
        discovery = 'inconclusive';
      }
    }
    const passed = testCase.expected === 'discover'
      ? discovery !== 'inconclusive'
      : testCase.expected === 'unsupported'
      ? response.status === 400 && evidence.errorCode === 'invalid_reasoning_effort'
      : response.ok
        && audit?.received === testCase.requestedEffort
        && audit?.forwarded === testCase.expectedForwarded
        && audit?.source === testCase.expectedSource;
    results.push({ ...testCase, clientRequestId, requestId, ...evidence, audit, discovery, passed });
  }
  return { generatedAt: new Date().toISOString(), maxOutputTokens: MAX_OUTPUT_TOKENS, budgetUsd: config.budgetUsd, reservedUsd, plannedCases: cases.length, executedCases: results.length, results };
}

export function assertReasoningReportPassed(report) {
	if (report.executedCases !== report.plannedCases) {
		throw new Error(`reasoning matrix incomplete: ${report.executedCases}/${report.plannedCases}`);
	}
	const failures = report.results.filter((result) => !result.passed);
	if (failures.length > 0) {
		throw new Error(`reasoning matrix failed: ${failures.length} case(s)`);
	}
}

async function main() {
  const config = {
    baseUrl: process.env.ZTAPI_REASONING_BASE_URL,
    adminBaseUrl: process.env.ZTAPI_REASONING_ADMIN_BASE_URL,
    apiKey: process.env.ZTAPI_REASONING_API_KEY,
    adminToken: process.env.ZTAPI_REASONING_ADMIN_TOKEN,
    adminUserId: process.env.ZTAPI_REASONING_ADMIN_USER_ID,
    budgetUsd: Number(process.env.ZTAPI_REASONING_BUDGET_USD),
    maxCostPerCaseUsd: Number(process.env.ZTAPI_REASONING_MAX_CASE_COST_USD),
    outputPath: process.env.ZTAPI_REASONING_OUTPUT,
  };
  try {
    const report = await runReasoningMatrix(config);
    await writeFile(config.outputPath, `${redactSecrets(report)}\n`, { encoding: 'utf8', mode: 0o600 });
    await chmod(config.outputPath, 0o600);
	assertReasoningReportPassed(report);
    process.stdout.write(`reasoning matrix completed: ${report.executedCases}/${report.plannedCases}\n`);
  } catch (error) {
    process.stderr.write(`${redactSecrets(error instanceof Error ? error.message : error)}\n`);
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
