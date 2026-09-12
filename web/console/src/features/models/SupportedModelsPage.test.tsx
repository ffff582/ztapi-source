import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { SupportedModelsPage } from './SupportedModelsPage';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function catalogItem(overrides: Record<string, unknown> = {}) {
  return {
    model_name: 'zt-gpt-5.4-mini',
    provider_family: 'openai',
    provider_name: 'OpenAI',
    protocol: 'openai_compatible',
    enable_groups: ['default'],
    supported_endpoint_types: ['openai'],
    input_price_per_million: '1.2500000000',
    output_price_per_million: '5.0000000000',
    billing_dimensions: ['input_tokens', 'output_tokens'],
    sale_usd: {
      input_tokens: '1.2500000000',
      output_tokens: '5.0000000000',
    },
    billing_rule: 'token',
    pricing_version: 'pricing-test-v1',
    ...overrides,
  };
}

function successfulCatalog() {
  const catalog = [
    catalogItem(),
    catalogItem({
      model_name: 'zt-claude-sonnet-4.6',
      provider_family: 'claude',
      provider_name: 'Claude',
      protocol: 'anthropic',
      supported_endpoint_types: ['anthropic'],
      input_price_per_million: '3.0000000000',
      output_price_per_million: '15.0000000000',
      billing_dimensions: ['cache_read', 'input_tokens', 'output_tokens'],
      sale_usd: {
        cache_read: '0.0000001000',
        input_tokens: '3.0000000000',
        output_tokens: '15.0000000000',
      },
      billing_rule: 'multi_dimension',
    }),
    catalogItem({
      model_name: 'zt-gemini-2.5-pro',
      provider_family: 'gemini',
      provider_name: 'Gemini',
      protocol: 'gemini',
      supported_endpoint_types: ['gemini'],
      input_price_per_million: '1.5000000000',
      output_price_per_million: '10.0000000000',
      sale_usd: {
        input_tokens: '1.5000000000',
        output_tokens: '10.0000000000',
      },
    }),
  ];

  return {
    success: true,
    message: '',
    data: catalog.map((item) => item.model_name),
    catalog,
  };
}

function mediaCatalog() {
  const image = catalogItem({
    modality: 'image',
    model_name: 'zt-gp-image-2',
    provider_name: 'OpenAI',
    supported_endpoint_types: ['images'],
    input_price_per_million: '',
    output_price_per_million: '',
    billing_dimensions: [],
    sale_usd: {},
    billing_rule: 'multi_dimension',
    supported_options: {
      sizes: ['1024x1024'], qualities: ['standard'], response_formats: ['url'],
      min_count: 1, max_count: 2,
    },
    pricing_rules: [{
      id: 'image_output', conditions: { token_bucket: 'image_output' },
      billing_unit: 'usd_per_million_tokens', sale_usd: { image_output: '39.00' },
    }],
    billing_unit: 'usd_per_million_tokens',
  });
  const video = catalogItem({
    modality: 'video',
    model_name: 'zt-seedance-2',
    provider_family: 'seedance',
    provider_name: 'Seedance',
    supported_endpoint_types: ['video-tasks'],
    input_price_per_million: '',
    output_price_per_million: '',
    billing_dimensions: [],
    sale_usd: {},
    billing_rule: 'multi_dimension',
    supported_options: {
      resolutions: ['720p'], duration_seconds: [5], supports_video_input: false,
    },
    pricing_rules: [{
      id: '720p_video_false',
      conditions: { resolution: '720p', contains_video_input: 'false' },
      billing_unit: 'usd_per_million_tokens', sale_usd: { input_tokens: '10.4192129630' },
    }],
    billing_unit: 'usd_per_million_tokens',
  });
  return { success: true, message: '', data: [image.model_name, video.model_name], catalog: [image, video] };
}

function setClipboard(writeText: ReturnType<typeof vi.fn>) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
}

