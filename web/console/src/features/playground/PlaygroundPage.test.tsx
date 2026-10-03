import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { PlaygroundPage } from './PlaygroundPage';

function jsonResponse(body: unknown, status = 200, headers?: HeadersInit) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function catalogItem(modelName: string, endpoints = ['openai'], modality = 'text') {
  return {
    modality,
    model_name: modelName,
    provider_family: 'openai',
    provider_name: 'OpenAI',
    protocol: 'openai_compatible',
    enable_groups: ['default'],
    supported_endpoint_types: endpoints,
    input_price_per_million: '1.0000000000',
    output_price_per_million: modality === 'embedding' ? '0.0000000000' : '4.0000000000',
    billing_dimensions: modality === 'embedding' ? ['input_tokens'] : ['input_tokens', 'output_tokens'],
    sale_usd: modality === 'embedding'
      ? { input_tokens: '1.0000000000' }
      : { input_tokens: '1.0000000000', output_tokens: '4.0000000000' },
    billing_rule: modality === 'embedding' ? 'input_only' : 'token',
    pricing_version: 'pricing-test-v1',
  };
}

function catalogResponse() {
  const catalog = [
    catalogItem('zt-gpt-5.6-sol'),
    catalogItem('zt-gpt-responses-only', ['openai-response']),
    catalogItem('zt-text-embedding', ['embeddings'], 'embedding'),
  ];
  return {
    success: true,
    data: catalog.map((item) => item.model_name),
    catalog,
  };
}

function mediaCatalogResponse() {
  const image = {
    ...catalogItem('zt-image-2', ['images'], 'image'),
    provider_family: 'openai',
    supported_options: {
      sizes: ['1024x1024', '1536x1024'],
      qualities: ['standard', 'hd'],
      response_formats: ['url', 'b64_json'],
      min_count: 1,
      max_count: 2,
    },
    input_price_per_million: '',
    output_price_per_million: '',
    billing_dimensions: [],
    sale_usd: {},
    billing_rule: 'multi_dimension',
    billing_unit: 'per_image',
    pricing_rules: [{
      id: 'image-standard',
      conditions: { quality: 'standard' },
      billing_unit: 'per_image',
      sale_usd: { image: '0.0400000000' },
    }],
  };
  const video = {
    ...catalogItem('zt-video-2', ['video-tasks'], 'video'),
    provider_family: 'openai',
    supported_options: {
      resolutions: ['1280x720', '1920x1080'],
      duration_seconds: [5, 10],
      supports_video_input: false,
    },
    input_price_per_million: '',
    output_price_per_million: '',
    billing_dimensions: [],
    sale_usd: {},
    billing_rule: 'multi_dimension',
    billing_unit: 'per_second',
    pricing_rules: [{
      id: 'video-hd',
      conditions: { resolution: '1280x720' },
      billing_unit: 'per_second',
      sale_usd: { second: '0.1000000000' },
    }],
  };
  const catalog = [catalogItem('zt-gpt-5.6-sol'), image, video];
  return {
    success: true,
    data: catalog.map((item) => item.model_name),
    catalog,
  };
}

