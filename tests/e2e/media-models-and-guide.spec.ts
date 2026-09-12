import { expect, test, type Page, type Route } from '@playwright/test';

const adminBaseURL = process.env.ZTAPI_ADMIN_E2E_BASE_URL ?? 'http://127.0.0.1:4174';

const userSession = {
  success: true,
  data: {
    access_token: 'browser-user-session',
    expires_in: 900,
    user: { id: 7, username: 'alice', role: 1, group: 'default' },
  },
};

const adminSession = {
  success: true,
  data: {
    access_token: 'browser-admin-session',
    expires_in: 900,
    user: { id: 2, username: 'root-admin', role: 100, group: 'default' },
  },
};

const imageModel = {
  modality: 'image',
  model_name: 'zt-image-pro',
  provider_family: 'openai',
  provider_name: 'ZTAPI Image',
  protocol: 'openai_compatible',
  enable_groups: ['default'],
  supported_endpoint_types: ['images'],
  input_price_per_million: '',
  output_price_per_million: '',
  billing_dimensions: [],
  sale_usd: {},
  billing_rule: 'multi_dimension',
  billing_unit: 'usd_per_million_tokens',
  pricing_version: 'media-browser-v1',
  supported_options: {
    sizes: ['1024x1024'],
    qualities: ['standard'],
    response_formats: ['url'],
    min_count: 1,
    max_count: 2,
  },
  pricing_rules: [
    {
      id: 'image_output',
      conditions: { token_bucket: 'image_output' },
      billing_unit: 'usd_per_million_tokens',
      sale_usd: { image_output: '39.00' },
    },
  ],
};

const videoModel = {
  ...imageModel,
  modality: 'video',
  model_name: 'zt-video-pro',
  provider_family: 'seedance',
  provider_name: 'ZTAPI Video',
  supported_endpoint_types: ['video-tasks'],
  supported_options: {
    resolutions: ['720p'],
    duration_seconds: [5],
    supports_video_input: false,
  },
  pricing_rules: [
    {
      id: '720p_video_false',
      conditions: { resolution: '720p', contains_video_input: 'false' },
      billing_unit: 'usd_per_million_tokens',
      sale_usd: { input_tokens: '10.4192129630' },
    },
  ],
};

const adminModel = {
  id: 31,
  version: 9,
  source_model: 'zt-image-pro',
  public_name: 'zt-image-pro',
  family: 'openai',
  protocol: 'openai_compatible',
  provider_family: 'openai',
  modality: 'image',
  input_cost_per_million: 0,
  output_cost_per_million: 0,
  input_price_per_million: 0,
  output_price_per_million: 0,
  cache_read_ratio: 1,
  cache_creation_ratio: 1,
  cache_creation_5m_ratio: 1,
  cache_creation_1h_ratio: 1,
  image_ratio: 1,
  audio_ratio: 1,
  audio_completion_ratio: 1,
  enabled_groups: ['default'],
  published: true,
  route_ready: true,
  enabled_route_count: 2,
  publication_blockers: [],
};

function fulfill(route: Route, data: unknown) {
  return route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ success: true, data }),
  });
}

async function expectNoHorizontalOverflow(page: Page) {
  const result = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
    offenders: Array.from(document.querySelectorAll<HTMLElement>('body *'))
      .filter((element) => {
        const box = element.getBoundingClientRect();
        return box.width > 0 && (box.left < -1 || box.right > window.innerWidth + 1);
      })
      .slice(0, 12)
      .map((element) => {
        const box = element.getBoundingClientRect();
        return {
          tag: element.tagName,
          className: element.className,
          left: Math.round(box.left),
          right: Math.round(box.right),
          width: Math.round(box.width),
        };
      }),
  }));
  expect(result.scrollWidth, `horizontal overflow: ${JSON.stringify(result)}`).toBeLessThanOrEqual(
    result.viewportWidth,
  );
}

