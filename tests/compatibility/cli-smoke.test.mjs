import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, stat, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const runnerUrl = new URL('./cli-smoke.mjs', import.meta.url);
let runner = {};
try { runner = await import(runnerUrl.href); } catch (error) {
  if (error.code !== 'ERR_MODULE_NOT_FOUND') throw error;
}
const need = (name) => {
  assert.equal(typeof runner[name], 'function', `runner must export ${name}`);
  return runner[name];
};
const key = 'local-fixture-secret-never-log';
const ready = (overrides = {}) => ({
  base_url: 'http://127.0.0.1:19091', api_key: key,
  model: 'zt-claude-sonnet-5', pid: 1234, run_nonce: '0123456789abcdef0123456789abcdef',
  provider_kind: 'simulated', ...overrides,
});
async function temporary(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'ztapi-cli-unit-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

test('arguments require one explicit client and absolute ready/output paths', () => {
  const parse = need('parseArgs');
  const absolute = path.resolve('fixture-ready.json');
  const out = path.resolve('cli-output');
  assert.deepEqual(parse(['--client', 'codex', '--ready', absolute, '--output-dir', out]),
    { client: 'codex', ready: absolute, outputDir: out });
  for (const args of [[], ['--client', 'cursor'], ['--client', 'codex', '--ready', 'relative'],
    ['--client', 'codex', '--ready', absolute, '--output-dir', 'relative'],
    ['--client', 'codex', '--ready', absolute, '--output-dir', out, '--api-key', key],
    ['--client', 'codex', '--ready', absolute, '--output-dir', out, '--model-catalog-json', absolute],
    ['--client', 'codex', '--client', 'codex', '--ready', absolute, '--output-dir', out]]) {
    assert.throws(() => parse(args), /arguments/);
  }
});

test('readiness accepts only canonical loopback HTTP with an explicit port', () => {
  const validate = need('validateReady');
  for (const base_url of ['http://127.0.0.1:19091', 'http://[::1]:19091/']) {
    assert.equal(validate(ready({ base_url })).base_url, base_url.replace(/\/$/, ''));
  }
  for (const base_url of ['https://127.0.0.1:19091', 'http://127.0.0.1', 'http://127.0.0.1:0',
    'http://127.0.0.1:65536', 'http://127.1:19091', 'http://2130706433:19091',
    'http://127.0.0.1:19091/v1', 'http://127.0.0.1:19091/?api_key=x',
    'http://user:pass@localhost:19091', 'http://localhost.evil:19091', 'http://paid.example:19091',
    'http://127.0.0.1:19091/#x', 'http://127.0.0.1:19091\\@paid.example',
    'http://localhost:19091', 'http://127.0.0.1:80']) {
    assert.throws(() => validate(ready({ base_url })), /readiness/);
  }
});

test('readiness rejects live providers, wrong models, missing keys and invalid bridge PIDs without echoing them', () => {
  const validate = need('validateReady');
  for (const value of [null, [], ready({ provider_kind: 'live' }), ready({ model: 'other' }),
    ready({ api_key: '' }), ready({ api_key: `${key}\n` }), ready({ pid: 0 }), ready({ pid: '1234' }),
    ready({ run_nonce: undefined }), ready({ run_nonce: 'A'.repeat(32) })]) {
    assert.throws(() => validate(value), (error) => /readiness/.test(error.message) && !error.message.includes(key));
  }
});

test('child env is built from a case-insensitive core allowlist and fresh owned directories', () => {
  const build = need('buildChildEnvironment');
  const root = path.resolve('owned-output', '.isolation');
  const inherited = { Path: 'core-path', SystemRoot: 'C:\\Windows', ComSpec: 'cmd.exe',
    HOME: 'real-home', USERPROFILE: 'real-profile', TEMP: 'real-temp', APPDATA: 'real-appdata',
    OPENAI_API_KEY: 'real-key', ANTHROPIC_AUTH_TOKEN: 'oauth', HTTP_PROXY: 'proxy',
    HTTPS_PROXY: 'real-https-proxy', ALL_PROXY: 'real-all-proxy', http_proxy: 'lower-http-proxy',
    https_proxy: 'lower-https-proxy', all_proxy: 'lower-all-proxy', NO_PROXY: '*', no_proxy: 'paid.example',
    CLAUDE_CODE_SIMPLE: '1', claude_code_simple: '1', CLAUDE_CODE_SAFE_MODE: '0',
    CLAUDE_CODE_OAUTH_TOKEN: 'real-oauth', CLAUDE_CODE_OAUTH_REFRESH_TOKEN: 'real-refresh',
    CLAUDE_CODE_HOST_AUTH_ENV_VAR: 'REAL_AUTH_HELPER', REAL_AUTH_HELPER: 'real-helper-token',
    MODEL_CATALOG_JSON: 'real-user-catalog', CODEX_MODEL_CATALOG_JSON: 'real-user-catalog',
    NODE_OPTIONS: '--require bad.js', CLAUDE_CODE_USE_BEDROCK: '1', MCP_TOKEN: 'mcp',
    CODEX_HOME: 'real-codex', CODEX_INTERNAL_ORIGINATOR_OVERRIDE: 'desktop',
    GITHUB_TOKEN: 'github', PLUGIN_CONFIG: 'plugin', CUSTOM_UNRECOGNIZED: 'must-not-survive' };
  const before = { ...inherited };
  const env = build('codex', validate(), root, inherited);
  assert.equal(env.Path, 'core-path');
  assert.equal(env.SystemRoot, 'C:\\Windows');
  assert.equal(env.ZTAPI_ACCEPTANCE_KEY, key);
  assert.equal(env.NO_PROXY, '127.0.0.1,localhost,::1');
  assert.equal(env.no_proxy, '127.0.0.1,localhost,::1');
  for (const name of ['HOME', 'USERPROFILE', 'CODEX_HOME', 'CLAUDE_CONFIG_DIR', 'TEMP', 'TMP', 'APPDATA', 'LOCALAPPDATA']) {
    assert.ok(env[name].startsWith(`${root}${path.sep}`), name);
  }
  for (const name of ['OPENAI_API_KEY', 'ANTHROPIC_AUTH_TOKEN', 'HTTP_PROXY', 'NODE_OPTIONS',
    'CLAUDE_CODE_USE_BEDROCK', 'MCP_TOKEN', 'GITHUB_TOKEN', 'PLUGIN_CONFIG', 'CUSTOM_UNRECOGNIZED',
    'CODEX_INTERNAL_ORIGINATOR_OVERRIDE', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy',
    'CLAUDE_CODE_SIMPLE', 'claude_code_simple', 'CLAUDE_CODE_SAFE_MODE', 'CLAUDE_CODE_OAUTH_TOKEN',
    'CLAUDE_CODE_OAUTH_REFRESH_TOKEN', 'CLAUDE_CODE_HOST_AUTH_ENV_VAR', 'REAL_AUTH_HELPER',
    'MODEL_CATALOG_JSON', 'CODEX_MODEL_CATALOG_JSON']) {
    assert.equal(env[name], undefined, name);
  }
  assert.deepEqual(inherited, before);
  const claude = build('claude-code', validate(), root, inherited);
  assert.equal(claude.ANTHROPIC_API_KEY, key);
  assert.equal(claude.ANTHROPIC_BASE_URL, ready().base_url);
  assert.equal(claude.ZTAPI_ACCEPTANCE_KEY, undefined);
  assert.equal(claude.CLAUDE_CODE_SAFE_MODE, '1');
  assert.equal(claude.CLAUDE_CODE_SIMPLE, undefined);
  assert.equal(claude.claude_code_simple, undefined);
  assert.equal(claude.NO_PROXY, '127.0.0.1,localhost,::1');
  assert.equal(claude.no_proxy, '127.0.0.1,localhost,::1');
  for (const name of ['HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy',
    'ANTHROPIC_AUTH_TOKEN', 'CLAUDE_CODE_OAUTH_TOKEN', 'CLAUDE_CODE_OAUTH_REFRESH_TOKEN',
    'CLAUDE_CODE_HOST_AUTH_ENV_VAR', 'REAL_AUTH_HELPER']) assert.equal(claude[name], undefined, name);
  function validate() { return need('validateReady')(ready()); }
});

test('Codex argv explicitly selects Responses provider without credentials or bypass flags', () => {
  const build = need('buildInvocation');
  const catalogPath = path.resolve('owned evidence', 'fixture-models.json');
  const invocation = build('codex', need('validateReady')(ready()), path.resolve('project'), catalogPath);
  const args = invocation.args;
  assert.equal(args[0], 'exec');
  for (const flag of ['--ignore-user-config', '--ignore-rules', '--ephemeral', '--skip-git-repo-check', '--json']) {
    assert.ok(args.includes(flag), flag);
  }
  for (const [flag, value] of [['--color', 'never'], ['-s', 'workspace-write']]) {
    assert.equal(args[args.indexOf(flag) + 1], value);
  }
  const config = args.flatMap((arg, i) => arg === '-c' ? [args[i + 1]] : []).join('\n');
  for (const value of ['model_provider="ztapi_acceptance"', 'wire_api="responses"',
    'env_key="ZTAPI_ACCEPTANCE_KEY"', 'base_url="http://127.0.0.1:19091/v1"',
    'model="zt-claude-sonnet-5"', 'approval_policy="never"', 'web_search="disabled"']) {
    assert.ok(config.includes(value), value);
  }
  for (const value of ['features.plugins=false', 'features.remote_plugin=false', 'features.hooks=false',
    'features.apps=false', 'mcp_servers={}', 'features.api_key_model_discovery=false',
    `model_catalog_json=${JSON.stringify(catalogPath)}`]) assert.ok(config.includes(value), value);
  assert.ok(!args.join(' ').includes(key));
  assert.ok(!args.some((arg) => /danger|bypass/.test(arg)));
  assert.match(invocation.input, /fixture\.txt/);
  assert.match(invocation.input, /client-result\.txt/);
  assert.match(invocation.input, /echo ZTAPI_CLIENT_EXEC_OK/);
  assert.match(invocation.input, /ZTAPI_CLIENT_FILE_OK/);
  assert.match(invocation.input, /ZTAPI_CLIENT_OK/);
});

test('Claude argv uses safe mode without the bare/simple tool filter, limited to Read/Write', () => {
  const invocation = need('buildInvocation')('claude-code', need('validateReady')(ready()),
    path.resolve('project'), path.resolve('owned'));
  const args = invocation.args;
  for (const flag of ['--safe-mode', '--print', '--verbose', '--no-session-persistence', '--strict-mcp-config']) {
    assert.ok(args.includes(flag), flag);
  }
  for (const [flag, value] of [['--output-format', 'stream-json'], ['--tools', 'Read,Write'],
    ['--allowedTools', 'Read,Write'], ['--setting-sources', ''], ['--max-budget-usd', '0.8'],
    ['--model', 'zt-claude-sonnet-5']]) assert.equal(args[args.indexOf(flag) + 1], value);
  assert.deepEqual(JSON.parse(args[args.indexOf('--mcp-config') + 1]), { mcpServers: {} });
  assert.ok(!args.includes('--bare'), '2.1.312 bare mode removes Write before the tools allowlist is applied');
  assert.ok(!args.some((arg) => /danger|bypass|apiKeyHelper/.test(arg)));
  assert.ok(!args.join(' ').includes(key));
  assert.ok(!invocation.input.includes('ZTAPI_CLIENT_EXEC_OK'));
});

test('redaction removes known key, encoded key, bearer tokens and JSON auth fields', () => {
  const redact = need('redact');
  const text = `${key} ${encodeURIComponent(key)} Bearer arbitrary.credential== {"api_key":"other-secret","authorization":"secret"}`;
  const clean = redact(text, [key]);
  for (const secret of [key, 'arbitrary.credential', 'other-secret', '"secret"']) assert.ok(!clean.includes(secret));
  assert.match(clean, /REDACTED/);
});

test('Claude resolver checks package identity and resolves both JS and native npm shims without executing them', async (t) => {
  const resolve = need('resolveClaudeEntry');
  const root = await temporary(t);
  const pkg = path.join(root, 'node_modules', '@anthropic-ai', 'claude-code');
  await mkdir(path.join(pkg, 'bin'), { recursive: true });
  const shim = path.join(root, 'claude.ps1');
  for (const bin of ['cli.js', 'bin/claude.exe']) {
    await writeFile(path.join(pkg, bin), '');
    await writeFile(path.join(pkg, 'package.json'), JSON.stringify({ name: '@anthropic-ai/claude-code',
      version: '2.1.312', bin: { claude: bin } }));
    await writeFile(shim, `& "$basedir/node_modules/@anthropic-ai/claude-code/${bin}" $args`);
    const resolved = await resolve(shim);
    assert.equal(resolved.command, bin.endsWith('.js') ? process.execPath : path.join(pkg, bin));
    assert.deepEqual(resolved.prefixArgs, bin.endsWith('.js') ? [path.join(pkg, bin)] : []);
  }
  for (const metadata of [{ name: 'other', version: '2.1.312', bin: { claude: 'cli.js' } },
    { name: '@anthropic-ai/claude-code', version: '0.0.0', bin: { claude: 'cli.js' } },
    { name: '@anthropic-ai/claude-code', version: '2.1.312', bin: { claude: '../escape.js' } }]) {
    await writeFile(path.join(pkg, 'package.json'), JSON.stringify(metadata));
    await assert.rejects(resolve(shim), /client_installation/);
  }
});

function codexEvents() {
  return [
    { type: 'item.completed', item: { id: 'exec-1', type: 'command_execution',
      command: 'Get-Content fixture.txt; echo ZTAPI_CLIENT_EXEC_OK', exit_code: 0,
      status: 'completed', aggregated_output: 'fixture data\nZTAPI_CLIENT_EXEC_OK\n' } },
    { type: 'item.completed', item: { id: 'patch-1', type: 'file_change', status: 'completed',
      changes: [{ path: 'client-result.txt', kind: 'add' }] } },
    { type: 'item.completed', item: { type: 'agent_message', text: 'ZTAPI_CLIENT_OK' } },
    { type: 'turn.completed', usage: { input_tokens: 1, output_tokens: 1 } },
  ];
}
function claudeEvents() {
  return [
    { type: 'assistant', message: { content: [
      { type: 'tool_use', id: 'read-1', name: 'Read', input: { file_path: 'fixture.txt' } },
      { type: 'tool_use', id: 'write-1', name: 'Write', input: { file_path: 'client-result.txt', content: 'ZTAPI_CLIENT_FILE_OK' } },
    ] } },
    { type: 'user', message: { content: [
      { type: 'tool_result', tool_use_id: 'read-1', content: 'fixture data' },
      { type: 'tool_result', tool_use_id: 'write-1', content: 'File created successfully.' },
    ] } },
    { type: 'result', subtype: 'success', is_error: false, result: 'ZTAPI_CLIENT_OK' },
    { type: 'system', subtype: 'init', tools: ['Read', 'Write'] },
  ];
}
const jsonl = (events) => events.map((event) => JSON.stringify(event)).join('\n') + '\n';

test('event summaries require actual completed Codex tools and a terminal marker', () => {
  const summarize = need('summarizeEvents');
  const good = summarize('codex', jsonl(codexEvents()));
  assert.equal(good.success, true);
  assert.equal(good.successful_tool_events, 2);
  assert.equal(good.exec_marker, true);
  assert.equal(good.terminal_event, 'turn.completed');
  assert.deepEqual(good.tool_ids, ['exec-1', 'patch-1']);
  for (const events of [codexEvents().slice(1), codexEvents().slice(0, -1),
    [{ type: 'item.completed', item: { type: 'agent_message', text: 'ZTAPI_CLIENT_OK ZTAPI_CLIENT_EXEC_OK' } },
      { type: 'turn.completed' }],
    [...codexEvents(), { type: 'turn.failed', error: { message: 'failed' } }]]) {
    assert.equal(summarize('codex', jsonl(events)).success, false);
  }
});

test('Claude summaries associate Read/Write result IDs, reject failed/orphan tools and require final success', () => {
  const summarize = need('summarizeEvents');
  const good = summarize('claude-code', jsonl(claudeEvents()));
  assert.equal(good.success, true);
  assert.equal(good.successful_tool_events, 2);
  assert.deepEqual(good.tool_ids, ['read-1', 'write-1']);
  for (const mutate of [
    (events) => { events[1].message.content[0].is_error = true; },
    (events) => { events[1].message.content[0].tool_use_id = 'orphan'; },
    (events) => { events[2].is_error = true; },
    (events) => { events[2].subtype = 'error_max_budget_usd'; },
    (events) => { events[0].message.content[1].name = 'Bash'; },
  ]) {
    const events = claudeEvents(); mutate(events);
    assert.equal(summarize('claude-code', jsonl(events)).success, false);
  }
  assert.equal(summarize('claude-code', 'not-json\nZTAPI_CLIENT_OK').success, false);
});

test('failures distinguish sandbox/client environment from gateway responses and timeout', () => {
  const classify = need('classifyFailure');
  for (const [result, expected] of [
    [{ timedOut: true }, 'timeout'], [{ errorCode: 'ENOENT' }, 'environment_client_unavailable'],
    [{ stderr: 'Windows sandbox is not configured' }, 'environment_sandbox_unavailable'],
    [{ stderr: 'sandbox setup failed' }, 'environment_sandbox_unavailable'],
    [{ stderr: '401 authentication failed' }, 'gateway_authentication'],
    [{ stderr: '429 rate limit' }, 'gateway_rate_limit'],
    [{ stderr: '503 upstream unavailable' }, 'gateway_provider_error'],
    [{ stderr: 'HTTP 502 Bad Gateway before listener provenance is known' }, 'client_transport_unverified'],
    [{ stderr: 'ECONNREFUSED' }, 'environment_bridge_unavailable'],
    [{ exitCode: 1, stderr: 'other' }, 'client_failure'],
  ]) assert.equal(classify(result), expected);
});

test('read-only writes and approval-policy rejection are environment sandbox failures', () => {
  const classify = need('classifyFailure');
  for (const result of [
    { exitCode: 1, stderr: 'patch rejected: writing is blocked by read-only sandbox' },
    { exitCode: 1, stderr: 'patch rejected: rejected by user approval settings' },
    { exitCode: 1, stdout: JSON.stringify({ type: 'error', message:
      'patch rejected: writing is blocked by read-only sandbox; rejected by user approval settings' }) },
  ]) assert.equal(classify(result), 'environment_sandbox');
});

async function harness(t, client = 'codex') {
  const root = await temporary(t);
  const project = path.join(root, 'project');
  await mkdir(project);
  await writeFile(path.join(project, 'fixture.txt'), 'fixture data\n');
  const readiness = path.join(root, 'ready.json');
  await writeFile(readiness, JSON.stringify(ready()));
  const options = { client, ready: readiness, outputDir: path.join(root, 'evidence') };
  const calls = [];
  const catalogs = [];
  const runProcess = async (command, args, processOptions) => {
    calls.push({ command, args, ...processOptions });
    if (args.includes('-V')) return { exitCode: 0, stdout: client === 'codex'
      ? 'codex-cli 0.165.0-alpha.1.2\n' : '2.1.312 (Claude Code)\n', stderr: '', runtimeMs: 1 };
    if (args.includes('--help')) return { exitCode: 0, stdout: (client === 'codex'
      ? '--ignore-user-config --ignore-rules --ephemeral --skip-git-repo-check --json --color --sandbox --config'
      : '--bare --safe-mode --print --output-format --verbose --tools --allowedTools --no-session-persistence --strict-mcp-config --mcp-config --setting-sources --max-budget-usd --model'),
    stderr: '', runtimeMs: 1 };
    const catalogConfig = args.find((arg) => arg.startsWith('model_catalog_json='));
    if (catalogConfig) {
      const filename = JSON.parse(catalogConfig.slice('model_catalog_json='.length));
      const bytes = await readFile(filename);
      catalogs.push({ filename, bytes, data: JSON.parse(bytes), mode: (await stat(filename)).mode });
    }
    await writeFile(path.join(project, 'client-result.txt'), 'ZTAPI_CLIENT_FILE_OK\n');
    return { exitCode: 0, stdout: jsonl(client === 'codex' ? codexEvents() : [
      { type: 'system', subtype: 'init', tools: ['Read', 'Write'], claude_code_version: '2.1.312' }, ...claudeEvents(),
    ]),
      stderr: `redact ${key} Bearer other-credential`, runtimeMs: 2 };
  };
  const identityChecks = [];
  const dependencies = { resolveClient: async () => ({ command: process.execPath, prefixArgs: [] }), runProcess,
    verifyBridge: async (value) => { identityChecks.push(value); } };
  return { root, project, options, calls, dependencies, identityChecks, catalogs };
}

for (const client of ['codex', 'claude-code']) {
  test(`orchestration verifies ${client} version/help before tools, redacts evidence and removes only owned home`, async (t) => {
    const h = await harness(t, client);
    const receipt = await need('runSmoke')(h.options, h.dependencies);
    assert.equal(receipt.status, 'passed');
    assert.equal(receipt.provider_kind, 'simulated');
    assert.equal(receipt.paid_calls, 0);
    assert.equal(receipt.exit_code, 0);
    assert.equal(receipt.evidence_access, 'owner_only');
    assert.equal(receipt.client_version, client === 'codex' ? '0.165.0-alpha.1.2' : '2.1.312');
    assert.ok(receipt.runtime_ms >= 0);
    assert.equal(receipt.artifact.sha256, createHash('sha256').update('ZTAPI_CLIENT_FILE_OK\n').digest('hex'));
    assert.equal(h.calls.length, 3);
    assert.ok(h.calls[0].args.includes('-V'));
    assert.ok(h.calls[1].args.includes('--help'));
    assert.equal(h.calls[2].cwd, h.project);
    assert.ok(h.calls.every((call) => call.timeoutMs > 0 && call.timeoutMs <= 180000));
    assert.ok(h.calls.every((call) => !JSON.stringify(call.args).includes(key)));
    assert.ok(h.calls.every((call) => call.env.NO_PROXY === '127.0.0.1,localhost,::1'
      && call.env.no_proxy === '127.0.0.1,localhost,::1'));
    if (client === 'claude-code') {
      assert.ok(h.calls[1].args.includes('--safe-mode'));
      assert.ok(!h.calls[1].args.includes('--bare'));
      assert.equal(receipt.actual_events.declared_tools_valid, true);
      assert.deepEqual(receipt.actual_events.declared_tools, ['Read', 'Write']);
      assert.equal(receipt.model_catalog, null);
      assert.equal(h.catalogs.length, 0);
    } else {
      assert.equal(h.catalogs.length, 1);
      const catalog = h.catalogs[0];
      assert.equal(path.isAbsolute(catalog.filename), true);
      assert.equal(path.dirname(catalog.filename), h.calls[2].env.CODEX_HOME);
      assert.equal(catalog.data.models[0].slug, ready().model);
      assert.equal(catalog.data.models[0].shell_type, 'unified_exec');
      assert.equal(catalog.data.models[0].apply_patch_tool_type, 'freeform');
      assert.deepEqual(catalog.data.models[0].input_modalities, ['text']);
      assert.equal(receipt.model_catalog.source, 'simulated_fixture_only');
      assert.equal(receipt.model_catalog.sha256, createHash('sha256').update(catalog.bytes).digest('hex'));
      assert.ok(!catalog.bytes.toString().includes(key));
      if (process.platform !== 'win32') assert.equal(catalog.mode & 0o777, 0o600);
      await assert.rejects(stat(catalog.filename), { code: 'ENOENT' });
    }
    assert.ok(!Object.values(h.calls[0].env).includes(key));
    assert.ok(!Object.values(h.calls[1].env).includes(key));
    const receiptText = await readFile(receipt.receipt_path, 'utf8');
    assert.ok(!receiptText.includes(key));
    const stderr = await readFile(receipt.logs.stderr, 'utf8');
    assert.ok(!stderr.includes(key));
    assert.ok(!stderr.includes('other-credential'));
    await assert.rejects(stat(h.calls[2].env.HOME), { code: 'ENOENT' });
    assert.equal(await readFile(path.join(h.project, 'fixture.txt'), 'utf8'), 'fixture data\n');
    if (process.platform !== 'win32') assert.equal((await stat(receipt.logs.stdout)).mode & 0o777, 0o600);
  });
}

test('fail closed before spawning on invalid readiness, missing fixture, existing artifact or project aliases', async (t) => {
  const run = need('runSmoke');
  for (const invalid of ['live', 'missing-fixture', 'old-artifact', 'project-alias']) {
    const h = await harness(t);
    if (invalid === 'live') await writeFile(h.options.ready, JSON.stringify(ready({ provider_kind: 'live' })));
    if (invalid === 'missing-fixture') await rm(path.join(h.project, 'fixture.txt'));
    if (invalid === 'old-artifact') await writeFile(path.join(h.project, 'client-result.txt'), 'ZTAPI_CLIENT_FILE_OK');
    if (invalid === 'project-alias') {
      await rm(h.project, { recursive: true });
      const external = path.join(h.root, 'external'); await mkdir(external);
      await writeFile(path.join(external, 'fixture.txt'), 'fixture');
      await symlink(external, h.project, process.platform === 'win32' ? 'junction' : 'dir');
    }
    await assert.rejects(run(h.options, h.dependencies), /readiness|fixture|artifact|project/);
    assert.equal(h.calls.length, 0);
  }
});

test('wrong installed version and unsupported flags never advance to inference', async (t) => {
  const run = need('runSmoke');
  for (const stage of ['version', 'help']) {
    const h = await harness(t);
    const actual = h.dependencies.runProcess;
    h.dependencies.runProcess = async (...args) => {
      if ((stage === 'version' && args[1].includes('-V')) || (stage === 'help' && args[1].includes('--help'))) {
        h.calls.push({ args: args[1] });
        return { exitCode: 0, stdout: stage === 'version' ? 'codex-cli 99.0.0' : 'no flags', stderr: '' };
      }
      return actual(...args);
    };
    const receipt = await run(h.options, h.dependencies);
    assert.equal(receipt.status, 'failed');
    assert.match(receipt.error.classification, /environment_client_version|environment_client_flags/);
    assert.ok(!h.calls.some((call) => call.input));
  }
});

test('success text and file alone cannot qualify tools; sandbox errors are not gateway failures', async (t) => {
  const run = need('runSmoke');
  for (const kind of ['no-tools', 'sandbox', 'timeout', 'nonzero', 'fixture-change']) {
    const h = await harness(t);
    const actual = h.dependencies.runProcess;
    h.dependencies.runProcess = async (...args) => {
      if (args[1].includes('-V') || args[1].includes('--help')) return actual(...args);
      const result = await actual(...args);
      if (kind === 'no-tools') result.stdout = jsonl(codexEvents().slice(2));
      if (kind === 'sandbox') { result.exitCode = 1; result.stderr = 'Windows sandbox is not configured'; }
      if (kind === 'timeout') result.timedOut = true;
      if (kind === 'nonzero') result.exitCode = 3;
      if (kind === 'fixture-change') await writeFile(path.join(h.project, 'fixture.txt'), 'changed');
      return result;
    };
    const receipt = await run(h.options, h.dependencies);
    assert.equal(receipt.status, 'failed', kind);
    assert.equal(receipt.error.classification, ({ 'no-tools': 'client_evidence_incomplete',
      sandbox: 'environment_sandbox_unavailable', timeout: 'timeout', nonzero: 'client_failure',
      'fixture-change': 'client_fixture_modified' })[kind]);
  }
});

test('process execution is shell-free and redacts only after complete chunks; timeout kills its own child', async (t) => {
  const run = need('runProcess');
  const root = await temporary(t);
  const result = await run(process.execPath, ['-e', 'process.stdout.write("chunk");process.stderr.write("error")'],
    { cwd: root, env: { ...need('buildChildEnvironment')('codex', need('validateReady')(ready()), root, process.env) },
      timeoutMs: 10000, input: '' });
  assert.equal(result.exitCode, 0);
  assert.equal(result.stdout, 'chunk');
  assert.equal(result.stderr, 'error');
  const timeout = await run(process.execPath, ['-e', 'setInterval(()=>{},1000)'],
    { cwd: root, env: {}, timeoutMs: 150, input: '' });
  assert.equal(timeout.timedOut, true);
  assert.ok(timeout.runtimeMs < 10000);
});

test('CLI invalid arguments print a static nonsecret diagnostic and do not launch a client', () => {
  const result = spawnSync(process.execPath, [fileURLToPath(runnerUrl),
    '--api-key', key], { encoding: 'utf8', timeout: 10000 });
  assert.notEqual(result.status, 0);
  assert.ok(!(result.stdout + result.stderr).includes(key));
  assert.match(result.stderr, /arguments/);
});

test('artifact hashes cover the original bytes, not UTF-8 replacement characters', async (t) => {
  const h = await harness(t);
  const actual = h.dependencies.runProcess;
  const bytes = Buffer.concat([Buffer.from('ZTAPI_CLIENT_FILE_OK\n'), Buffer.from([0x80, 0xff])]);
  h.dependencies.runProcess = async (...args) => {
    const result = await actual(...args);
    if (args[2].input) await writeFile(path.join(h.project, 'client-result.txt'), bytes);
    return result;
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'passed');
  assert.equal(receipt.artifact.sha256, createHash('sha256').update(bytes).digest('hex'));
  assert.equal(receipt.artifact.bytes, bytes.length);
});

test('readiness/output paths fail closed on aliases and oversized files before client discovery', async (t) => {
  const run = need('runSmoke');
  for (const kind of ['huge-ready', 'output-alias', 'fixture-alias']) {
    const h = await harness(t);
    if (kind === 'huge-ready') await writeFile(h.options.ready, ' '.repeat(65537));
    if (kind === 'output-alias') {
      const target = path.join(h.root, 'real-output'); await mkdir(target);
      await symlink(target, h.options.outputDir, process.platform === 'win32' ? 'junction' : 'dir');
    }
    if (kind === 'fixture-alias') {
      // A directory junction is also an invalid regular file and does not require Windows symlink privileges.
      await rm(path.join(h.project, 'fixture.txt'));
      await symlink(h.root, path.join(h.project, 'fixture.txt'), process.platform === 'win32' ? 'junction' : 'dir');
    }
    await assert.rejects(run(h.options, h.dependencies), /readiness|output|fixture/);
    assert.equal(h.calls.length, 0);
  }
});

test('artifact and tool-result failures never qualify, including client-controlled secret IDs', async (t) => {
  const run = need('runSmoke');
  for (const kind of ['no-artifact', 'wrong-marker', 'secret-artifact', 'failed-command', 'secret-id']) {
    const h = await harness(t);
    const actual = h.dependencies.runProcess;
    h.dependencies.runProcess = async (...args) => {
      const result = await actual(...args);
      if (!args[2].input) return result;
      if (kind === 'no-artifact') await rm(path.join(h.project, 'client-result.txt'));
      if (kind === 'wrong-marker') await writeFile(path.join(h.project, 'client-result.txt'), 'wrong');
      if (kind === 'secret-artifact') await writeFile(path.join(h.project, 'client-result.txt'), `ZTAPI_CLIENT_FILE_OK\n${key}`);
      if (kind === 'failed-command') { const events = codexEvents(); events[0].item.exit_code = 1; result.stdout = jsonl(events); }
      if (kind === 'secret-id') { const events = codexEvents(); events[0].item.id = key; result.stdout = jsonl(events); }
      return result;
    };
    const receipt = await run(h.options, h.dependencies);
    assert.equal(receipt.status, kind === 'secret-id' ? 'passed' : 'failed', kind);
    assert.ok(!JSON.stringify(receipt).includes(key));
    assert.ok(!(await readFile(receipt.logs.stdout, 'utf8')).includes(key));
  }
});

test('client discovery and spawn errors create nonsecret environment failure receipts', async (t) => {
  const run = need('runSmoke');
  const missing = await harness(t);
  missing.dependencies.resolveClient = async () => { const error = new Error(key); error.code = 'environment_client_installation'; throw error; };
  const discoveryReceipt = await run(missing.options, missing.dependencies);
  assert.equal(discoveryReceipt.error.classification, 'environment_client_installation');
  assert.ok(!JSON.stringify(discoveryReceipt).includes(key));
  const spawn = await harness(t);
  spawn.dependencies.runProcess = async () => ({ exitCode: null, errorCode: 'ENOENT', stdout: '', stderr: '' });
  const spawnReceipt = await run(spawn.options, spawn.dependencies);
  assert.equal(spawnReceipt.error.classification, 'environment_client_unavailable');
});

test('provider errors, budget stops and malformed streams are summarized without invented tool success', () => {
  const summarize = need('summarizeEvents');
  for (const subtype of ['error_during_execution', 'error_max_turns', 'error_max_budget_usd']) {
    const events = claudeEvents(); events[2] = { type: 'result', subtype, is_error: true, errors: ['503'] };
    assert.equal(summarize('claude-code', jsonl(events)).success, false);
  }
  const partial = summarize('codex', '{"type":"item.completed"\n');
  assert.equal(partial.success, false);
  assert.equal(partial.unparsed_lines, 1);
});

test('process capture is bounded and missing executables do not reject with raw diagnostics', async (t) => {
  const run = need('runProcess');
  const root = await temporary(t);
  const options = { cwd: root, env: need('buildChildEnvironment')('codex', ready(), root, process.env), timeoutMs: 10000 };
  const overflow = await run(process.execPath, ['-e', 'process.stdout.write("x".repeat(5*1024*1024));setInterval(()=>{},1000)'], options);
  assert.equal(overflow.outputLimit, true);
  assert.ok(Buffer.byteLength(overflow.stdout) <= 4 * 1024 * 1024);
  const missing = await run(path.join(root, 'nonexistent-client.exe'), [], options);
  assert.equal(missing.errorCode, 'ENOENT');
});

test('model metadata generic fallback is a warning, not a capability certification', async (t) => {
  const h = await harness(t);
  const actual = h.dependencies.runProcess;
  h.dependencies.runProcess = async (...args) => {
    const result = await actual(...args);
    if (args[2].input) result.stderr = 'Unknown model metadata; generic model fallback';
    return result;
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.deepEqual(receipt.warnings, ['model_metadata_generic_fallback_unverified']);
  assert.equal(receipt.dispatch_count, null);
  assert.equal(receipt.dispatch_count_source, 'bridge_receipt_required');
});

test('Codex rejects ancestor project configuration instead of importing external hooks/MCP settings', async (t) => {
  const h = await harness(t);
  await mkdir(path.join(h.root, '.codex'));
  await writeFile(path.join(h.root, '.codex', 'config.toml'), 'notify=["external-command"]\n');
  await assert.rejects(need('runSmoke')(h.options, h.dependencies), /environment_project_config/);
  assert.equal(h.calls.length, 0);
});

test('deadline terminates only the spawned child tree, including its spawned descendant', async (t) => {
  const run = need('runProcess');
  const root = await temporary(t);
  const childProgram = 'const{spawn}=require("node:child_process");const c=spawn(process.execPath,["-e","setInterval(()=>{},1000)"],{stdio:"ignore"});console.log(c.pid);setInterval(()=>{},1000)';
  const result = await run(process.execPath, ['-e', childProgram], {
    cwd: root, env: need('buildChildEnvironment')('codex', ready(), root, process.env), timeoutMs: 1000,
  });
  const descendant = Number(result.stdout.trim());
  assert.ok(Number.isSafeInteger(descendant) && descendant > 0);
  assert.equal(result.timedOut, true);
  assert.throws(() => process.kill(descendant, 0), { code: 'ESRCH' });
});

test('shared simulated-bridge identity verification receives nonce/PID and gates every inference', async (t) => {
  const h = await harness(t);
  const actual = h.dependencies.runProcess;
  h.dependencies.runProcess = async (...args) => {
    assert.equal(h.identityChecks.length, 1, 'identity must be verified before any client spawn');
    return actual(...args);
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'passed');
  assert.equal(receipt.bridge_identity, 'verified');
  assert.equal(h.identityChecks[0].pid, 1234);
  assert.equal(h.identityChecks[0].run_nonce, ready().run_nonce);
  assert.equal(h.identityChecks[0].api_key, key);
});

test('an unverified loopback service or SSH tunnel cannot launch CLI inference', async (t) => {
  const h = await harness(t);
  h.dependencies.verifyBridge = async () => { throw new Error(`identity mismatch ${key}`); };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'failed');
  assert.equal(receipt.error.classification, 'environment_bridge_identity_unverified');
  assert.equal(receipt.exit_code, null);
  assert.equal(h.calls.length, 0);
  assert.ok(!JSON.stringify(receipt).includes(key));
  assert.equal(receipt.provider_kind, 'simulated');
  assert.equal(receipt.paid_calls, 0);
});

test('default shared identity guard is wired without any external network requests', async (t) => {
  const h = await harness(t);
  delete h.dependencies.verifyBridge;
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({ url: String(url), options });
    return new Response(JSON.stringify({ provider_kind: 'simulated', pid: ready().pid, run_nonce: ready().run_nonce }),
      { headers: { 'content-type': 'application/json' } });
  });
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'passed');
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, 'http://127.0.0.1:19091/__ztapi_client_bridge');
  assert.equal(requests[0].options.method, 'GET');
  assert.equal(requests[0].options.redirect, 'error');
});