describe('PlaygroundPage', () => {
  beforeEach(() => {
    setAuthSession({
      access_token: 'playground-session',
      expires_in: 900,
      user: { id: 9, username: 'alice', role: 1, group: 'default' },
    });
  });

  afterEach(() => {
    clearAuthSession();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders an APIMart-style text workspace instead of the generic test form', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/text']}><PlaygroundPage initialMode="text" workbench /></MemoryRouter>);

    expect(await screen.findByRole('button', { name: '新对话' })).toBeVisible();
    expect(screen.getByText('询问 ZTAPI')).toBeVisible();
    expect(screen.getByLabelText('文本模型')).toHaveValue('zt-gpt-5.6-sol');
    expect(screen.getByRole('button', { name: '添加附件' })).toBeVisible();
    expect(screen.getByRole('button', { name: '发送' })).toBeVisible();
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('renders an image generation workspace with a model panel and recent gallery', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);

    expect(await screen.findByRole('heading', { name: '图像工作台' })).toBeVisible();
    expect(screen.getByLabelText('图片模型')).toHaveValue('zt-image-2');
    expect(screen.getByLabelText('图片提示词')).toBeVisible();
    expect(screen.getByLabelText('图片尺寸')).toBeVisible();
    expect(screen.getByLabelText('图片质量')).toBeVisible();
    expect(screen.getByText('最近生成')).toBeVisible();
    expect(screen.getByRole('button', { name: '生成图片' })).toBeVisible();
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('renders a video task workspace with capability-driven controls and a result area', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/video']}><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);

    expect(await screen.findByRole('heading', { name: '视频工作台' })).toBeVisible();
    expect(screen.getByLabelText('视频模型')).toHaveValue('zt-video-2');
    expect(screen.getByText('生成模式')).toBeVisible();
    expect(screen.getByRole('button', { name: '文生视频' })).toBeVisible();
    expect(screen.getByLabelText('视频提示词')).toBeVisible();
    expect(screen.getByLabelText('视频时长')).toBeVisible();
    expect(screen.getByText('生成结果')).toBeVisible();
    expect(screen.getByRole('button', { name: '生成视频' })).toBeVisible();
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('loads only compatible text models and runs a normally billed request', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      void init;
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      if (url.endsWith('/pg/chat/completions')) {
        return jsonResponse({
          id: 'chatcmpl-test',
          choices: [{ message: { content: '测试成功' }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 12, completion_tokens: 3, total_tokens: 15 },
        }, 200, { 'X-Request-ID': 'req-playground-1' });
      }
      if (url.includes('/api/log/self?')) {
        return jsonResponse({
          success: true,
          data: {
            page: 1,
            page_size: 1,
            total: 1,
            items: [{
              timestamp: 1_700_000_000,
              request_id: 'req-playground-1',
              model: 'zt-gpt-5.6-sol',
              status: 'success',
              latency: 1,
              prompt_tokens: 12,
              completion_tokens: 3,
              total_tokens: 15,
              billed_amount: 0.000024,
            }],
          },
        });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/test?model=zt-gpt-5.6-sol']}><PlaygroundPage /></MemoryRouter>);

    const modelSelect = await screen.findByLabelText('测试模型');
    expect(within(modelSelect).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'zt-gpt-5.6-sol',
    ]);
    expect(modelSelect).toHaveValue('zt-gpt-5.6-sol');
    expect(screen.getByRole('note', { name: '计费提醒' })).toHaveTextContent('本次测试会按正常 API 请求扣费');

    fireEvent.change(screen.getByLabelText('测试问题'), { target: { value: '请回答 35+42。' } });
    fireEvent.click(screen.getByRole('button', { name: '发送测试请求' }));

    expect(await screen.findByText('测试成功')).toBeVisible();
    expect(screen.getByText('15 tokens')).toBeVisible();
    expect(screen.getByText('0.000024 U')).toBeVisible();
    expect(screen.getByText('req-playground-1')).toBeVisible();
    expect(screen.getByRole('link', { name: '在使用日志中查看' })).toHaveAttribute(
      'href',
      '/console/logs?request_id=req-playground-1',
    );

    const relayCall = fetchMock.mock.calls.find(([input]) => input.toString().endsWith('/pg/chat/completions'));
    expect(relayCall).toBeDefined();
    const body = JSON.parse(String(relayCall?.[1]?.body));
    expect(body).toMatchObject({ model: 'zt-gpt-5.6-sol', stream: false, max_tokens: 256 });
    expect(body.messages[1]).toEqual({ role: 'user', content: '请回答 35+42。' });
  });

  it('shows safe actionable errors without exposing upstream details', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      if (url.endsWith('/pg/chat/completions')) {
        return jsonResponse({ error: { code: 'insufficient_quota', message: 'secret upstream detail' } }, 403);
      }
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/test']}><PlaygroundPage /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    fireEvent.change(screen.getByLabelText('测试问题'), { target: { value: '你好' } });
    fireEvent.click(screen.getByRole('button', { name: '发送测试请求' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('余额不足，请先充值后再测试。');
    expect(alert).not.toHaveTextContent('secret upstream detail');
  });

  it('renders integration examples with placeholders instead of real credentials', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(catalogResponse())));
    render(<MemoryRouter initialEntries={['/console/test']}><PlaygroundPage /></MemoryRouter>);

    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    const example = screen.getByTestId('playground-code');
    expect(example).toHaveTextContent('https://ztapi.vip/v1/chat/completions');
    expect(example).toHaveTextContent('$ZTAPI_API_KEY');
    expect(example).not.toHaveTextContent('playground-session');
  });

  it('runs image generation with the catalog options and renders a downloadable result', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      if (url.endsWith('/pg/images/generations')) {
        expect(JSON.parse(String(init?.body))).toMatchObject({
          model: 'zt-image-2',
          prompt: '一只在雨中的橘猫',
          size: '1536x1024',
          quality: 'hd',
          n: 1,
          response_format: 'url',
        });
        return jsonResponse({
          created: 1,
          data: [{ url: 'https://cdn.example/image-1.png', revised_prompt: '雨中的橘猫' }],
        }, 200, { 'X-Request-ID': 'req-image-1' });
      }
      if (url.includes('/api/log/self?')) return jsonResponse({ success: true, data: { page: 1, page_size: 1, total: 0, items: [] } });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/test']}><PlaygroundPage /></MemoryRouter>);
    await screen.findByRole('button', { name: '图片' });
    fireEvent.click(screen.getByRole('button', { name: '图片' }));
    expect(await screen.findByLabelText('图片模型')).toHaveValue('zt-image-2');
    fireEvent.change(screen.getByLabelText('图片提示词'), { target: { value: '一只在雨中的橘猫' } });
    fireEvent.change(screen.getByLabelText('图片尺寸'), { target: { value: '1536x1024' } });
    fireEvent.change(screen.getByLabelText('图片质量'), { target: { value: 'hd' } });
    fireEvent.click(screen.getByRole('button', { name: '生成图片' }));

    const image = await screen.findByRole('img', { name: '生成结果 1' });
    expect(image).toHaveAttribute('src', 'https://cdn.example/image-1.png');
    expect(screen.getByRole('link', { name: '下载图片 1' })).toHaveAttribute('href', 'https://cdn.example/image-1.png');
    expect(screen.getByText('req-image-1')).toBeVisible();
  });

  it('creates a video task, polls its status, and renders the completed video', async () => {
    let pollCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      if (url.endsWith('/pg/video/generations')) {
        expect(JSON.parse(String(init?.body))).toMatchObject({
          model: 'zt-video-2',
          prompt: '镜头缓慢推进一片森林',
          size: '1280x720',
          duration: 5,
        });
        return jsonResponse({ id: 'task-1', status: 'queued' }, 200, { 'X-Request-ID': 'req-video-1' });
      }
      if (url.endsWith('/pg/video/generations/task-1')) {
        pollCount += 1;
        if (pollCount === 1) return jsonResponse({ data: { task_id: 'task-1', status: 'processing', progress: '处理中' } });
        return jsonResponse({ data: { task_id: 'task-1', status: 'succeeded', result_url: 'https://cdn.example/video-1.mp4' } });
      }
      if (url.includes('/api/log/self?')) return jsonResponse({ success: true, data: { page: 1, page_size: 1, total: 0, items: [] } });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/test']}><PlaygroundPage /></MemoryRouter>);
    await screen.findByRole('button', { name: '视频' });
    fireEvent.click(screen.getByRole('button', { name: '视频' }));
    expect(await screen.findByLabelText('视频模型')).toHaveValue('zt-video-2');
    fireEvent.change(screen.getByLabelText('视频提示词'), { target: { value: '镜头缓慢推进一片森林' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));

    expect(await screen.findByText('视频任务已提交，正在等待上游处理。')).toBeVisible();
    expect(await screen.findByText('视频已生成', {}, { timeout: 4500 })).toBeVisible();
    expect(screen.getByLabelText('生成的视频')).toHaveAttribute('src', 'https://cdn.example/video-1.mp4');
    expect(screen.getByRole('link', { name: '下载视频' })).toHaveAttribute('href', 'https://cdn.example/video-1.mp4');
    expect(pollCount).toBeGreaterThanOrEqual(2);
  }, 5000);
});
