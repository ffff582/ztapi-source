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
    expect(screen.getByText('$0.000024')).toBeVisible();
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
});