test('Codex execution proof requires a parsed zero-exit command event with string aggregated output', () => {
  const summarize = need('summarizeEvents');
  for (const mutate of [
    (events) => { events[0].item.aggregated_output = ['ZTAPI_CLIENT_EXEC_OK']; },
    (events) => { events[0].item.aggregated_output = ''; },
    (events) => { events[0].item.exit_code = 1; },
    (events) => { events[0].item.exit_code = '0'; },
    (events) => { delete events[0].item.exit_code; },
    (events) => { events[0].item.type = 'shell_execution'; },
    (events) => { events[0].type = 'item.started'; },
    (events) => { events[0].item.status = 'in_progress'; },
  ]) {
    const events = codexEvents();
    mutate(events);
    const output = `${jsonl(events)}ZTAPI_CLIENT_EXEC_OK\nZTAPI_CLIENT_OK\n`;
    assert.equal(summarize('codex', output).success, false,
      'global stdout/final text cannot replace a recognized successful command event');
  }
});

test('Claude requires matched nonerror Read/Write results and refuses unknown error-field types', () => {
  const summarize = need('summarizeEvents');
  for (const errorValue of [true, 'false', 'true', 0, null]) {
    const events = claudeEvents();
    events[1].message.content[1].is_error = errorValue;
    assert.equal(summarize('claude-code', jsonl(events)).success, false);
  }
  for (const mutate of [
    (events) => { events.splice(1, 1); },
    (events) => { events[1].message.content[1].tool_use_id = 'unmatched-write'; },
    (events) => { events[1].type = 'assistant'; },
  ]) {
    const events = claudeEvents(); mutate(events);
    assert.equal(summarize('claude-code', jsonl(events)).success, false);
  }
  const explicitSuccess = claudeEvents();
  for (const block of explicitSuccess[1].message.content) block.is_error = false;
  assert.equal(summarize('claude-code', jsonl(explicitSuccess)).success, true);
});

