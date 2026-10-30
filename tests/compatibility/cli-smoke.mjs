import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { chmod, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateSimulatedBridgeConfig, verifySimulatedBridge } from './client-bridge.mjs';

const CLIENTS = Object.freeze({
  codex: { version: '0.165.0-alpha.1.2',
    executable: 'C:/Users/Administrator/AppData/Local/OpenAI/Codex/bin/0ab0e7a8f3779036/codex.exe' },
  'claude-code': { version: '2.1.312', shim: 'C:/Users/Administrator/AppData/Roaming/npm/claude.ps1' },
});
const CORE_ENV = new Set(['PATH', 'SYSTEMROOT', 'WINDIR', 'COMSPEC', 'PATHEXT', 'SYSTEMDRIVE']);
const TIMEOUT_MS = 180000;
const OUTPUT_LIMIT = 4 * 1024 * 1024;
const FILE_LIMIT = 64 * 1024;
const FILE_MARKER = 'ZTAPI_CLIENT_FILE_OK';
const EXEC_MARKER = 'ZTAPI_CLIENT_EXEC_OK';
const FINAL_MARKER = 'ZTAPI_CLIENT_OK';
const LOOPBACK_NO_PROXY = '127.0.0.1,localhost,::1';

function fail(code) { const error = new Error(code); error.code = code; throw error; }
function clientKind(client) { if (!Object.hasOwn(CLIENTS, client)) fail('invalid_arguments'); }
function samePath(a, b) {
  const normalize = (value) => process.platform === 'win32' ? path.resolve(value).toLowerCase() : path.resolve(value);
  return normalize(a) === normalize(b);
}
function inside(parent, child) {
  const relative = path.relative(parent, child);
  return relative !== '' && !relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative);
}

export function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 2) {
    const name = argv[i];
    if (!['--client', '--ready', '--output-dir'].includes(name) || Object.hasOwn(values, name)
      || typeof argv[i + 1] !== 'string' || argv[i + 1].startsWith('--')) fail('invalid_arguments');
    values[name] = argv[i + 1];
  }
  clientKind(values['--client']);
  if (!values['--ready'] || !values['--output-dir'] || !path.isAbsolute(values['--ready'])
    || !path.isAbsolute(values['--output-dir'])) fail('invalid_arguments');
  return { client: values['--client'], ready: values['--ready'], outputDir: values['--output-dir'] };
}

export function validateReady(value) {
  if (!value || Array.isArray(value) || typeof value !== 'object'
    || value.provider_kind !== 'simulated' || value.model !== 'zt-claude-sonnet-5'
    || !Number.isSafeInteger(value.pid) || value.pid <= 0
    || typeof value.api_key !== 'string' || !/^[\x21-\x7e]{8,4096}$/.test(value.api_key)
    || typeof value.base_url !== 'string') fail('invalid_readiness');
  // Check the original spelling, not URL's normalization of alternate IP encodings.
  const match = /^http:\/\/(127\.0\.0\.1|localhost|\[::1\]):([1-9][0-9]{0,4})\/?$/.exec(value.base_url);
  if (!match || Number(match[2]) > 65535) fail('invalid_readiness_endpoint');
  try { validateSimulatedBridgeConfig(value); } catch { fail('invalid_readiness'); }
  return { base_url: value.base_url.replace(/\/$/, ''), api_key: value.api_key,
    model: value.model, pid: value.pid, run_nonce: value.run_nonce, provider_kind: 'simulated' };
}

function coreEnvironment(inherited) {
  const env = {};
  const seen = new Set();
  for (const [name, value] of Object.entries(inherited)) {
    const upper = name.toUpperCase();
    if (CORE_ENV.has(upper) && typeof value === 'string' && !seen.has(upper)) {
      env[name] = value; seen.add(upper);
    }
  }
  return env;
}