async function expectConsoleRegionsDoNotOverlap(page: Page) {
  const regions = await page.evaluate(() => {
    const box = (selector: string) => {
      const element = document.querySelector(selector);
      const value = element?.getBoundingClientRect();
      return value
        ? {
            left: value.left,
            right: value.right,
            top: value.top,
            bottom: value.bottom,
          }
        : null;
    };
    return {
      width: window.innerWidth,
      sidebar: box('.console-sidebar'),
      workspace: box('.console-workspace'),
      header: box('.console-header'),
      content: box('.console-content'),
    };
  });
  expect(regions.sidebar).not.toBeNull();
  expect(regions.workspace).not.toBeNull();
  expect(regions.header).not.toBeNull();
  expect(regions.content).not.toBeNull();
  if (regions.width > 640) {
    expect(regions.sidebar!.right).toBeLessThanOrEqual(regions.workspace!.left);
  } else {
    expect(regions.sidebar!.bottom).toBeLessThanOrEqual(regions.header!.top);
  }
  expect(regions.header!.bottom).toBeLessThanOrEqual(regions.content!.top);
}

async function expectAdminRegionsDoNotOverlap(page: Page) {
  const regions = await page.evaluate(() => {
    const box = (selector: string) => {
      const element = document.querySelector(selector);
      const value = element?.getBoundingClientRect();
      return value
        ? {
            left: value.left,
            right: value.right,
            top: value.top,
            bottom: value.bottom,
          }
        : null;
    };
    return {
      width: window.innerWidth,
      sidebar: box('.ztapi-admin-sidebar'),
      header: box('.ztapi-admin-header'),
      content: box('.ztapi-admin-content'),
      heading: box('#ztapi-model-title'),
    };
  });
  expect(regions.header).not.toBeNull();
  expect(regions.content).not.toBeNull();
  expect(regions.heading).not.toBeNull();
  expect(regions.header!.bottom).toBeLessThanOrEqual(regions.heading!.top);
  if (regions.width > 768) {
    expect(regions.sidebar).not.toBeNull();
    expect(regions.sidebar!.right).toBeLessThanOrEqual(regions.content!.left);
  }
}

function watchBrowser(page: Page) {
  const browserErrors: string[] = [];
  const failedAPIs: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });
  page.on('pageerror', (error) => browserErrors.push(error.message));
  page.on('requestfailed', (request) => {
    if (request.url().includes('/api/')) failedAPIs.push(request.url());
  });
  page.on('response', (response) => {
    if (response.url().includes('/api/') && response.status() >= 400) {
      failedAPIs.push(`${response.status()} ${response.url()}`);
    }
  });
  return { browserErrors, failedAPIs };
}

async function installConsoleAPI(page: Page, unexpected: string[]) {
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === '/api/auth/refresh') {
      await route.fulfill({ json: userSession });
      return;
    }
    if (url.pathname === '/api/user/models') {
      await route.fulfill({
        json: {
          success: true,
          data: [imageModel.model_name, videoModel.model_name],
          catalog: [imageModel, videoModel],
        },
      });
      return;
    }
    if (url.pathname === '/api/auth/session') {
      await fulfill(route, userSession.data.user);
      return;
    }
    if (url.pathname === '/api/log/self/stat') {
      await fulfill(route, { rpm: 2, tpm: 168, quota: 0 });
      return;
    }
    if (url.pathname === '/api/log/self') {
      await fulfill(route, {
        page: 1,
        page_size: 5,
        total: 1,
        items: [{
          timestamp: 1789100000,
          request_id: 'req-browser-usage-001',
          model: 'zt-claude-sonnet-5',
          status: 'success',
          latency: 2,
          prompt_tokens: 120,
          completion_tokens: 48,
          total_tokens: 168,
          billed_amount: 0.004321,
        }],
      });
      return;
    }
    if (url.pathname === '/api/status') {
      await fulfill(route, { quota_per_unit: 500_000 });
      return;
    }
    if (url.pathname === '/api/user/self') {
      await fulfill(route, { quota: 5_000_000 });
      return;
    }
    if (url.pathname === '/api/user/self/pending-settlements') {
      await fulfill(route, { items: [], next_after_id: 0 });
      return;
    }
    if (url.pathname === '/api/user/topup/info') {
      await fulfill(route, {
        enable_usdt_trc20_topup: true,
        usdt_trc20_network: 'tron-mainnet',
        usdt_trc20_asset: 'USDT',
        usdt_trc20_min_topup: 10,
        usdt_trc20_order_ttl_seconds: 600,
      });
      return;
    }
    if (url.pathname === '/api/user/topup/self') {
      await fulfill(route, { page: 1, page_size: 5, total: 0, items: [] });
      return;
    }
    unexpected.push(`${request.method()} ${url.pathname}${url.search}`);
    await route.fulfill({ status: 418, json: { success: false } });
  });
}