describe('SupportedModelsPage', () => {
  beforeEach(() => {
    setAuthSession({
      access_token: 'models-session-token',
      expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' },
    });
  });

  afterEach(() => {
    cleanup();
    clearAuthSession();
    Reflect.deleteProperty(navigator, 'clipboard');
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('loads the authenticated user catalog and renders routing and pricing facts', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(successfulCatalog()));
    vi.stubGlobal('fetch', fetchMock);

    render(<SupportedModelsPage />);

    const row = (await screen.findByText('zt-gpt-5.4-mini')).closest('tr');
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText('OpenAI')).toBeVisible();
    expect(within(row as HTMLElement).getByText('OpenAI 兼容')).toBeVisible();
    expect(within(row as HTMLElement).getByText('/v1/chat/completions')).toBeVisible();
    expect(within(row as HTMLElement).getByText('$1.25 / 1M tokens')).toBeVisible();
    expect(within(row as HTMLElement).getByText('$5 / 1M tokens')).toBeVisible();
    const claudeRow = screen.getByText('zt-claude-sonnet-4.6').closest('tr');
    expect(claudeRow).not.toBeNull();
    expect(within(claudeRow as HTMLElement).getByText('缓存读取')).toBeVisible();
    expect(
      within(claudeRow as HTMLElement).getByText('$0.0000001 / 1M tokens'),
    ).toBeVisible();
    const claudePrices = within(claudeRow as HTMLElement)
      .getAllByRole('listitem')
      .map((item) => item.textContent);
    expect(claudePrices).toEqual([
      '输入$3 / 1M tokens',
      '输出$15 / 1M tokens',
      '缓存读取$0.0000001 / 1M tokens',
    ]);
    expect(screen.getByText('3 个可用模型')).toBeVisible();
    const billingNote = screen.getByRole('note', { name: '用量与计费' });
    expect(billingNote).toHaveTextContent('计费以上游返回的实际用量为准');
    expect(billingNote).toHaveTextContent(
      '部分模型上游不严格遵守 max_tokens，实际输出可能超出该值并计入用量',
    );
    expect(billingNote).not.toHaveTextContent('在推理过程中');
    expect(billingNote).toHaveTextContent('Claude 全线');
    expect(billingNote).toHaveTextContent('GPT 5.4 及以上');
    expect(billingNote).toHaveTextContent('GLM / Qwen 推理系');

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [input, init] = fetchMock.mock.calls[0] as [RequestInfo | URL, RequestInit];
    expect(input.toString()).toMatch(/\/api\/user\/models$/);
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer models-session-token');
    expect(headers.get('New-Api-User')).toBe('7');
  });

  it('puts current mainstream models first and filters them with visible categories', async () => {
    const catalog = [
      catalogItem({
        model_name: 'zt-legacy-research',
        provider_family: 'other',
        provider_name: 'Other',
      }),
      catalogItem({
        model_name: 'zt-qwen-3.8-max',
        provider_family: 'qwen',
        provider_name: 'Qwen',
      }),
      catalogItem({
        model_name: 'zt-gemini-3.5-flash',
        provider_family: 'google',
        provider_name: 'Gemini',
      }),
      catalogItem({
        model_name: 'zt-claude-sonnet-5',
        provider_family: 'anthropic',
        provider_name: 'Claude',
      }),
      catalogItem({
        model_name: 'zt-deepseek-v4-pro',
        provider_family: 'deepseek',
        provider_name: 'DeepSeek',
      }),
      catalogItem({
        model_name: 'zt-kimi-k2.7-code',
        provider_family: 'moonshot',
        provider_name: 'Kimi',
      }),
      catalogItem({ model_name: 'zt-gpt-5.6-sol' }),
      catalogItem({
        modality: 'embedding',
        model_name: 'zt-text-embedding-3-small',
        supported_endpoint_types: ['embeddings'],
        input_price_per_million: '0.0260000000',
        billing_dimensions: ['input_tokens'],
        sale_usd: { input_tokens: '0.0260000000' },
        output_price_per_million: '0.0000000000',
        billing_rule: 'input_only',
      }),
    ];
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
      success: true,
      message: '',
      data: catalog.map((item) => item.model_name),
      catalog,
    })));

    render(<SupportedModelsPage />);

    await screen.findByText('zt-gpt-5.6-sol');
    const visibleModelIDs = screen
      .getAllByRole('row')
      .slice(1)
      .map((row) => within(row).getAllByRole('cell')[0].textContent?.replace('模型 ID', ''));
    expect(visibleModelIDs).toEqual([
      'zt-gpt-5.6-sol',
      'zt-claude-sonnet-5',
      'zt-gemini-3.5-flash',
      'zt-deepseek-v4-pro',
      'zt-qwen-3.8-max',
      'zt-kimi-k2.7-code',
      'zt-legacy-research',
      'zt-text-embedding-3-small',
    ]);

    const categories = screen.getByRole('tablist', { name: '模型分类' });
    expect(within(categories).getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
      '全部', 'OpenAI', 'Claude', 'Gemini', '国产模型', '向量模型',
    ]);

    fireEvent.click(within(categories).getByRole('tab', { name: '国产模型' }));
    expect(screen.getByText('zt-deepseek-v4-pro')).toBeVisible();
    expect(screen.getByText('zt-qwen-3.8-max')).toBeVisible();
    expect(screen.getByText('zt-kimi-k2.7-code')).toBeVisible();
    expect(screen.queryByText('zt-gpt-5.6-sol')).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('搜索模型'), {
      target: { value: 'qwen' },
    });
    expect(screen.getByText('zt-qwen-3.8-max')).toBeVisible();
    expect(screen.queryByText('zt-deepseek-v4-pro')).not.toBeInTheDocument();
  });

  it('renders media endpoints, supported options, and conditional sale prices', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(mediaCatalog())));
    render(<SupportedModelsPage />);

    const imageRow = (await screen.findByText('zt-gp-image-2')).closest('tr') as HTMLElement;
    expect(within(imageRow).getByText('图片生成')).toBeVisible();
    expect(within(imageRow).getByText('/v1/images/generations')).toBeVisible();
    expect(within(imageRow).getByText(/1024x1024/)).toBeVisible();
    expect(within(imageRow).getByText(/图片输出/)).toBeVisible();
    expect(within(imageRow).getByText('$39 / 1M tokens')).toBeVisible();

    const videoRow = screen.getByText('zt-seedance-2').closest('tr') as HTMLElement;
    expect(within(videoRow).getByText('异步视频任务')).toBeVisible();
    expect(within(videoRow).getByText('/v1/video/generations')).toBeVisible();
    expect(within(videoRow).getByText('720p · 5 秒 · 无视频输入')).toBeVisible();
    expect(within(videoRow).getByText('$10.419212963 / 1M tokens')).toBeVisible();
  });

  it('searches, filters, and copies the exact public model ID', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard(writeText);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(successfulCatalog())));

    render(<SupportedModelsPage />);
    await screen.findByText('zt-gpt-5.4-mini');

    fireEvent.change(screen.getByLabelText('搜索模型'), {
      target: { value: 'claude' },
    });
    expect(screen.getByText('zt-claude-sonnet-4.6')).toBeVisible();
    expect(screen.queryByText('zt-gpt-5.4-mini')).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('搜索模型'), {
      target: { value: '' },
    });
    fireEvent.click(screen.getByRole('tab', { name: 'Gemini' }));
    expect(screen.getByText('zt-gemini-2.5-pro')).toBeVisible();
    expect(screen.queryByText('zt-claude-sonnet-4.6')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: '全部' }));
    fireEvent.click(screen.getByRole('button', { name: '复制 zt-gpt-5.4-mini' }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith('zt-gpt-5.4-mini'),
    );
    expect(screen.getByRole('status')).toHaveTextContent(
      'zt-gpt-5.4-mini 已复制',
    );

    fireEvent.click(screen.getByRole('tab', { name: 'Gemini' }));
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('renders Responses-only and Chat models together using endpoint metadata, not model names', async () => {
    const response = successfulCatalog();
    response.catalog.push(catalogItem({ model_name: 'zt-gpt-5.4-pro', supported_endpoint_types: ['openai-response'] }));
    response.catalog.push(catalogItem({ model_name: 'friendly-research', supported_endpoint_types: ['openai-response'] }));
    response.data = response.catalog.map((item) => item.model_name);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(response)));
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard(writeText);
    render(<SupportedModelsPage />);

    const pro = (await screen.findByText('zt-gpt-5.4-pro')).closest('tr') as HTMLElement;
    expect(within(pro).getByText('OpenAI Responses')).toBeVisible();
    expect(within(pro).getByText('/v1/responses')).toBeVisible();
    expect(within(pro).queryByText('/v1/chat/completions')).not.toBeInTheDocument();
    const chat = screen.getByText('zt-gpt-5.4-mini').closest('tr') as HTMLElement;
    expect(within(chat).getByText('/v1/chat/completions')).toBeVisible();
    expect(screen.getByText('/v1/messages')).toBeVisible();
    expect(screen.getByText('/v1beta/models/{model}:generateContent')).toBeVisible();
    fireEvent.click(within(pro).getByRole('button', { name: '复制 zt-gpt-5.4-pro' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('zt-gpt-5.4-pro'));
    fireEvent.change(screen.getByLabelText('搜索模型'), { target: { value: 'Responses' } });
    expect(screen.getByText('friendly-research')).toBeVisible();
    expect(screen.getByText('zt-gpt-5.4-pro')).toBeVisible();
    expect(screen.queryByText('zt-gpt-5.4-mini')).not.toBeInTheDocument();
  });

  it('renders a mixed 39-model catalog with embedding routes and only input prices', async () => {
    const catalog = Array.from({ length: 37 }, (_, index) => catalogItem({
      model_name: `text-fixture-${index}`, supported_endpoint_types: [index % 2 ? 'openai' : 'openai-response'],
    }));
    for (const [name, price] of [['zt-text-embedding-ada-002', '0.1300000000'], ['zt-text-embedding-3-small', '0.0260000000']]) {
      catalog.push(catalogItem({
        model_name: name, modality: 'embedding', supported_endpoint_types: ['embeddings'],
        input_price_per_million: price, output_price_per_million: '0.0000000000',
        billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: price }, billing_rule: 'input_only',
      }));
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
      success: true, data: catalog.map((item) => item.model_name), catalog,
    })));
    render(<SupportedModelsPage />);
    expect(await screen.findByText('39 个可用模型')).toBeVisible();
    for (const [name, price] of [['zt-text-embedding-ada-002', '$0.13 / 1M tokens'], ['zt-text-embedding-3-small', '$0.026 / 1M tokens']]) {
      const row = screen.getByText(name).closest('tr') as HTMLElement;
      expect(within(row).getByText('POST /v1/embeddings')).toBeVisible();
      expect(within(row).getByText('文本向量 Embeddings')).toBeVisible();
      expect(within(row).getByText(price)).toBeVisible();
      expect(within(row).getAllByRole('listitem')).toHaveLength(1);
      expect(within(row).queryByText('输出')).not.toBeInTheDocument();
    }
    fireEvent.change(screen.getByLabelText('搜索模型'), { target: { value: 'Embeddings' } });
    expect(screen.getByText('zt-text-embedding-3-small')).toBeVisible();
    expect(screen.queryByText('text-fixture-0')).not.toBeInTheDocument();
  });

  it.each([
    {
      caseName: 'a billed dimension has no sale price',
      overrides: { sale_usd: { input_tokens: '1.2500000000' } },
    },
    {
      caseName: 'billing dimensions contain a duplicate',
      overrides: {
        billing_dimensions: ['input_tokens', 'output_tokens', 'output_tokens'],
      },
    },
    {
      caseName: 'sale prices contain an unbilled dimension',
      overrides: {
        sale_usd: {
          input_tokens: '1.2500000000',
          output_tokens: '5.0000000000',
          request: '0.1000000000',
        },
      },
    },
    {
      caseName: 'the input summary disagrees with the dimensional price',
      overrides: { input_price_per_million: '2.0000000000' },
    },
    {
      caseName: 'the billing rule disagrees with its dimensions',
      overrides: { billing_rule: 'multi_dimension' },
    },
  ])('rejects a catalog when $caseName', async ({ overrides }) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({
          success: true,
          message: '',
          data: ['zt-gpt-5.4-mini'],
          catalog: [catalogItem(overrides)],
        }),
      ),
    );

    render(<SupportedModelsPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '模型目录加载失败，请刷新后重试。',
    );
  });

  it('shows an explicit error instead of rendering malformed catalog data', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({
          ...successfulCatalog(),
          catalog: [catalogItem({ protocol: 'unknown' })],
          data: ['zt-gpt-5.4-mini'],
        }),
      ),
    );

    render(<SupportedModelsPage />);

    expect(
      await screen.findByRole('alert'),
    ).toHaveTextContent('模型目录加载失败，请刷新后重试。');
    expect(screen.queryByText('zt-gpt-5.4-mini')).not.toBeInTheDocument();
  });
});
