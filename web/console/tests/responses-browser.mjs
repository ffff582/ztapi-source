/* global document:readonly, window:readonly, navigator:readonly */
import assert from 'node:assert/strict';
import console from 'node:console';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';

const root = fileURLToPath(new URL('../', import.meta.url));
const evidence = path.resolve(process.argv[2] ?? path.join(root, '../../output/playwright/responses-frontend'));
await mkdir(evidence, { recursive: true });

function catalogItem(modelName, protocol, endpoint, providerName = 'OpenAI') {
  return {
    model_name: modelName, provider_family: providerName.toLowerCase(), provider_name: providerName,
    protocol, supported_endpoint_types: [endpoint], enable_groups: ['default'],
    input_price_per_million: '1', output_price_per_million: '2',
    billing_dimensions: ['input_tokens', 'output_tokens'],
    sale_usd: { input_tokens: '1', output_tokens: '2' }, billing_rule: 'token', pricing_version: 'offline-fixture',
  };
}
const catalog = [
  catalogItem('zt-gpt-5.4-pro', 'openai_compatible', 'openai-response'),
  catalogItem('zt-gpt-5.5', 'openai_compatible', 'openai'),
  catalogItem('zt-claude-sonnet-5', 'anthropic', 'anthropic', 'Claude'),
  catalogItem('zt-gemini-2.5-pro', 'gemini', 'gemini', 'Gemini'),
];

async function checkLayout(page) {
  const issues = await page.evaluate(() => {
    const found = [];
    if (document.documentElement.scrollWidth > window.innerWidth + 1) found.push('horizontal overflow');
    const overlaps = (a, b) => {
      const x = a.getBoundingClientRect();
      const y = b.getBoundingClientRect();
      return Math.min(x.right, y.right) - Math.max(x.left, y.left) > 1 &&
        Math.min(x.bottom, y.bottom) - Math.max(x.top, y.top) > 1;
    };
    const checkSiblings = (elements, label) => {
      for (let i = 0; i < elements.length; i++) {
        for (let j = i + 1; j < elements.length; j++) {
          if (overlaps(elements[i], elements[j])) found.push(`${label}: ${i}/${j}`);
        }
      }
    };
    checkSiblings([...document.querySelectorAll('.console-page > header, .console-page > section, .console-page > aside')], 'page sections overlap');
    for (const row of document.querySelectorAll('.model-support-table tbody tr')) {
      checkSiblings([...row.querySelectorAll('td:not(.model-support-actions)')], 'model cells overlap');
      const code = row.querySelector('td:first-child code');
      const button = row.querySelector('button');
      if (code && button && overlaps(code, button)) found.push('model name overlaps copy button');
    }
    checkSiblings([...document.querySelectorAll('.guide-code-heading > *')], 'guide heading overlaps selector');
    checkSiblings([...document.querySelectorAll('.guide-tabs button')], 'language tabs overlap');
    checkSiblings([...document.querySelectorAll('.guide-code-toolbar > *')], 'copy toolbar overlaps');
    return found;
  });
  assert.deepEqual(issues, []);
  const images = await page.locator('img').evaluateAll((items) => items.every((img) => img.complete && img.naturalWidth > 0));
  assert.equal(images, true, 'local image assets must load');
}

const report = { status: 'running', externalRequests: [], unexpectedAPIRequests: [], screenshots: [], viewports: [] };
let browser;
let server;
try {
  server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  report.browser = browser.version();
  for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }, { width: 320, height: 568 }]) {
    const context = await browser.newContext({ viewport, isMobile: viewport.width < 500, serviceWorkers: 'block' });
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) {
        report.externalRequests.push(url.origin);
        return route.abort();
      }
      if (url.pathname === '/api/auth/refresh') {
        return route.fulfill({ json: { success: true, data: {
          access_token: 'offline-browser-fixture', expires_in: 900,
          user: { id: 7, username: 'offline-review', role: 1, group: 'default' },
        } } });
      }
      if (url.pathname === '/api/user/models') {
        return route.fulfill({ json: { success: true, data: catalog.map((item) => item.model_name), catalog } });
      }
      if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/')) {
        report.unexpectedAPIRequests.push(url.pathname);
        return route.abort();
      }
      return route.continue();
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()); });
    page.setDefaultTimeout(10_000);
    await page.goto(`${origin}/console/models`);
    const pro = page.getByRole('row').filter({ has: page.getByText('zt-gpt-5.4-pro', { exact: true }) });
    await expect(pro.getByText('/v1/responses', { exact: true })).toBeVisible();
    await expect(pro.getByText('/v1/chat/completions', { exact: true })).toHaveCount(0);
    await expect(page.getByText('/v1/chat/completions', { exact: true })).toBeVisible();
    await expect(page.getByText('/v1/messages', { exact: true })).toBeVisible();
    await expect(page.getByText('/v1beta/models/{model}:generateContent', { exact: true })).toBeVisible();
    await pro.getByRole('button', { name: '复制 zt-gpt-5.4-pro' }).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('zt-gpt-5.4-pro');
    await checkLayout(page);
    let screenshot = path.join(evidence, `models-${viewport.width}.png`);
    await page.screenshot({ path: screenshot, fullPage: true });
    report.screenshots.push(screenshot);

    await page.goto(`${origin}/console/guide`);
    await expect(page.getByTestId('guide-code')).toContainText('/v1/chat/completions');
    await expect(page.getByRole('note', { name: '端点选择' })).toContainText('Responses-only');
    await page.getByLabel('调用接口').selectOption('responses');
    await expect(page.getByText('POST /v1/responses', { exact: true })).toBeVisible();
    for (const [language, expected] of [['cURL', '/v1/responses'], ['Python', 'client.responses.create('], ['Node.js', 'client.responses.create({']]) {
      await page.getByRole('tab', { name: language, exact: true }).click();
      const code = page.getByTestId('guide-code');
      await expect(code).toContainText(expected);
      await expect(code).not.toContainText('chat/completions');
      await expect(code).not.toContainText('chat.completions');
      await page.getByRole('button', { name: '复制代码', exact: true }).click();
      await expect.poll(async () => (await page.evaluate(() => navigator.clipboard.readText())).replace(/\r\n/g, '\n')).toBe(await code.textContent());
    }
    await page.getByRole('tab', { name: 'cURL', exact: true }).click();
    await checkLayout(page);
    screenshot = path.join(evidence, `guide-responses-${viewport.width}.png`);
    await page.screenshot({ path: screenshot, fullPage: true });
    report.screenshots.push(screenshot);
    assert.deepEqual(errors, []);
    report.viewports.push({ ...viewport, status: 'passed', layoutIssues: [], browserErrors: errors });
    await context.close();
  }
  assert.deepEqual(report.externalRequests, []);
  assert.deepEqual(report.unexpectedAPIRequests, []);
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  report.error = error.message;
  process.exitCode = 1;
} finally {
  await browser?.close();
  if (server) await new Promise((resolve) => server.httpServer.close(resolve));
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}