async function installAdminAPI(page: Page, unexpected: string[]) {
  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === '/api/auth/refresh') {
      await route.fulfill({ json: adminSession });
      return;
    }
    if (url.pathname === '/api/models/ztapi/') {
      await fulfill(route, {
        items: [adminModel],
        total: 1,
        page: 1,
        page_size: 100,
      });
      return;
    }
    if (url.pathname === '/api/models/ztapi/audit-events') {
      await fulfill(route, { items: [] });
      return;
    }
    if (url.pathname === '/api/models/ztapi/31/media-contract') {
      await fulfill(route, {
        model_id: 31,
        public_name: 'zt-image-pro',
        modality: 'image',
        quotation_sheet: '国外模型',
        quotation_cell: 'C42',
        quotation_label: '图片输出',
        quotation_resource: '企业资源',
        quotation_currency: 'USD',
        price_policy: 'enterprise_40_margin',
        price_source_version: 3,
        frozen_pricing_version: 'media-browser-v1',
        quotation_effective_at: 1788566400,
        source_document_checksum: 'a'.repeat(64),
        protocol_evidence_sha256: 'b'.repeat(64),
        pending_reconciliation_count: 2,
        published: true,
      });
      return;
    }
    if (url.pathname === '/api/models/ztapi/31/health') {
      await fulfill(route, {
        model_id: 31,
        enabled: true,
        observed: true,
        state: {
          model_id: 31,
          generation: 2,
          open: false,
          completion_sequence: 8,
          consecutive_failures: 0,
          updated_at: 1788566400,
        },
        window: { valid_samples: 8, failures: 0 },
        coverage: [],
        events: [
          {
            id: 8,
            completion_sequence: 8,
            generation: 2,
            result: 'success',
            finish_reasons: ['stop'],
            completed_at: 1788566400,
          },
        ],
        incidents: [],
        outbox: [],
      });
      return;
    }
    unexpected.push(`${request.method()} ${url.pathname}${url.search}`);
    await route.fulfill({ status: 418, json: { success: false } });
  });
}