for (const client of ['codex', 'claude-code']) {
  test(`${client} cannot pass with valid tool events but no physical artifact, or with artifact but unknown events`, async (t) => {
    for (const scenario of ['missing-artifact', 'unknown-events']) {
      const h = await harness(t, client);
      const actual = h.dependencies.runProcess;
      h.dependencies.runProcess = async (...args) => {
        const result = await actual(...args);
        if (!args[2].input) return result;
        if (scenario === 'missing-artifact') await rm(path.join(h.project, 'client-result.txt'));
        else result.stdout = jsonl([{ type: 'unknown-client-schema', text: 'ZTAPI_CLIENT_EXEC_OK ZTAPI_CLIENT_OK' }]);
        return result;
      };
      const receipt = await need('runSmoke')(h.options, h.dependencies);
      assert.equal(receipt.status, 'failed');
      assert.equal(receipt.error.classification,
        scenario === 'missing-artifact' ? 'client_artifact_missing' : client === 'claude-code' ? 'environment_client_tools_unavailable' : 'client_evidence_incomplete');
      if (scenario === 'unknown-events' && client === 'codex') assert.match(receipt.artifact.sha256, /^[a-f0-9]{64}$/);
    }
  });
}

test('Claude init must actually declare exactly Read and Write, not merely request them in argv', () => {
  const summarize = need('summarizeEvents');
  for (const tools of [['Read'], ['Write'], [], ['Read', 'Write', 'Bash'], ['Read', 'Read'], null, 'Read,Write']) {
    const events = [{ type: 'system', subtype: 'init', tools }, ...claudeEvents()];
    const summary = summarize('claude-code', jsonl(events));
    assert.equal(summary.success, false, JSON.stringify(tools));
    assert.equal(summary.declared_tools_valid, false);
  }
  const good = summarize('claude-code', jsonl([
    { type: 'system', subtype: 'init', tools: ['Write', 'Read'] }, ...claudeEvents(),
  ]));
  assert.equal(good.success, true);
  assert.equal(good.declared_tools_valid, true);
});

