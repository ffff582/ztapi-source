import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

assert.equal(process.platform, 'darwin');
const [bundle, playwright] = process.argv.slice(2);
const { _electron: electron } = createRequire(import.meta.url)(resolve(playwright));
const root = await mkdtemp(join(homedir(), 'ztapi-installed-qa-'));
const project = join(root, 'project');
const target = join(project, 'calc.mjs');
const corrected = 'export const sum = (a, b) => a + b;';
const marker = 'ZTAPI_INSTALLED_PROJECT_OK';
const model = 'zt-claude-sonnet-5';
let client;
try {
  await mkdir(project);
  await writeFile(target, 'export const sum = (a, b) => a - b;');
  await writeFile(join(project, 'calc.test.mjs'), 'import test from "node:test"; import assert from "node:assert/strict"; import { sum } from "./calc.mjs"; test("addition", () => assert.equal(sum(9, 4), 13));');
  client = await electron.launch({ executablePath: join(bundle, 'Contents/MacOS/Electron'),
    env: { ...process.env, HOME: root }, chromiumSandbox: true, timeout: 35_000 });
  const page = await client.firstWindow({ timeout: 25_000 });
  page.setDefaultTimeout(15_000);
  await page.locator('#account-login').waitFor();
  assert.equal(await page.evaluate(() => typeof require), 'undefined');
  await client.evaluate(({ app, dialog, BrowserWindow }, fixture) => {
    const fs = process.getBuiltinModule('node:fs');
    const { randomUUID } = process.getBuiltinModule('node:crypto');
    if (app.commandLine.hasSwitch('no-sandbox') || !BrowserWindow.getAllWindows()[0]?.webContents.getLastWebPreferences().sandbox)
      throw Error('Renderer sandbox was not active');
    const original = globalThis.fetch;
    const holds = new Map();
    const counts = { login: 0, model: 0, reserve: 0, revoke: 0, approvals: 0, logout: 0 };
    globalThis.ztapiQa = counts;
    const user = { id: 91, username: 'synthetic-qa' };
    const json = data => Response.json({ success: true, data });
    let key, task;
    const tools = [
      ['read_file', { path: 'calc.mjs' }],
      ['stage_edit', { path: 'calc.mjs', content: fixture.corrected }],
      ['run_test', { path: 'calc.test.mjs' }],
    ];
    function reply(replyNumber, action, hold) {
      const block = action ? { type: 'tool_use', id: 'toolu_installed_' + replyNumber,
        name: 'mcp__ztapi_' + action[0], input: {} } : { type: 'text', text: '' };
      const delta = action ? { type: 'input_json_delta', partial_json: JSON.stringify(action[1]) }
        : { type: 'text_delta', text: fixture.marker };
      const events = [
        { type: 'message_start', message: { id: 'msg_installed_' + replyNumber, type: 'message', role: 'assistant',
          model: fixture.model, content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 5, output_tokens: 0 } } },
        { type: 'content_block_start', index: 0, content_block: block },
        { type: 'content_block_delta', index: 0, delta },
        { type: 'content_block_stop', index: 0 },
        { type: 'message_delta', delta: { stop_reason: action ? 'tool_use' : 'end_turn', stop_sequence: null },
          usage: { output_tokens: 5 } },
        { type: 'message_stop' },
      ];
      return new Response(events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join(''), {
        headers: { 'content-type': 'text/event-stream', 'x-ztapi-budget-hold': hold },
      });
    }
    dialog.showOpenDialog = async () => ({ canceled: false, filePaths: [fixture.project] });
    dialog.showMessageBox = async (_window, options) => {
      if (typeof options?.detail !== 'string' || !options.detail.includes(fixture.project) ||
          !options.detail.includes('calc.mjs') || !fs.readFileSync(fixture.target, 'utf8').includes('a - b'))
        throw Error('Unexpected writeback scope');
      return { response: ++counts.approvals === 1 ? 0 : 1 };
    };
    globalThis.fetch = async (value, init) => {
      const url = new URL(typeof value === 'string' || value instanceof URL ? value : value.url);
      if (url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.pathname === '/v1/messages')
        return original(value, init);
      if (url.origin !== 'https://ztapi.vip' || url.search) throw Error('Non-fixture network call rejected');
      const body = init?.body ? JSON.parse(init.body) : null;
      const headers = new Headers(init?.headers);
      if (url.pathname === '/api/auth/login' && init.method === 'POST') {
        if (body.username !== 'synthetic-qa' || body.password !== 'synthetic-offline-only') throw Error('Unexpected QA credentials');
        counts.login++;
        return Response.json({ success: true, data: { user, access_token: 'synthetic-token', expires_in: 600 } }, {
          headers: { 'set-cookie': 'ztapi_refresh=synthetic-cookie; Secure; HttpOnly' },
        });
      }
      if (url.pathname === '/api/auth/session') return json(user);
      if (url.pathname === '/api/user/self') return json({ id: user.id, quota: 500000 });
      if (url.pathname === '/api/auth/logout') { counts.logout++; return json(true); }
      if (url.pathname === '/.well-known/ztapi-workbench') return Response.json({
        protocol: 'ztapi-workbench-first-party-v1', budget: 'wire-bound-v1', messages: 'wire-bound-v1',
        dispatch: 'enabled', account_routes: 'workbench-v1',
      });
      if (url.pathname === '/api/workbench/models') return Response.json({
        success: true, data: [fixture.model], catalog: [{ model_name: fixture.model, modality: 'text',
          supported_endpoint_types: ['anthropic'], workbench: { contract: 'ztapi-workbench-messages-v1', project_tools: true } }],
      });
      if (url.pathname === '/api/workbench/keys' && init.method === 'POST') {
        if (body.model_limits !== fixture.model || body.unlimited_quota || body.remain_quota > 500000) throw Error('Key policy');
        key = { ...body, id: 7, user_id: user.id, used_quota: 0 };
        return json({ id: 7, key: 'sk-zt-syntheticQaOnly' });
      }
      if (url.pathname === '/api/workbench/keys/7' && init.method === 'GET' && key) return json(key);
      if (url.pathname === '/api/workbench/keys/7' && init.method === 'DELETE' && key) { key = null; counts.revoke++; return json(true); }
      if (!key || headers.get('authorization') !== 'Bearer sk-zt-syntheticQaOnly') throw Error('Unknown fixture route');
      if (url.pathname === '/__workbench_budget/messages/reserve') {
        task ??= body.task;
        if (body.task !== task || body.model !== fixture.model || body.legs.length !== 1 || counts.reserve >= 4)
          throw Error('Bad reservation');
        const id = randomUUID();
        holds.set(id, body.legs[0].wire);
        counts.reserve++;
        return json({ holds: [{ id, stage: body.legs[0].stage }] });
      }
      if (url.pathname === '/__workbench_budget/messages') {
        const hold = headers.get('x-ztapi-hold');
        if (headers.get('x-ztapi-task') !== task || holds.get(hold) !== init.body || body.model !== fixture.model ||
            counts.model >= 4) throw Error('Unbound model dispatch');
        holds.delete(hold);
        const previous = JSON.stringify(body.messages);
        const receipt = ['a - b', 'staged', 'exitCode'][counts.model - 1];
        if (receipt && !previous.includes(receipt)) throw Error('Missing tool receipt');
        return reply(counts.model + 1, tools[counts.model++], hold);
      }
      throw Error('Unexpected fixture route');
    };
  }, { project, target, model, marker, corrected });
  await page.locator('#account-login').click();
  await page.locator('#login-username').fill('synthetic-qa');
  await page.locator('#login-password').fill('synthetic-offline-only');
  await page.locator('#login-submit').click();
  await page.locator('#account-models').locator(`option[value="${model}"]`).waitFor({ state: 'attached' });
  assert.match(await page.locator('#account-balance').innerText(), /1\./);
  await page.locator('#account-models').selectOption(model);
  await page.locator('#open').click();
  await page.waitForFunction(() => document.getElementById('project')?.textContent.trim() === 'project');
  await page.locator('#model-prompt').fill('Read the addition file, repair the regression and run its test.');
  await page.locator('#model-submit').click();
  await page.locator('#online-consent-allow').click({ timeout: 25_000 });
  await page.locator('#messages').getByText(marker, { exact: true }).waitFor({ timeout: 45_000 });
  assert.match(await readFile(target, 'utf8'), /a - b/);
  for (let i = 0; i < 2; i++) {
    await page.locator('#review').click();
    await page.locator('#review-window').waitFor();
    assert.match(await page.locator('#diff').innerText(), /a - b[\s\S]*a \+ b/);
    await page.locator('#changes input').check();
    await page.locator('#approve').click();
    await page.locator('#review-window').waitFor({ state: 'hidden' });
    if (i === 0) assert.match(await readFile(target, 'utf8'), /a - b/);
  }
  assert.equal(await readFile(target, 'utf8'), corrected);
  await page.locator('#account-logout').click();
  await page.locator('#account-login').waitFor();
  assert.deepEqual(await client.evaluate(() => globalThis.ztapiQa),
    { login: 1, model: 4, reserve: 4, revoke: 1, approvals: 2, logout: 1 });
  console.log('Installed Mac Workbench verified via synthetic account, gateway, native code and isolated test. No paid request.');
} finally {
  try { await client?.close(); }
  finally { await rm(root, { recursive: true, force: true }); }
}
