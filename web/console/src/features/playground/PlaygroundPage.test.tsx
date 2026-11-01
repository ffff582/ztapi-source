import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from '../../api/client';
import { PlaygroundPage } from './PlaygroundPage';

function jsonResponse(body: unknown, status = 200, headers?: HeadersInit) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function keyMetadata(overrides: Record<string, unknown> = {}) {
  return {
    id: 15, user_id: 9, status: 1, name: 'workbench', created_time: 1, accessed_time: 0,
    expired_time: -1, remain_quota: 100, unlimited_quota: true,
    model_limits_enabled: false, model_limits: '', allow_ips: '', key_prefix: 'sk-zt-safe...',
    ...overrides,
  };
}

function keyResponse(items: unknown[] = [keyMetadata()]) {
  return jsonResponse({ success: true, data: { page: 1, page_size: 100, total: items.length, items } });
}

function stubFetch(_name: 'fetch', handler: typeof fetch) {
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) =>
    input.toString().startsWith('/api/token/') ? Promise.resolve(keyResponse()) : handler(input, init));
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
      render_options: [
        { aspect_ratio: '1:1', resolution: '1K', size: '1024x1024', quality: 'standard' },
        { aspect_ratio: '3:2', resolution: '2K', size: '1536x1024', quality: 'hd' },
      ],
      response_formats: ['url', 'b64_json'],
      min_count: 1,
      max_count: 10,
      supports_edits: true,
      edit_input: {
        max_files: 15,
        max_bytes: 20 * 1024 * 1024,
        max_total_bytes: 256 * 1024 * 1024,
        mime_types: ['image/jpeg', 'image/png', 'image/webp'],
      },
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