test('Claude tool and final events without init are incomplete evidence', () => {
  const summary=need('summarizeEvents')('claude-code',jsonl(claudeEvents().filter(e=>e.type!=='system')));
  assert.equal(summary.success,false);
});

test('Claude Read-only init reproduces the real failure and is a client tool-profile error, not a relay outage', async (t) => {
  const h = await harness(t, 'claude-code');
  const actual = h.dependencies.runProcess;
  h.dependencies.runProcess = async (...args) => {
    if (!args[2].input) return actual(...args);
    h.calls.push({ args: args[1], ...args[2] });
    return { exitCode: 1, stdout: jsonl([
      { type: 'system', subtype: 'init', tools: ['Read'], claude_code_version: '2.1.312' },
      { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call_bridge_1', name: 'Read',
        input: { file_path: 'fixture.txt' } }] } },
      { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'call_bridge_1', content: 'fixture' }] } },
      { type: 'result', subtype: 'success', is_error: true, result: 'bridge_tool_shape' },
    ]), stderr: 'ECONNREFUSED after bridge_tool_shape retries', runtimeMs: 1 };
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'failed');
  assert.equal(receipt.error.classification, 'environment_client_tools_unavailable');
  assert.equal(receipt.actual_events.successful_tool_events, 1);
  assert.deepEqual(receipt.actual_events.declared_tools, ['Read']);
  assert.equal(receipt.actual_events.declared_tools_valid, false);
  assert.equal(receipt.artifact, null);
});