export function buildChildEnvironment(client, ready, ownedRoot, inherited = process.env) {
  clientKind(client);
  if (!path.isAbsolute(ownedRoot)) fail('invalid_arguments');
  const env = coreEnvironment(inherited);
  Object.assign(env, {
    HOME: path.join(ownedRoot, 'home'), USERPROFILE: path.join(ownedRoot, 'home'),
    CODEX_HOME: path.join(ownedRoot, 'codex'), CLAUDE_CONFIG_DIR: path.join(ownedRoot, 'claude'),
    TEMP: path.join(ownedRoot, 'tmp'), TMP: path.join(ownedRoot, 'tmp'),
    APPDATA: path.join(ownedRoot, 'appdata'), LOCALAPPDATA: path.join(ownedRoot, 'localappdata'),
    NO_PROXY: LOOPBACK_NO_PROXY, no_proxy: LOOPBACK_NO_PROXY,
  });
  if (client === 'codex') env.ZTAPI_ACCEPTANCE_KEY = ready.api_key;
  else Object.assign(env, { ANTHROPIC_API_KEY: ready.api_key, ANTHROPIC_BASE_URL: ready.base_url,
    CLAUDE_CODE_SAFE_MODE: '1',
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_AUTOUPDATER: '1',
    DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1' });
  return env;
}

export function buildFixtureModelCatalog(ready) {
  const { model } = validateReady(ready);
  // Schema checked with 0.165.0-alpha.1.2; these are scripted-fixture settings,
  // not copied provider defaults or a claim about any live model's abilities.
  return { models: [{
    slug: model,
    display_name: 'ZTAPI simulated CLI fixture',
    description: 'Local scripted acceptance only; not live model capability evidence.',
    default_reasoning_level: null,
    supported_reasoning_levels: [],
    shell_type: 'unified_exec',
    visibility: 'hide',
    supported_in_api: true,
    priority: 0,
    base_instructions: 'You are a simulated client acceptance fixture. Use only local exec_command '
      + 'and apply_patch tools to follow the supplied synthetic project prompt. Do not access '
      + 'network, credentials, user profiles, MCP, plugins, search or media tools.',
    apply_patch_tool_type: 'freeform',
    input_modalities: ['text'],
    supports_image_detail_original: false,
    supports_search_tool: false,
    support_verbosity: false,
    truncation_policy: { mode: 'tokens', limit: 8192 },
    context_window: 32768,
    max_context_window: 32768,
    include_skills_usage_instructions: false,
    include_plugin_usage_instructions: false,
    include_apps_usage_instructions: false,
    additional_speed_tiers: [],
    service_tiers: [],
    experimental_supported_tools: [],
    node_repl_disabled: true,
  }] };
}

export function buildInvocation(client, ready, project, catalogPath) {
  clientKind(client);
  const input = `In this synthetic project only, read fixture.txt using an actual local tool.\n`
    + `Create client-result.txt containing exactly ${FILE_MARKER} followed by a newline, using an actual local tool.\n`
    + (client === 'codex' ? `Then execute a shell command: echo ${EXEC_MARKER}\n` : '')
    + `Do not access any other files, services, credentials, or network. Finally reply exactly ${FINAL_MARKER}.\n`;
  // 2.1.312 bare/simple mode filters out Write; safe mode preserves built-in tools.
  if (client === 'claude-code') return { input, args: [
    '--safe-mode', '--print', '--output-format', 'stream-json', '--verbose',
    '--tools', 'Read,Write', '--allowedTools', 'Read,Write', '--no-session-persistence',
    '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}', '--setting-sources', '',
    '--max-budget-usd', '0.8', '--model', ready.model,
  ] };
  if (typeof catalogPath !== 'string' || !path.isAbsolute(catalogPath)) fail('invalid_catalog_path');
  const config = [
    'model_provider="ztapi_acceptance"', `model=${JSON.stringify(ready.model)}`,
    `model_catalog_json=${JSON.stringify(catalogPath)}`, 'features.api_key_model_discovery=false',
    'model_providers.ztapi_acceptance.name="ZTAPI simulated acceptance"',
    `model_providers.ztapi_acceptance.base_url=${JSON.stringify(`${ready.base_url}/v1`)}`,
    'model_providers.ztapi_acceptance.wire_api="responses"',
    'model_providers.ztapi_acceptance.env_key="ZTAPI_ACCEPTANCE_KEY"',
    'model_providers.ztapi_acceptance.requires_openai_auth=false',
    'model_providers.ztapi_acceptance.request_max_retries=0',
    'model_providers.ztapi_acceptance.stream_max_retries=0',
    'approval_policy="never"', 'web_search="disabled"', 'project_doc_max_bytes=0',
    'features.plugins=false', 'features.remote_plugin=false', 'features.hooks=false',
    'features.apps=false', 'mcp_servers={}',
    'shell_environment_policy.exclude=["ZTAPI_ACCEPTANCE_KEY"]',
  ];
  return { input, args: ['exec', '--ignore-user-config', '--ignore-rules', '--ephemeral',
    '--skip-git-repo-check', '--json', '--color', 'never', '-s', 'workspace-write',
    '-C', project, ...config.flatMap((entry) => ['-c', entry]), '-'] };
}

async function plainPath(file, kind, code) {
  let info;
  try {
    info = await lstat(file);
    if (info.isSymbolicLink() || !samePath(await realpath(file), file)
      || (kind === 'file' ? !info.isFile() : !info.isDirectory())) fail(code);
  } catch { fail(code); }
  return info;
}
async function boundedBytes(file, code) {
  const info = await plainPath(file, 'file', code);
  if (info.size > FILE_LIMIT) fail(code);
  const bytes = await readFile(file);
  if (bytes.length > FILE_LIMIT) fail(code);
  return bytes;
}
async function boundedRead(file, code) { return (await boundedBytes(file, code)).toString('utf8'); }

export async function resolveClaudeEntry(shim) {
  const shimText = await boundedRead(shim, 'environment_client_installation');
  const packageRoot = path.join(path.dirname(shim), 'node_modules', '@anthropic-ai', 'claude-code');
  let metadata;
  try { metadata = JSON.parse(await boundedRead(path.join(packageRoot, 'package.json'), 'environment_client_installation')); }
  catch { fail('environment_client_installation'); }
  const entry = metadata?.bin?.claude;
  if (metadata.name !== '@anthropic-ai/claude-code' || metadata.version !== CLIENTS['claude-code'].version
    || !['cli.js', 'bin/claude.exe'].includes(entry)
    || !shimText.replaceAll('\\', '/').includes(`node_modules/@anthropic-ai/claude-code/${entry}`)) {
    fail('environment_client_installation');
  }
  const entryPath = path.join(packageRoot, entry);
  await plainPath(entryPath, 'file', 'environment_client_installation');
  return { command: entry.endsWith('.js') ? process.execPath : entryPath,
    prefixArgs: entry.endsWith('.js') ? [entryPath] : [], entry_kind: entry.endsWith('.js') ? 'node' : 'native' };
}

async function resolveInstalledClient(client) {
  if (client === 'claude-code') return resolveClaudeEntry(CLIENTS[client].shim);
  await plainPath(CLIENTS.codex.executable, 'file', 'environment_client_installation');
  return { command: CLIENTS.codex.executable, prefixArgs: [], entry_kind: 'native' };
}

export function redact(value, secrets = []) {
  let output = String(value ?? '');
  for (const secret of secrets.filter((item) => typeof item === 'string' && item.length)) {
    for (const spelling of new Set([secret, JSON.stringify(secret).slice(1, -1), encodeURIComponent(secret)])) {
      output = output.replaceAll(spelling, '[REDACTED]');
    }
  }
  return output.replace(/\bBearer[ \t]+[^\s"'\\,;}]+/gi, 'Bearer [REDACTED]')
    .replace(/("(?:api[_-]?key|access[_-]?token|auth[_-]?token|authorization)"\s*:\s*")[^"\r\n]*(")/gi, '$1[REDACTED]$2')
    .replace(/\bsk-[a-z0-9_-]{8,}/gi, '[REDACTED]');
}

export function summarizeEvents(client, stdout) {
  const summary = { success: false, parsed_events: 0, unparsed_lines: 0, successful_tool_events: 0,
    tool_ids: [], tools: [], terminal_event: null, final_marker: false, exec_marker: false, failed_events: 0,
    declared_tools: null, declared_tools_valid: null };
  const pending = new Map();
  const completed = new Set();
  const successfulNames = new Set();
  for (const line of stdout.split(/\r?\n/).filter((item) => item.trim())) {
    let event;
    try { event = JSON.parse(line); } catch { summary.unparsed_lines++; continue; }
    if (!event || typeof event !== 'object') { summary.unparsed_lines++; continue; }
    summary.parsed_events++;
    if (client === 'codex') {
      if (event.type === 'turn.failed' || event.type === 'error') summary.failed_events++;
      if (event.type === 'turn.completed') summary.terminal_event = event.type;
      const item = event.item;
      if (event.type !== 'item.completed' || !item) continue;
      if (item.type === 'agent_message') summary.final_marker ||= item.text?.trim() === FINAL_MARKER;
      if (item.type === 'command_execution' && item.exit_code === 0 && item.status === 'completed'
        && typeof item.aggregated_output === 'string') {
        summary.exec_marker ||= item.aggregated_output.split(/\r?\n/).some((text) => text.trim() === EXEC_MARKER);
        addTool(item.id, 'command_execution');
      }
      if (item.type === 'file_change' && item.status === 'completed') addTool(item.id, 'file_change');
      if (['command_execution', 'file_change'].includes(item.type)
        && (item.status === 'failed' || (item.exit_code != null && item.exit_code !== 0))) summary.failed_events++;
    } else {
      if (event.type === 'system' && event.subtype === 'init') {
        summary.declared_tools = Array.isArray(event.tools) ? event.tools : null;
        const valid = Array.isArray(event.tools) && event.tools.length === 2
          && event.tools.includes('Read') && event.tools.includes('Write');
        summary.declared_tools_valid = summary.declared_tools_valid !== false && valid;
        if (!valid) summary.failed_events++;
      }
      if (event.type === 'assistant' || event.type === 'user') {
        const content = event.message?.content;
        for (const block of Array.isArray(content) ? content : []) {
          if (event.type === 'assistant' && block.type === 'tool_use') {
            if (typeof block.id !== 'string' || pending.has(block.id) || !['Read', 'Write'].includes(block.name)) {
              summary.failed_events++; continue;
            }
            pending.set(block.id, block.name);
          }
          if (event.type === 'user' && block.type === 'tool_result') {
            const name = pending.get(block.tool_use_id);
            if (!name || (Object.hasOwn(block, 'is_error') && block.is_error !== false)
              || completed.has(block.tool_use_id)) summary.failed_events++;
            else addTool(block.tool_use_id, name);
          }
        }
      }
      if (event.type === 'result') {
        summary.terminal_event = event.subtype === 'success' && event.is_error === false ? 'result.success' : 'result.error';
        summary.final_marker = event.result?.trim() === FINAL_MARKER;
        if (summary.terminal_event === 'result.error') summary.failed_events++;
      }
    }
  }
  summary.success = summary.failed_events === 0 && summary.final_marker
    && (client === 'codex' ? summary.terminal_event === 'turn.completed' && summary.exec_marker
      && successfulNames.has('command_execution')
      : summary.declared_tools_valid === true && summary.terminal_event === 'result.success' && successfulNames.has('Read') && successfulNames.has('Write')
        && pending.size === completed.size);
  return summary;
  function addTool(id, name) {
    if (typeof id !== 'string' || completed.has(id)) { summary.failed_events++; return; }
    completed.add(id); successfulNames.add(name);
    summary.successful_tool_events++; summary.tool_ids.push(id); summary.tools.push(name);
  }
}

export function classifyFailure(result) {
  if (result.timedOut) return 'timeout';
  if (result.outputLimit) return 'client_output_limit';
  if (result.errorCode === 'ENOENT' || result.errorCode === 'EACCES') return 'environment_client_unavailable';
  const diagnostic = `${result.stderr ?? ''}\n${result.stdout ?? ''}`;
  if (/writing is blocked by read-only sandbox|rejected by user approval settings/i.test(diagnostic)) {
    return 'environment_sandbox';
  }
  if (/sandbox.*(?:not configured|setup failed|unavailable|initializ|not supported)|(?:bwrap|landlock).*failed/i.test(diagnostic)) {
    return 'environment_sandbox_unavailable';
  }
  if (/ECONNREFUSED|connection refused|failed to connect/i.test(diagnostic)) return 'environment_bridge_unavailable';
  if (/\b401\b|authentication failed|invalid[_ -]api[_ -]key|unauthorized/i.test(diagnostic)) return 'gateway_authentication';
  if (/\b429\b|rate.?limit/i.test(diagnostic)) return 'gateway_rate_limit';
  if (/\b502\b/.test(diagnostic)) return 'client_transport_unverified';
  if (/\b50[0-9]\b|upstream unavailable|provider error/i.test(diagnostic)) return 'gateway_provider_error';
  return 'client_failure';
}

export function runProcess(command, args, { cwd, env, input = '', timeoutMs }) {
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > TIMEOUT_MS) fail('invalid_process_timeout');
  const started = performance.now();
  return new Promise((resolve) => {
    let stdout = ''; let stderr = ''; let bytes = 0; let timedOut = false; let outputLimit = false; let errorCode;
    const child = spawn(command, args, { cwd, env, shell: false, windowsHide: true,
      detached: process.platform !== 'win32', stdio: ['pipe', 'pipe', 'pipe'] });
    let killed = false;
    // Only the child handle created here can supply the target PID; never the bridge PID.
    const killOwnedTree = () => {
      if (killed || !child.pid || child.exitCode !== null || child.signalCode !== null) return;
      killed = true;
      if (process.platform === 'win32') {
        const systemRoot = Object.entries(process.env).find(([name]) => name.toUpperCase() === 'SYSTEMROOT')?.[1];
        if (systemRoot) spawnSync(path.join(systemRoot, 'System32', 'taskkill.exe'),
          ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, shell: false,
            env: coreEnvironment(process.env), stdio: 'ignore', timeout: 5000 });
        if (child.exitCode === null) child.kill('SIGKILL');
      } else {
        try { process.kill(-child.pid, 'SIGKILL'); } catch { child.kill('SIGKILL'); }
      }
    };
    const timer = setTimeout(() => { timedOut = true; killOwnedTree(); }, timeoutMs);
    child.stdout.setEncoding('utf8'); child.stderr.setEncoding('utf8');
    const collect = (target) => (chunk) => {
      bytes += Buffer.byteLength(chunk);
      if (bytes > OUTPUT_LIMIT) { outputLimit = true; killOwnedTree(); return; }
      if (target === 'stdout') stdout += chunk; else stderr += chunk;
    };
    child.stdout.on('data', collect('stdout')); child.stderr.on('data', collect('stderr'));
    child.on('error', (error) => { errorCode = error.code ?? 'spawn_error'; });
    child.stdin.on('error', () => {});
    child.stdin.end(input);
    child.on('close', (exitCode, signal) => {
      clearTimeout(timer);
      resolve({ stdout, stderr, exitCode, signal, timedOut, outputLimit, errorCode,
        runtimeMs: Math.round(performance.now() - started) });
    });
  });
}

function requiredFlags(client) {
  return client === 'codex' ? ['--ignore-user-config', '--ignore-rules', '--ephemeral', '--skip-git-repo-check',
    '--json', '--color', '--sandbox', '--config'] : ['--safe-mode', '--print', '--output-format', '--verbose',
    '--tools', '--allowedTools', '--no-session-persistence', '--strict-mcp-config', '--mcp-config',
    '--setting-sources', '--max-budget-usd', '--model'];
}
const hash = (text) => createHash('sha256').update(text).digest('hex');
async function restrictedWrite(file, content) {
  await writeFile(file, content, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  await chmod(file, 0o600);
}

async function rejectAncestorConfig(project) {
  let directory = project;
  while (true) {
    try { await lstat(path.join(directory, '.codex', 'config.toml')); }
    catch (error) {
      if (error.code !== 'ENOENT') fail('environment_project_config');
      const parent = path.dirname(directory);
      if (parent === directory) return;
      directory = parent;
      continue;
    }
    fail('environment_project_config');
  }
}

async function restrictEvidenceDirectory(directory) {
  await chmod(directory, 0o700);
  if (process.platform !== 'win32') return;
  // POSIX mode bits are not an access boundary on Windows. Restrict this newly
  // created directory only; children inherit its current-user-only DACL.
  const env = coreEnvironment(process.env);
  const systemRoot = Object.entries(env).find(([name]) => name.toUpperCase() === 'SYSTEMROOT')?.[1];
  if (!systemRoot) fail('environment_private_evidence_unavailable');
  const options = { env, shell: false, windowsHide: true, encoding: 'utf8', timeout: 5000 };
  const identity = spawnSync(path.join(systemRoot, 'System32', 'whoami.exe'), ['/user', '/fo', 'csv', '/nh'], options);
  const sid = identity.stdout?.match(/\bS-1-\d+(?:-\d+)+\b/)?.[0];
  if (identity.status !== 0 || !sid) fail('environment_private_evidence_unavailable');
  const acl = spawnSync(path.join(systemRoot, 'System32', 'icacls.exe'),
    [directory, '/inheritance:r', '/grant:r', `*${sid}:(OI)(CI)F`], options);
  if (acl.status !== 0) fail('environment_private_evidence_unavailable');
}

export async function runSmoke(options, dependencies = {}) {
  const parsed = parseArgs(['--client', options.client, '--ready', options.ready, '--output-dir', options.outputDir]);
  let ready;
  try { ready = validateReady(JSON.parse(await boundedRead(parsed.ready, 'invalid_readiness_path'))); }
  catch (error) { if (String(error.code).includes('readiness')) throw error; fail('invalid_readiness'); }
  const project = path.join(path.dirname(parsed.ready), 'project');
  await plainPath(project, 'directory', 'invalid_project_path');
  if (parsed.client === 'codex') await rejectAncestorConfig(project);
  const fixture = path.join(project, 'fixture.txt');
  const fixtureBytes = await boundedBytes(fixture, 'invalid_fixture_path');
  const artifact = path.join(project, 'client-result.txt');
  const entries = await readdir(project);
  if (entries.includes('client-result.txt')) fail('existing_artifact');
  if (entries.length !== 1 || entries[0] !== 'fixture.txt') fail('invalid_project_contents');
  if (samePath(parsed.outputDir, project) || inside(project, parsed.outputDir)
    || samePath(parsed.outputDir, path.dirname(parsed.ready))) fail('invalid_output_directory');
  await mkdir(parsed.outputDir, { recursive: true, mode: 0o700 });
  await plainPath(parsed.outputDir, 'directory', 'invalid_output_directory');
  const runDir = await mkdtemp(path.join(parsed.outputDir, `cli-${parsed.client}-`));
  await restrictEvidenceDirectory(runDir);
  const isolatedRoot = path.join(runDir, '.isolation');
  const env = buildChildEnvironment(parsed.client, ready, isolatedRoot);
  const preflightEnv = { ...env };
  delete preflightEnv.ZTAPI_ACCEPTANCE_KEY; delete preflightEnv.ANTHROPIC_API_KEY;
  const started = performance.now();
  const deadline = started + TIMEOUT_MS;
  const receipt = { schema_version: 1, client: parsed.client, provider_kind: 'simulated', paid_calls: 0,
    evidence_access: 'owner_only',
    bridge_identity: 'unverified',
    endpoint: ready.base_url, model: ready.model, client_version: null, exit_code: null,
    status: 'failed', phase: 'preflight', runtime_ms: 0,
    actual_events: null, artifact: null, model_catalog: null, warnings: [],
    dispatch_count: null, dispatch_count_source: 'bridge_receipt_required',
    logs: { stdout: path.join(runDir, 'client.stdout.jsonl'), stderr: path.join(runDir, 'client.stderr.log') },
    receipt_path: path.join(runDir, 'receipt.json') };
  let result = { stdout: '', stderr: '' };
  const execute = dependencies.runProcess ?? runProcess;
  const secrets = [ready.api_key];
  try {
    for (const directory of new Set(Object.values(env).filter((value) => value.startsWith(`${isolatedRoot}${path.sep}`)))) {
      await mkdir(directory, { recursive: true, mode: 0o700 });
    }
    receipt.phase = 'bridge_verification';
    try {
      if (await (dependencies.verifyBridge ?? verifySimulatedBridge)(ready) === false) fail('environment_bridge_identity_unverified');
      receipt.bridge_identity = 'verified';
    } catch { fail('environment_bridge_identity_unverified'); }
    receipt.phase = 'preflight';
    const installed = await (dependencies.resolveClient ?? resolveInstalledClient)(parsed.client);
    receipt.client_entry_kind = installed.entry_kind ?? 'unknown';
    const call = async (args, input, inference = false) => {
      const remaining = Math.floor(deadline - performance.now());
      if (remaining <= 0) fail('timeout');
      return execute(installed.command, [...installed.prefixArgs, ...args], {
        cwd: project, env: inference ? env : preflightEnv, input, timeoutMs: remaining,
      });
    };
    result = await call(['-V']);
    if (result.exitCode !== 0 || result.timedOut || result.errorCode || result.outputLimit) fail(classifyFailure(result));
    const match = (parsed.client === 'codex' ? /^codex-cli ([^\s]+)\s*$/
      : /^([^\s]+) \(Claude Code\)\s*$/).exec(result.stdout);
    receipt.client_version = match?.[1] ?? null;
    if (receipt.client_version !== CLIENTS[parsed.client].version) fail('environment_client_version');
    result = await call(parsed.client === 'codex' ? ['exec', '--ignore-user-config', '--ignore-rules', '--help']
      : ['--safe-mode', '--help']);
    if (result.exitCode !== 0 || result.timedOut || result.errorCode || result.outputLimit) fail(classifyFailure(result));
    if (!requiredFlags(parsed.client).every((flag) => result.stdout.includes(flag))) fail('environment_client_flags');
    let catalogPath;
    if (parsed.client === 'codex') {
      receipt.phase = 'catalog_preparation';
      catalogPath = path.join(env.CODEX_HOME, 'fixture-models.json');
      const contents = `${JSON.stringify(buildFixtureModelCatalog(ready), null, 2)}\n`;
      await restrictedWrite(catalogPath, contents);
      receipt.model_catalog = { source: 'simulated_fixture_only', model: ready.model,
        shell_type: 'unified_exec', apply_patch_tool_type: 'freeform', input_modalities: ['text'],
        sha256: hash(contents) };
    }
    receipt.phase = 'inference';
    const invocation = buildInvocation(parsed.client, ready, project, catalogPath);
    result = await call(invocation.args, invocation.input, true);
    receipt.exit_code = result.exitCode ?? null;
    receipt.signal = result.signal ?? null;
    receipt.actual_events = summarizeEvents(parsed.client, result.stdout);
    if (/generic.*(?:model|fallback)|(?:model|metadata).*(?:unknown|not found|fallback)|catalog.*(?:slow|timeout)/i.test(result.stderr)) {
      receipt.warnings.push('model_metadata_generic_fallback_unverified');
    }
    if (result.timedOut || result.errorCode || result.outputLimit) fail(classifyFailure(result));
    if (parsed.client === 'claude-code' && receipt.actual_events.declared_tools_valid !== true) fail('environment_client_tools_unavailable');
    if (result.exitCode !== 0) fail(classifyFailure(result));
    if (hash(await boundedBytes(fixture, 'client_fixture_modified')) !== hash(fixtureBytes)) fail('client_fixture_modified');
    const artifactBytes = await boundedBytes(artifact, 'client_artifact_missing');
    const artifactText = artifactBytes.toString('utf8');
    if (!artifactText.split(/\r?\n/).some((line) => line === FILE_MARKER)) fail('client_artifact_marker_missing');
    if (artifactText.includes(ready.api_key)) fail('client_artifact_contains_secret');
    receipt.artifact = { path: artifact, marker: FILE_MARKER, sha256: hash(artifactBytes), bytes: artifactBytes.length };
    if (!receipt.actual_events.success) fail('client_evidence_incomplete');
    receipt.status = 'passed';
  } catch (error) {
    receipt.error = { classification: /^[a-z_]+$/.test(error.code ?? '') ? error.code : 'runner_failure',
      message: redact(result.stderr || error.code || 'runner_failure', secrets).slice(0, 2048) };
  } finally {
    try {
      // Verify the exact owned target immediately before recursive removal.
      if (!inside(runDir, isolatedRoot)) fail('cleanup_boundary');
      await plainPath(isolatedRoot, 'directory', 'cleanup_boundary');
      await rm(isolatedRoot, { recursive: true, force: true });
    } catch {
      receipt.status = 'failed'; receipt.warnings.push('controlled_temp_cleanup_failed');
      receipt.error ??= { classification: 'environment_cleanup_failed', message: 'controlled_temp_cleanup_failed' };
    }
  }
  receipt.runtime_ms = Math.round(performance.now() - started);
  await restrictedWrite(receipt.logs.stdout, redact(result.stdout, secrets));
  await restrictedWrite(receipt.logs.stderr, redact(result.stderr, secrets));
  // IDs and other client-controlled metadata are redacted too, not only raw output.
  const safeReceipt = JSON.parse(redact(JSON.stringify(receipt), secrets));
  await restrictedWrite(receipt.receipt_path, `${JSON.stringify(safeReceipt, null, 2)}\n`);
  return safeReceipt;
}

if (process.argv[1] && samePath(fileURLToPath(import.meta.url), process.argv[1])) {
  try {
    const receipt = await runSmoke(parseArgs(process.argv.slice(2)));
    // Do not echo the readiness document, environment, CLI output, or command line.
    process.stdout.write(`${JSON.stringify({ status: receipt.status, client: receipt.client,
      provider_kind: 'simulated', paid_calls: 0 })}\n`);
    process.exitCode = receipt.status === 'passed' ? 0 : 1;
  } catch (error) {
    process.stderr.write(`${/^[a-z_]+$/.test(error.code ?? '') ? error.code : 'runner_failure'}\n`);
    process.exitCode = 1;
  }
}