test('user media catalog, guide and wallet remain usable without data exposure', async ({ page }, testInfo) => {
  const unexpected: string[] = [];
  const observed = watchBrowser(page);
  await installConsoleAPI(page, unexpected);
  await page.addInitScript(() => window.localStorage.setItem('ztapi.locale', 'zh-CN'));

  await page.goto('/console/models');
  await expect(page.getByRole('heading', { level: 1, name: '模型支持' })).toBeVisible();
  const categoryTabs = page.getByRole('tablist', { name: '模型分类' });
  await expect(categoryTabs.getByRole('tab')).toHaveText(['全部', 'OpenAI', '图片模型', '视频模型']);
  await categoryTabs.getByRole('tab', { name: '图片模型' }).click();
  await expect(page.getByText('zt-image-pro')).toBeVisible();
  await expect(page.getByText('zt-video-pro')).not.toBeVisible();
  await categoryTabs.getByRole('tab', { name: '全部' }).click();
  const imageRow = page.getByText('zt-image-pro').locator('xpath=ancestor::*[@role="row"][1]');
  await expect(imageRow.getByText('/v1/images/generations')).toBeVisible();
  await expect(imageRow.getByText(/1024x1024/)).toBeVisible();
  await expect(imageRow.getByText('$39 / 1M tokens')).toBeVisible();
  const videoRow = page.getByText('zt-video-pro').locator('xpath=ancestor::*[@role="row"][1]');
  await expect(videoRow.getByText('/v1/video/generations')).toBeVisible();
  await expect(videoRow.getByText('720p · 5 秒 · 无视频输入')).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await expectConsoleRegionsDoNotOverlap(page);
  expect(await page.locator('body').innerText()).not.toMatch(/yunxin|云信号池|sk-[A-Za-z0-9]{12,}/i);
  await page.screenshot({
    path: `test-results/visual/media-models-${testInfo.project.name}.png`,
    fullPage: true,
  });

  await page.goto('/console/guide');
  await expect(page.getByRole('heading', { level: 1, name: '使用说明' })).toBeVisible();
  await page.getByLabel('调用接口').selectOption('images');
  await expect(page.getByText('POST /v1/images/generations')).toBeVisible();
  await page.getByLabel('调用接口').selectOption('video-tasks');
  await expect(page.getByText('POST /v1/video/generations')).toBeVisible();
  await expect(page.getByTestId('guide-code')).toContainText('https://ztapi.vip/v1/video/generations/${TASK_ID}');
  await expectNoHorizontalOverflow(page);
  await expectConsoleRegionsDoNotOverlap(page);
  await page.screenshot({
    path: `test-results/visual/media-guide-${testInfo.project.name}.png`,
    fullPage: true,
  });

  await page.goto('/console/wallet');
  await expect(page.getByRole('heading', { level: 1, name: '余额充值' })).toBeVisible();
  await expect(page.getByRole('region', { name: '账户余额' }).getByText('$10.00')).toBeVisible();
  await expect(page.getByText('最低 10 USDT')).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await expectConsoleRegionsDoNotOverlap(page);
  await page.screenshot({
    path: `test-results/visual/media-wallet-${testInfo.project.name}.png`,
    fullPage: true,
  });

  await page.goto('/console');
  await expect(page.getByRole('heading', { level: 1, name: '使用概览' })).toBeVisible();
  const usageRow = page.getByText('zt-claude-sonnet-5').locator('xpath=ancestor::tr[1]');
  await expect(usageRow.getByText('$0.004321')).toBeVisible();
  await expect(usageRow.getByText('输入 120')).toBeVisible();
  await expect(usageRow.getByText('输出 48')).toBeVisible();
  await expect(usageRow.getByText('总计 168')).toBeVisible();
  await expect(usageRow.getByText('2 秒')).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await expectConsoleRegionsDoNotOverlap(page);
  await page.screenshot({
    path: `test-results/visual/usage-log-${testInfo.project.name}.png`,
    fullPage: true,
  });

  await page.goto('/console/logs');
  await expect(page.getByRole('heading', { level: 1, name: '使用日志' })).toBeVisible();
  const fullLogRow = page.getByText('zt-claude-sonnet-5').locator('xpath=ancestor::tr[1]');
  await expect(fullLogRow.getByText('$0.004321')).toBeVisible();
  await expect(fullLogRow.getByText('本次实际扣费')).toBeVisible();
  await expect(fullLogRow.getByText('输入 120')).toBeVisible();
  await expect(fullLogRow.getByText('输出 48')).toBeVisible();
  await expect(fullLogRow.getByText('总计 168')).toBeVisible();
  await expect(fullLogRow.getByText('2 秒')).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await expectConsoleRegionsDoNotOverlap(page);
  await page.screenshot({
    path: `test-results/visual/full-usage-log-${testInfo.project.name}.png`,
    fullPage: true,
  });

  expect(unexpected).toEqual([]);
  expect(observed.failedAPIs).toEqual([]);
  expect(observed.browserErrors).toEqual([]);
});

test('administrator sees safe media evidence and pending reconciliation state', async ({ page }, testInfo) => {
  const unexpected: string[] = [];
  const observed = watchBrowser(page);
  await installAdminAPI(page, unexpected);

  await page.goto(`${adminBaseURL}/models`);
  await expect(page.getByRole('heading', { level: 1, name: '模型上线管理' })).toBeVisible();
  await page.getByRole('button', { name: '媒体证据 zt-image-pro' }).click();
  const dialog = page.getByRole('dialog', { name: '媒体产品证据' });
  await expect(dialog.getByText('国外模型 / C42')).toBeVisible();
  await expect(dialog.getByText('企业 40% 毛利')).toBeVisible();
  await expect(dialog.getByText('media-browser-v1')).toBeVisible();
  await expect(dialog.getByText('2 笔待对账')).toBeVisible();
  await expect(dialog.getByText('运行正常')).toBeVisible();
  await expect(dialog.getByText('成功', { exact: true })).toBeVisible();
  await expect(dialog.getByText('stop')).toBeVisible();
  await expect(dialog.getByRole('button', { name: '打开健康恢复' })).toBeVisible();
  await expect(dialog.getByRole('button', { name: '打开发布工作流' })).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await expectAdminRegionsDoNotOverlap(page);
  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/yunxin|云信号池|sk-[A-Za-z0-9]{12,}/i);
  await page.screenshot({
    path: `test-results/visual/media-admin-contract-${testInfo.project.name}.png`,
    fullPage: true,
  });

  expect(unexpected).toEqual([]);
  expect(observed.failedAPIs).toEqual([]);
  expect(observed.browserErrors).toEqual([]);
});