test('Codex HTTP 502 remains unverified transport evidence without asserting a gateway/provider failure', async (t) => {
  const h = await harness(t);
  const actual = h.dependencies.runProcess;
  h.dependencies.runProcess = async (...args) => {
    if (!args[2].input) return actual(...args);
    h.calls.push({ args: args[1], ...args[2] });
    return { exitCode: 1, stdout: '', stderr: 'HTTP 502 Bad Gateway', runtimeMs: 1 };
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'failed');
  assert.equal(receipt.error.classification, 'client_transport_unverified');
  assert.equal(receipt.dispatch_count, null);
  assert.equal(receipt.provider_kind, 'simulated');
  assert.equal(receipt.paid_calls, 0);
});

test('Codex fixture catalog contains one simulated text/tool model without importing identity, hooks or credentials', () => {
  const build = need('buildFixtureModelCatalog');
  const injected = ready({
    identity: { account_id: 'user-identity-must-not-copy' }, hooks: { command: 'user-hook-must-not-copy' },
    plugins: ['user-plugin-must-not-copy'], mcp_servers: { user: { token: 'user-mcp-secret' } },
    model_catalog_json: path.resolve('not-a-user-profile', 'never-read-models-cache.json'),
    env: { OPENAI_API_KEY: 'real-provider-secret' },
  });
  const catalog = build(injected);
  assert.deepEqual(catalog, build(ready()));
  assert.deepEqual(Object.keys(catalog), ['models']);
  assert.equal(catalog.models.length, 1);
  const model = catalog.models[0];
  assert.equal(model.slug, ready().model);
  assert.equal(model.shell_type, 'unified_exec');
  assert.equal(model.apply_patch_tool_type, 'freeform');
  assert.deepEqual(model.input_modalities, ['text']);
  assert.equal(model.supports_search_tool, false);
  assert.equal(model.supports_image_detail_original, false);
  assert.equal(model.node_repl_disabled, true);
  assert.deepEqual(model.experimental_supported_tools, []);
  assert.equal(model.include_skills_usage_instructions, false);
  assert.equal(model.include_plugin_usage_instructions, false);
  assert.equal(model.include_apps_usage_instructions, false);
  assert.deepEqual(model.supported_reasoning_levels, []);
  assert.equal(model.default_reasoning_level, null);
  assert.equal(model.support_verbosity, false);
  assert.deepEqual(model.truncation_policy, { mode: 'tokens', limit: 8192 });
  assert.match(model.description, /simulated|scripted/i);
  assert.match(model.base_instructions, /synthetic project/i);
  for (const field of ['identity', 'hooks', 'plugins', 'mcp_servers', 'env', 'model_catalog_json', 'comp_hash']) {
    assert.equal(model[field], undefined, field);
    assert.equal(catalog[field], undefined, field);
  }
  const serialized = JSON.stringify(catalog);
  for (const secret of [key, ready().run_nonce, ready().base_url, 'user-identity-must-not-copy',
    'user-hook-must-not-copy', 'user-plugin-must-not-copy', 'user-mcp-secret', 'real-provider-secret',
    'never-read-models-cache']) assert.ok(!serialized.includes(secret), secret);
  assert.throws(() => build(ready({ provider_kind: 'live' })), /readiness/);
  assert.throws(() => build(ready({ model: 'real-model-alias' })), /readiness/);
});