function mediaCatalogWithoutEditsResponse() {
  const response = mediaCatalogResponse();
  return {
    ...response,
    catalog: response.catalog.map((item) => {
      if (item.model_name !== 'zt-image-2' || !('supported_options' in item)) return item;
      return { ...item, supported_options: { ...item.supported_options, supports_edits: false, edit_input: undefined } };
    }),
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
    cleanup();
    clearAuthSession();
    window.localStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it.each([
    ['none', [], /没有可用 Key/],
    ['expired', [keyMetadata({ expired_time: 1 })], /Key 已过期/],
    ['expired status', [keyMetadata({ status: 3 })], /Key 已过期/],
    ['disabled', [keyMetadata({ status: 2 })], /Key 已停用/],
    ['exhausted', [keyMetadata({ unlimited_quota: false, remain_quota: 0 })], /Key 额度已用尽/],
  ])('blocks all paid workbench submissions with %s metadata', async (_name, items, message) => {
    const paid = vi.fn();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url === '/api/token/?p=1&page_size=100') return keyResponse(items);
      paid(url, init);
      throw new Error('No paid requests allowed');
    }));
    const page = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await waitFor(() => expect(screen.getByLabelText('Key 状态')).toHaveTextContent(message));
    expect(screen.getByRole('link', { name: '管理 Keys' })).toHaveAttribute('href', '/console/keys');
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'synthetic prompt' } });
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
    fireEvent.submit(screen.getByLabelText('输入消息').closest('form')!);
    for (const mode of ['image', 'video'] as const) {
      page.rerender(<MemoryRouter><PlaygroundPage initialMode={mode} workbench /></MemoryRouter>);
      const prompt = await screen.findByLabelText(mode === 'image' ? '图片提示词' : '视频提示词');
      fireEvent.change(prompt, { target: { value: 'synthetic prompt' } });
      expect(screen.getByRole('button', { name: mode === 'image' ? '生成图片' : '生成视频' })).toBeDisabled();
      fireEvent.submit(prompt.closest('form')!);
    }
    expect(paid).not.toHaveBeenCalled();
  });

  it('keeps paid actions blocked while Key metadata is loading or failed, with a GET retry', async () => {
    let finish!: (response: Response) => void;
    let requests = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe('GET');
      if (input.toString() === '/api/user/models') return jsonResponse(catalogResponse());
      if (input.toString().startsWith('/api/token/')) {
        requests += 1;
        return requests === 1 ? new Promise<Response>((resolve) => { finish = resolve; }) : keyResponse();
      }
      throw new Error('Unexpected request');
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    expect(await screen.findByLabelText('Key 状态')).toHaveTextContent('正在检查 Key');
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'synthetic prompt' } });
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
    await act(async () => { finish(jsonResponse({ success: false, message: 'sensitive error' }, 503)); });
    expect(screen.getByLabelText('Key 状态')).toHaveTextContent('Key 状态检查失败');
    expect(screen.queryByText('sensitive error')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '刷新 Key 状态' }));
    await waitFor(() => expect(screen.getByRole('button', { name: '发送' })).toBeEnabled());
    expect(screen.getByLabelText('Key 状态')).toHaveTextContent('可用 Key');
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });

  it('revalidates Key metadata before sending and never dispatches after revocation', async () => {
    let revoked = false;
    const paid = vi.fn();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse(catalogResponse());
      if (input.toString().startsWith('/api/token/')) return keyResponse([keyMetadata({ status: revoked ? 2 : 1 })]);
      paid();
      return jsonResponse({ choices: [{ message: { content: 'must not send' } }] });
    }));
    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByLabelText('输入消息');
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'synthetic prompt' } });
    await waitFor(() => expect(screen.getByRole('button', { name: '发送' })).toBeEnabled());
    revoked = true;
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(screen.getByLabelText('Key 状态')).toHaveTextContent('Key 已停用'));
    expect(paid).not.toHaveBeenCalled();
    expect(screen.getByLabelText('输入消息')).toHaveValue('synthetic prompt');
  });

  it('reports unavailable requested models instead of silently sending to a different model', async () => {
    stubFetch('fetch', vi.fn(async () => jsonResponse(catalogResponse())));
    render(<MemoryRouter initialEntries={['/console/workbench/text?model=zt-gpt-responses-only']}><PlaygroundPage workbench /></MemoryRouter>);
    expect(await screen.findByText('所选模型不支持当前工作台或已不可用。')).toBeVisible();
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'synthetic prompt' } });
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
    expect(screen.getByLabelText('文本模型')).toHaveValue('zt-gpt-responses-only');
  });

  it('renders text catalog failures visibly instead of leaving an unexplained empty selector', async () => {
    stubFetch('fetch', vi.fn(async () => jsonResponse({ success: false }, 503)));
    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    expect(await screen.findByRole('alert')).toHaveTextContent('可测试模型加载失败');
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
  });

  it('persists only an account-owned video reference and resumes with GET after refresh', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url.startsWith('/api/token/')) return keyResponse();
      if (url === '/pg/video/generations') return jsonResponse({ id: 'resume-task', status: 'queued' });
      if (url === '/pg/video/generations/resume-task') return jsonResponse({ code: 'success', data: { task_id: 'resume-task', user_id: 9, status: 'SUCCESS', result_url: 'https://example.test/resumed.mp4' } });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    const page = render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    const prompt = await screen.findByLabelText('视频提示词');
    await waitFor(() => expect(screen.getByLabelText('Key 状态')).toHaveTextContent('可用 Key'));
    fireEvent.change(prompt, { target: { value: 'private synthetic video prompt' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));
    await screen.findByText('resume-task');
    expect(JSON.parse(localStorage.getItem('ztapi:workbench:video-task:v1:9')!)).toEqual({ task_id: 'resume-task', model: 'zt-video-2' });
    page.unmount();
    fetchMock.mockClear();
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    expect(await screen.findByLabelText('生成的视频')).toHaveAttribute('src', 'https://example.test/resumed.mp4');
    expect(fetchMock.mock.calls.some(([input]) => input.toString() === '/pg/video/generations')).toBe(false);
    expect(screen.getByLabelText('视频提示词')).toHaveValue('');
  });

  it('retains the last confirmed video status on fetch failure and retries GET without another paid POST', async () => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', JSON.stringify({ task_id: 'pending-task', model: 'retired-video' }));
    let failed = true;
    const paid = vi.fn();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url.startsWith('/api/token/')) return keyResponse([keyMetadata({ unlimited_quota: false, remain_quota: 0 })]);
      if (url.endsWith('/pending-task') && init?.method === 'GET') {
        if (failed) throw new Error('upstream secret');
        return jsonResponse({ code: 'success', data: { task_id: 'pending-task', user_id: 9, status: 'FAILURE', fail_reason: 'upstream secret' } });
      }
      paid();
      throw new Error('No POST allowed');
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    expect(await screen.findByText('视频状态查询失败，任务未重新提交。')).toBeVisible();
    expect(screen.queryByText('upstream secret')).not.toBeInTheDocument();
    expect(screen.queryByText('视频生成失败')).not.toBeInTheDocument();
    expect(screen.getByText('retired-video')).toBeVisible();
    expect(screen.getByText('任务状态未知，请手动查询。')).toBeVisible();
    failed = false;
    fireEvent.click(screen.getByRole('button', { name: '重试查询状态' }));
    expect(await screen.findByText('视频生成失败')).toBeVisible();
    expect(paid).not.toHaveBeenCalled();
    expect(screen.queryByText('upstream secret')).not.toBeInTheDocument();
  });

  it('never fetches or renders another account video reference or late result', async () => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', JSON.stringify({ task_id: 'alice-task', model: 'zt-video-2' }));
    let finish!: (response: Response) => void;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url.startsWith('/api/token/')) return keyResponse([keyMetadata({ user_id: getAuthSession()?.user.id })]);
      if (url.endsWith('/alice-task')) return new Promise<Response>((resolve) => { finish = resolve; });
      throw new Error('Unexpected request');
    });
    vi.stubGlobal('fetch', fetchMock);
    const page = render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    await waitFor(() => expect(finish).toBeTypeOf('function'));
    act(() => setAuthSession({ access_token: 'bob', expires_in: 900, user: { id: 10, username: 'bob', role: 1, group: 'default' } }));
    await act(async () => { finish(jsonResponse({ data: { task_id: 'alice-task', user_id: 9, status: 'SUCCESS', result_url: 'https://example.test/alice.mp4' } })); });
    expect(screen.queryByText('alice-task')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('生成的视频')).not.toBeInTheDocument();
    expect(localStorage.getItem('ztapi:workbench:video-task:v1:10')).toBeNull();
    page.unmount();
    fetchMock.mockClear();
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    await screen.findByLabelText('视频提示词');
    expect(fetchMock.mock.calls.some(([input]) => input.toString().includes('/alice-task'))).toBe(false);
  });

  it('keeps a successful video submission visible when persistence fails', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked storage'); });
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url.startsWith('/api/token/')) return keyResponse();
      return jsonResponse({ task_id: 'memory-task', status: 'succeeded', result_url: 'https://example.test/video.mp4' });
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));
    expect(await screen.findByLabelText('生成的视频')).toHaveAttribute('src', 'https://example.test/video.mp4');
    expect(screen.getByText('视频任务引用未能保存，刷新后可能无法续查。')).toBeVisible();
  });

  it('does not offer unimplemented video input modes as clickable features', async () => {
    const response = mediaCatalogResponse();
    const video = response.catalog.find((item) => item.modality === 'video')!;
    if ('supported_options' in video && video.supported_options && 'supports_video_input' in video.supported_options) video.supported_options.supports_video_input = true;
    stubFetch('fetch', vi.fn(async () => jsonResponse(response)));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    await screen.findByLabelText('视频模型');
    expect(screen.queryByRole('button', { name: '关键帧' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '视频续写' })).not.toBeInTheDocument();
    expect(screen.getByText('当前工作台仅支持文生视频，参考视频输入暂未接入。')).toBeVisible();
  });

  it('treats an interrupted video POST as unconfirmed rather than inviting a blind paid retry', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      throw new Error('connection lost after submit');
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('视频提交结果未确认，请先查看使用日志，避免重复提交。');
    expect(screen.getByRole('link', { name: '查看使用日志' })).toHaveAttribute('href', '/console/logs');
  });

  it('keeps video task lookup available when the model catalog fails', async () => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', JSON.stringify({ task_id: 'catalog-independent', model: 'old-model' }));
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse({ success: false }, 503);
      return jsonResponse({ code: 'success', data: { task_id: 'catalog-independent', status: 'SUCCESS' } });
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    expect(await screen.findByText('任务已完成，暂未返回可用视频链接。')).toBeVisible();
    expect(screen.getByText('old-model')).toBeVisible();
    expect(screen.getByRole('button', { name: '重试查询状态' })).toBeEnabled();
  });

  it('pauses unknown video states rather than automatically polling forever', async () => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', JSON.stringify({ task_id: 'unknown-task', model: 'zt-video-2' }));
    let lookups = 0;
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      lookups += 1;
      return jsonResponse({ code: 'success', data: { task_id: 'unknown-task', status: 'FUTURE_STATUS' } });
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    await waitFor(() => expect(lookups).toBe(1));
    expect(screen.getByText('任务状态未知，请手动查询。')).toBeVisible();
    vi.useFakeTimers();
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(lookups).toBe(1);
    expect(screen.getByRole('button', { name: '重试查询状态' })).toBeEnabled();
  });

  it('retains queued video state after polling fails and does not create another task', async () => {
    let lookups = 0;
    let submissions = 0;
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (input.toString() === '/pg/video/generations') {
        submissions += 1;
        return jsonResponse({ id: 'queued-task', status: 'queued' });
      }
      lookups += 1;
      throw new Error('temporary query failure');
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    vi.useFakeTimers();
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '生成视频' })); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1200); });
    expect(screen.getByText('视频状态查询失败，任务未重新提交。')).toBeVisible();
    expect(screen.getByText('视频任务已提交，正在等待上游处理。')).toBeVisible();
    expect(screen.getByRole('button', { name: '生成视频' })).toBeDisabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(lookups).toBe(1);
    expect(submissions).toBe(1);
  });

  it('does not display unsafe video URLs or raw provider errors from a create response', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      return jsonResponse({ id: 'unsafe-task', status: 'failed', fail_reason: 'private upstream credential', progress: 'private upstream credential', result_url: 'javascript:alert(1)' });
    }));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));
    await screen.findByText('unsafe-task');
    expect(screen.queryByText('private upstream credential')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '下载视频' })).not.toBeInTheDocument();
  });

  it('blocks video creation when the catalog does not provide supported parameters', async () => {
    const response = mediaCatalogResponse();
    response.catalog = response.catalog.map((item) => item.modality === 'video' ? { ...item, supported_options: undefined } : item) as typeof response.catalog;
    stubFetch('fetch', vi.fn(async () => jsonResponse(response)));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    expect(await screen.findByRole('alert')).toHaveTextContent('可测试模型加载失败');
    expect(screen.queryByRole('button', { name: '生成视频' })).not.toBeInTheDocument();
  });

  it('keeps unavailable media model selection visible and lets the user choose a supported model', async () => {
    stubFetch('fetch', vi.fn(async () => jsonResponse(mediaCatalogResponse())));
    render(<MemoryRouter initialEntries={['/console/workbench/video?model=retired-video']}><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    expect(await screen.findByText('所选模型不支持当前工作台或已不可用。')).toBeVisible();
    expect(screen.getByLabelText('视频模型')).toHaveValue('retired-video');
    fireEvent.change(screen.getByLabelText('视频模型'), { target: { value: 'zt-video-2' } });
    fireEvent.change(screen.getByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    expect(screen.getByRole('button', { name: '生成视频' })).toBeEnabled();
  });

  it('expires a loaded Key locally without enabling another paid action or automatic requests', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-10-31T00:00:00Z'));
    const now = Math.floor(Date.now() / 1000);
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => input.toString() === '/api/user/models' ? jsonResponse(catalogResponse()) : keyResponse([keyMetadata({ expired_time: now })]));
    vi.stubGlobal('fetch', fetchMock);
    await act(async () => { render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>); });
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'synthetic prompt' } });
    expect(screen.getByRole('button', { name: '发送' })).toBeEnabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByLabelText('Key 状态')).toHaveTextContent('Key 已过期');
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('does not load an old video billing result into another modality after clearing its result', async () => {
    let logs = 0;
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url === '/api/user/models') return jsonResponse(mediaCatalogResponse());
      if (url.includes('/api/log/self?')) {
        logs += 1;
        return jsonResponse({ success: true, data: { page: 1, page_size: 1, total: 0, items: [] } });
      }
      return jsonResponse({ task_id: 'completed-task', status: 'succeeded', result_url: 'https://example.test/video.mp4' }, 200, { 'X-Request-ID': 'video-request' });
    }));
    render(<MemoryRouter><PlaygroundPage /></MemoryRouter>);
    fireEvent.click(await screen.findByRole('button', { name: '视频' }));
    fireEvent.change(screen.getByLabelText('视频提示词'), { target: { value: 'synthetic prompt' } });
    fireEvent.click(screen.getByRole('button', { name: '生成视频' }));
    await screen.findByLabelText('生成的视频');
    await act(async () => { await Promise.resolve(); });
    logs = 0;
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '文本' })); });
    expect(logs).toBe(0);
  });

  it('uses the selected catalog video parameters in its example instead of inventing defaults', async () => {
    const response = mediaCatalogResponse();
    for (const item of response.catalog) {
      if ('supported_options' in item && item.supported_options && 'duration_seconds' in item.supported_options) {
        item.supported_options.duration_seconds = [10];
        item.supported_options.resolutions = ['1920x1080'];
      }
    }
    stubFetch('fetch', vi.fn(async () => jsonResponse(response)));
    render(<MemoryRouter><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);
    await screen.findByLabelText('视频提示词');
    expect(screen.getByTestId('playground-code')).toHaveTextContent('"duration": 10');
    expect(screen.getByTestId('playground-code')).toHaveTextContent('"size": "1920x1080"');
  });

  it('does not offer runnable code for an unsupported selected model', async () => {
    stubFetch('fetch', vi.fn(async () => jsonResponse(catalogResponse())));
    render(<MemoryRouter initialEntries={['/?model=zt-gpt-responses-only']}><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByText('所选模型不支持当前工作台或已不可用。');
    expect(screen.getByRole('button', { name: '复制代码' })).toBeDisabled();
    expect(screen.getByTestId('playground-code')).not.toHaveTextContent('curl');
  });

  it('renders an APIMart-style text workspace instead of the generic test form', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/text']}><PlaygroundPage initialMode="text" workbench /></MemoryRouter>);

    expect(await screen.findByRole('button', { name: '新对话' })).toBeVisible();
    expect(screen.getByText('询问 ZTAPI')).toBeVisible();
    expect(screen.getByLabelText('文本模型')).toHaveValue('zt-gpt-5.6-sol');
    expect(screen.getByLabelText('输入消息')).toHaveValue('');
    expect(screen.getByRole('button', { name: '添加附件' })).toBeVisible();
    expect(screen.getByRole('button', { name: '发送' })).toBeVisible();
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('creates a text conversation only after sending and restores it after refresh', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      if (url.endsWith('/pg/chat/completions')) {
        return jsonResponse({
          id: 'chatcmpl-history',
          choices: [{ message: { content: '已保存的回复' }, finish_reason: 'stop' }],
        }, 200, { 'X-Request-ID': 'req-history-1' });
      }
      if (url.includes('/api/log/self?')) {
        return jsonResponse({ success: true, data: { page: 1, page_size: 1, total: 0, items: [] } });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    stubFetch('fetch', fetchMock);

    const firstRender = render(<MemoryRouter initialEntries={['/console/workbench/text']}><PlaygroundPage initialMode="text" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('输入消息'), { target: { value: '第一次提问' } });
    expect(screen.queryByRole('button', { name: '第一次提问' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    expect(await screen.findByText('已保存的回复')).toBeVisible();
    expect(screen.getByRole('button', { name: '第一次提问' })).toBeVisible();
    expect(screen.getByLabelText('输入消息')).toHaveValue('');

    firstRender.unmount();
    render(<MemoryRouter initialEntries={['/console/workbench/text']}><PlaygroundPage initialMode="text" workbench /></MemoryRouter>);
    expect(await screen.findByRole('button', { name: '第一次提问' })).toBeVisible();
    expect(screen.getByText('已保存的回复')).toBeVisible();
  });

  it('keeps billing metrics and request ids out of the text conversation', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      if (url.endsWith('/pg/chat/completions')) {
        return jsonResponse({
          id: 'chatcmpl-workbench',
          choices: [{ message: { content: '工作台回复' }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 12, completion_tokens: 3, total_tokens: 15 },
        }, 200, { 'X-Request-ID': 'req-workbench-1' });
      }
      if (url.includes('/api/log/self?')) {
        return jsonResponse({
          success: true,
          data: {
            page: 1,
            page_size: 1,
            total: 1,
            items: [{
              request_id: 'req-workbench-1',
              total_tokens: 15,
              billed_amount: 0.000024,
            }],
          },
        });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    stubFetch('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/workbench/text']}><PlaygroundPage initialMode="text" workbench /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText('输入消息'), { target: { value: '你好' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));

    expect(await screen.findByText('工作台回复')).toBeVisible();
    expect(screen.queryByText('15 tokens')).not.toBeInTheDocument();
    expect(screen.queryByText('0.000024 U')).not.toBeInTheDocument();
    expect(screen.queryByText('req-workbench-1')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '在使用日志中查看' })).not.toBeInTheDocument();
  });

  it('sends restored context and starts a new conversation without carrying old turns', async () => {
    const submitted: Array<{ messages: Array<{ role: string; content: string }> }> = [];
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      if (url.endsWith('/pg/chat/completions')) {
        submitted.push(JSON.parse(String(init?.body)));
        return jsonResponse({ choices: [{ message: { content: `Answer ${submitted.length}` }, finish_reason: 'stop' }] });
      }
      throw new Error(`Unexpected request: ${url}`);
    }));
    const first = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'Remember Atlas' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await screen.findByText('Answer 1');
    first.unmount();

    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    expect(screen.getByText('Answer 1')).toBeVisible();
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'What did I say?' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await screen.findByText('Answer 2');
    expect(submitted[1].messages).toEqual([
      { role: 'system', content: '你是一个简洁、可靠的助手。' },
      { role: 'user', content: 'Remember Atlas' },
      { role: 'assistant', content: 'Answer 1' },
      { role: 'user', content: 'What did I say?' },
    ]);

    fireEvent.click(screen.getByRole('button', { name: '新对话' }));
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'Fresh topic' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await screen.findByText('Answer 3');
    expect(submitted[2].messages).toEqual([
      { role: 'system', content: '你是一个简洁、可靠的助手。' },
      { role: 'user', content: 'Fresh topic' },
    ]);
  });

  it('never writes another account history during a mounted account change', async () => {
    const key = (id: number) => `ztapi:workbench:text-conversations:v1:${id}`;
    const conversation = (name: string) => [{
      id: name, title: name, model: 'zt-gpt-5.6-sol', updatedAt: 1,
      messages: [{ role: 'user', content: `${name} private question` }, { role: 'assistant', content: `${name} private answer` }],
    }];
    window.localStorage.setItem(key(9), JSON.stringify(conversation('Alice')));
    window.localStorage.setItem(key(10), JSON.stringify(conversation('Bob')));
    const writes: Array<{ key: string; value: string }> = [];
    const setItem = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key, value) {
      writes.push({ key, value });
      setItem.call(this, key, value);
    });
    stubFetch('fetch', vi.fn(async () => jsonResponse(catalogResponse())));
    const view = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    expect(await screen.findByText('Alice private answer')).toBeVisible();
    act(() => setAuthSession({ access_token: 'bob', expires_in: 900, user: { id: 10, username: 'bob', role: 1, group: 'default' } }));
    view.rerender(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    expect(await screen.findByText('Bob private answer')).toBeVisible();
    expect(screen.queryByText('Alice private answer')).not.toBeInTheDocument();
    expect(writes.filter((write) => write.key === key(10)).every((write) => !write.value.includes('Alice'))).toBe(true);
    expect(JSON.parse(window.localStorage.getItem(key(10))!)).toEqual(conversation('Bob'));
  });

  it('discards a response arriving after the signed-in account has changed', async () => {
    let resolveReply!: (value: Response) => void;
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString().endsWith('/api/user/models')) return jsonResponse(catalogResponse());
      return new Promise<Response>((resolve) => { resolveReply = resolve; });
    }));
    const view = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'Alice private question' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(resolveReply).toBeTypeOf('function'));
    act(() => setAuthSession({ access_token: 'bob', expires_in: 900, user: { id: 10, username: 'bob', role: 1, group: 'default' } }));
    view.rerender(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await act(async () => resolveReply(jsonResponse({ choices: [{ message: { content: 'Alice private answer' }, finish_reason: 'stop' }] })));
    expect(screen.queryByText('Alice private answer')).not.toBeInTheDocument();
    expect(window.localStorage.getItem('ztapi:workbench:text-conversations:v1:10')).not.toContain('Alice');
    expect(screen.getByLabelText('输入消息')).toHaveValue('');
    expect(screen.queryByText('发送中')).not.toBeInTheDocument();
  });

  it('keeps a successful reply visible and warns when browser history cannot be saved', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('Full', 'QuotaExceededError'); });
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/user/models')
      ? jsonResponse(catalogResponse())
      : jsonResponse({ choices: [{ message: { content: 'Unsaved answer' }, finish_reason: 'stop' }] })));
    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'Save this' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    expect(await screen.findByText('Unsaved answer')).toBeVisible();
    expect(screen.getByRole('status', { name: '聊天记录保存状态' })).toHaveTextContent('聊天记录未能保存到此浏览器，刷新后可能丢失。');
    expect(screen.getByLabelText('输入消息')).toHaveValue('');
  });

  it.each(['corrupt JSON', 'read denied'])('preserves unreadable history without overwriting it: %s', async (failure) => {
    const key = 'ztapi:workbench:text-conversations:v1:9';
    window.localStorage.setItem(key, 'original unreadable data');
    const getItem = Storage.prototype.getItem;
    if (failure === 'read denied') {
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (this: Storage, name) {
        if (name === key) throw new DOMException('Denied', 'SecurityError');
        return getItem.call(this, name);
      });
    }
    stubFetch('fetch', vi.fn(async () => jsonResponse(catalogResponse())));
    render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    expect(getItem.call(window.localStorage, key)).toBe('original unreadable data');
    expect(screen.getByRole('status', { name: '聊天记录保存状态' })).toHaveTextContent('此浏览器的聊天记录暂时无法读取，原记录未覆盖。');
  });

  it.each(['account', 'new conversation'])('does not append a late attachment after changing %s', async (change) => {
    stubFetch('fetch', vi.fn(async () => jsonResponse(catalogResponse())));
    const view = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    let resolveText!: (value: string) => void;
    const file = new File(['private'], 'alice.txt', { type: 'text/plain' });
    Object.defineProperty(file, 'text', { value: () => new Promise<string>((resolve) => { resolveText = resolve; }) });
    fireEvent.change(view.container.querySelector('input[accept=".txt,.md,.json,.csv"]')!, { target: { files: [file] } });
    if (change === 'account') {
      act(() => setAuthSession({ access_token: 'bob', expires_in: 900, user: { id: 10, username: 'bob', role: 1, group: 'default' } }));
    } else {
      fireEvent.click(screen.getByRole('button', { name: '新对话' }));
    }
    await act(async () => resolveText('Alice private attachment'));
    expect(screen.getByLabelText('输入消息')).toHaveValue('');
    expect(screen.queryByText('alice.txt')).not.toBeInTheDocument();
  });

  it('preserves an unsaved in-memory reply when changing workbench modes', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('Full', 'QuotaExceededError'); });
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/user/models')
      ? jsonResponse(mediaCatalogResponse())
      : jsonResponse({ choices: [{ message: { content: 'Unsaved mode answer' }, finish_reason: 'stop' }] })));
    const view = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByRole('option', { name: 'zt-gpt-5.6-sol' });
    fireEvent.change(screen.getByLabelText('输入消息'), { target: { value: 'Keep in memory' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));
    await screen.findByText('Unsaved mode answer');
    await act(async () => view.rerender(<MemoryRouter><PlaygroundPage initialMode="image" workbench /></MemoryRouter>));
    await screen.findByLabelText('图片提示词');
    await act(async () => view.rerender(<MemoryRouter><PlaygroundPage initialMode="text" workbench /></MemoryRouter>));
    await screen.findByLabelText('输入消息');
    expect(screen.getByText('Unsaved mode answer')).toBeVisible();
  });

  it('does not rewrite externally unreadable storage on a mode round trip', async () => {
    const key = 'ztapi:workbench:text-conversations:v1:9';
    window.localStorage.setItem(key, JSON.stringify([{ id: 'saved', title: 'Saved', model: 'zt-gpt-5.6-sol', updatedAt: 1,
      messages: [{ role: 'user', content: 'Saved question' }, { role: 'assistant', content: 'Saved answer' }] }]));
    stubFetch('fetch', vi.fn(async () => jsonResponse(mediaCatalogResponse())));
    const view = render(<MemoryRouter><PlaygroundPage workbench /></MemoryRouter>);
    await screen.findByText('Saved answer');
    await act(async () => view.rerender(<MemoryRouter><PlaygroundPage initialMode="image" workbench /></MemoryRouter>));
    await screen.findByLabelText('图片提示词');
    window.localStorage.setItem(key, 'externally unreadable record');
    await act(async () => view.rerender(<MemoryRouter><PlaygroundPage initialMode="text" workbench /></MemoryRouter>));
    await screen.findByLabelText('输入消息');
    expect(window.localStorage.getItem(key)).toBe('externally unreadable record');
    expect(screen.getByText('Saved answer')).toBeVisible();
  });

  it('clears an image prompt when the reused workbench instance switches to text', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    const { rerender } = render(
      <MemoryRouter initialEntries={['/console/workbench/image']}>
        <PlaygroundPage initialMode="image" workbench />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText('图片提示词'), { target: { value: '只用于图片生成的提示词' } });

    rerender(
      <MemoryRouter initialEntries={['/console/workbench/text']}>
        <PlaygroundPage initialMode="text" workbench />
      </MemoryRouter>,
    );

    expect(await screen.findByLabelText('输入消息')).toHaveValue('');
  });

  it('renders an image generation workspace with a model panel and recent gallery', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);

    expect(await screen.findByRole('heading', { name: '创建一张图片' })).toBeVisible();
    expect(screen.getByRole('heading', { level: 1, name: '图像工作台' })).toHaveClass('sr-only');
    expect(document.querySelector('.console-page__header')).not.toBeInTheDocument();
    expect(screen.getByLabelText('图片模型')).toHaveValue('zt-image-2');
    expect(screen.getByLabelText('图片提示词')).toBeVisible();
    expect(screen.getByLabelText('图片比例')).toBeVisible();
    expect(screen.getByLabelText('图片分辨率')).toBeVisible();
    expect(screen.getByText('模型 ID')).toBeVisible();
    expect(screen.getByText('最近生成')).toBeVisible();
    expect(screen.getByRole('button', { name: '生成图片' })).toBeVisible();
    expect(screen.getByRole('button', { name: '重置图像参数' })).toBeVisible();
    expect(screen.getByLabelText('上传参考图')).toBeInTheDocument();
    expect(within(screen.getByLabelText('图片数量')).getAllByRole('option')).toHaveLength(10);
    expect(screen.getByText('RUN')).toBeVisible();
    expect(screen.getByText('按实际 API 计费')).toBeVisible();
    expect(screen.getByLabelText('Key 状态')).toHaveTextContent('可用 Key（后端自动选择）');
    expect(screen.getByRole('link', { name: '管理 Keys' })).toHaveAttribute('href', '/console/keys');

    fireEvent.change(screen.getByLabelText('图片提示词'), { target: { value: '临时提示词' } });
    fireEvent.click(screen.getByRole('button', { name: '重置图像参数' }));
    expect(screen.getByLabelText('图片提示词')).toHaveValue('');
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('accepts ordered reference images, supports ten outputs, and removes previews safely', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    });
    stubFetch('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);
    expect(await screen.findByRole('heading', { name: '创建一张图片' })).toBeVisible();
    const referenceInput = screen.getByLabelText('上传参考图');
    const first = new File([new Uint8Array([1, 2])], 'first.png', { type: 'image/png' });
    const second = new File([new Uint8Array([3, 4])], 'second.webp', { type: 'image/webp' });
    fireEvent.change(referenceInput, { target: { files: [first, second] } });

    expect(await screen.findByAltText('参考图 1')).toHaveAttribute('alt', '参考图 1');
    expect(screen.getByAltText('参考图 2')).toHaveAttribute('alt', '参考图 2');
    expect(screen.getByText('first.png')).toBeVisible();
    expect(screen.getByText('second.webp')).toBeVisible();
    expect(screen.getByRole('button', { name: '编辑图片' })).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '删除参考图 1' }));
    expect(screen.getByAltText('参考图 1')).toBeVisible();
    expect(screen.queryByAltText('参考图 2')).not.toBeInTheDocument();
    expect(screen.getByText('second.webp')).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('rejects a sixteenth reference image before any billable request', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    });
    stubFetch('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);
    await screen.findByRole('heading', { name: '创建一张图片' });
    const files = Array.from({ length: 16 }, (_, index) => new File([new Uint8Array([index])], `image-${index + 1}.png`, { type: 'image/png' }));
    fireEvent.change(screen.getByLabelText('上传参考图'), { target: { files } });

    expect(await screen.findByRole('alert')).toHaveTextContent('最多上传 15 张参考图');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('routes a multi-reference submission to the image edit endpoint with ten outputs', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      if (url.endsWith('/pg/images/edits')) {
        const body = init?.body as FormData;
        expect(body.getAll('image')).toHaveLength(2);
        expect(body.get('n')).toBe('10');
        expect(body.get('prompt')).toBe('合成两张参考图');
        expect(body.has('response_format')).toBe(false);
        return jsonResponse({ created: 1, data: [{ url: 'https://cdn.example/edited.png' }] }, 200, { 'X-Request-ID': 'req-edit-1' });
      }
      if (url.includes('/api/log/self?')) return jsonResponse({ success: true, data: { page: 1, page_size: 1, total: 0, items: [] } });
      throw new Error(`Unexpected request: ${url}`);
    });
    stubFetch('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);
    await screen.findByRole('heading', { name: '创建一张图片' });
    fireEvent.change(screen.getByLabelText('上传参考图'), {
      target: {
        files: [
          new File([new Uint8Array([1])], 'first.png', { type: 'image/png' }),
          new File([new Uint8Array([2])], 'second.png', { type: 'image/png' }),
        ],
      },
    });
    fireEvent.change(screen.getByLabelText('图片提示词'), { target: { value: '合成两张参考图' } });
    fireEvent.change(screen.getByLabelText('图片数量'), { target: { value: '10' } });
    fireEvent.click(await screen.findByRole('button', { name: '编辑图片' }));

    expect(await screen.findByRole('img', { name: '生成结果 1' })).toHaveAttribute('src', 'https://cdn.example/edited.png');
    expect(screen.getByText('req-edit-1')).toBeVisible();
    expect(fetchMock.mock.calls.some(([input]) => input.toString().endsWith('/pg/images/generations'))).toBe(false);
  });

  it('renders a video task workspace with capability-driven controls and a result area', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/video']}><PlaygroundPage initialMode="video" workbench /></MemoryRouter>);

    expect(await screen.findByRole('heading', { name: '创建一段视频' })).toBeVisible();
    expect(screen.getByLabelText('视频模型')).toHaveValue('zt-video-2');
    expect(screen.getByText('生成模式')).toBeVisible();
    expect(screen.getByRole('button', { name: '文生视频' })).toBeVisible();
    expect(screen.getByLabelText('视频提示词')).toBeVisible();
    expect(screen.getByLabelText('视频时长')).toBeVisible();
    expect(screen.getByText('生成结果')).toBeVisible();
    expect(screen.getByRole('button', { name: '生成视频' })).toBeVisible();
    expect(screen.queryByText('发送测试请求')).not.toBeInTheDocument();
  });

  it('shows a visible disabled reference-image state when the selected model is generation-only', async () => {
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/user/models')) return jsonResponse(mediaCatalogWithoutEditsResponse());
      throw new Error(`Unexpected request: ${url}`);
    }));

    render(<MemoryRouter initialEntries={['/console/workbench/image']}><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);

    await screen.findByRole('heading', { name: '创建一张图片' });
    expect(screen.getByText('参考图编辑未开放')).toBeVisible();
    expect(screen.getByText('当前模型仅支持文本生成图片，请切换到支持图像编辑的模型。')).toBeVisible();
    expect(screen.queryByLabelText('上传参考图')).not.toBeInTheDocument();
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
    stubFetch('fetch', fetchMock);

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
    stubFetch('fetch', vi.fn(async (input: RequestInfo | URL) => {
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
    stubFetch('fetch', vi.fn().mockResolvedValue(jsonResponse(catalogResponse())));
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
    stubFetch('fetch', fetchMock);

    render(<MemoryRouter initialEntries={['/console/test']}><PlaygroundPage /></MemoryRouter>);
    await screen.findByRole('button', { name: '图片' });
    fireEvent.click(screen.getByRole('button', { name: '图片' }));
    expect(await screen.findByLabelText('图片模型')).toHaveValue('zt-image-2');
    fireEvent.change(screen.getByLabelText('图片提示词'), { target: { value: '一只在雨中的橘猫' } });
    fireEvent.change(screen.getByLabelText('图片比例'), { target: { value: '3:2' } });
    fireEvent.change(screen.getByLabelText('图片分辨率'), { target: { value: '2K' } });
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
    stubFetch('fetch', fetchMock);

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