test('Codex requires an explicit absolute fixture catalog path and escapes it as a TOML string', () => {
  const build = need('buildInvocation');
  for (const filename of [undefined, '', 'relative-models.json']) {
    assert.throws(() => build('codex', ready(), path.resolve('project'), filename), /catalog_path/);
  }
  const filename = path.resolve('owned output with spaces', 'codex', 'fixture-models.json');
  const invocation = build('codex', ready(), path.resolve('project'), filename);
  const entry = invocation.args.find((arg) => arg.startsWith('model_catalog_json='));
  assert.equal(JSON.parse(entry.slice('model_catalog_json='.length)), filename);
  assert.ok(!invocation.args.some((arg) => arg.includes('model_catalog_url')));
  assert.ok(!JSON.stringify(invocation.args).includes(key));
  const claude = build('claude-code', ready(), path.resolve('project'));
  assert.ok(!claude.args.some((arg) => arg.includes('model_catalog')));
});

test('Codex catalog is generated only after identity/version/help validation, never from caller metadata', async (t) => {
  const h = await harness(t);
  await writeFile(h.options.ready, JSON.stringify(ready({
    model_catalog_json: path.join(h.root, 'external-user-catalog-that-must-not-be-opened.json'),
    hooks: { command: 'do-not-import-hooks' }, identity: { token: 'do-not-import-identity' },
  })));
  const actual = h.dependencies.runProcess;
  h.dependencies.runProcess = async (...args) => {
    if (args[1].includes('-V') || args[1].includes('--help')) {
      await assert.rejects(stat(path.join(args[2].env.CODEX_HOME, 'fixture-models.json')), { code: 'ENOENT' });
    }
    return actual(...args);
  };
  const receipt = await need('runSmoke')(h.options, h.dependencies);
  assert.equal(receipt.status, 'passed');
  assert.equal(h.identityChecks.length, 1);
  assert.equal(h.catalogs.length, 1);
  assert.equal(path.basename(h.catalogs[0].filename), 'fixture-models.json');
  assert.ok(h.catalogs[0].filename.startsWith(`${h.options.outputDir}${path.sep}`));
  assert.ok(!h.catalogs[0].bytes.toString().includes('do-not-import'));
  assert.deepEqual(h.catalogs[0].data, need('buildFixtureModelCatalog')(ready()));
});
