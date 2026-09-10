import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { PublicModelProof } from './PublicModelProof';
import { managedPublicPricing } from './public-pricing.fixture';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function pricingResponse(data: unknown[]) {
  return jsonResponse({
    success: true,
    data,
    group_ratio: { default: 1.5 },
    usable_group: { default: '默认分组' },
    pricing_version: 'homepage-proof-v1',
  });
}

function statusResponse() {
  return jsonResponse({
    success: true,
    data: { quota_per_unit: 250_000 },
  });
}

const models = [
  {
    model_name: 'gpt-home',
    description: 'OpenAI 通用模型',
    quota_type: 0,
    model_ratio: 1.25,
    model_price: 0,
    owner_by: 'OpenAI',
    completion_ratio: 4,
    enable_groups: ['default'],
    billing_mode: 'ratio',
    billing_expr: '',
  },
  {
    model_name: 'claude-home',
    description: 'Claude 复杂任务模型',
    quota_type: 1,
    model_ratio: 0,
    model_price: 0.2,
    owner_by: 'Anthropic',
    completion_ratio: 0,
    enable_groups: ['default'],
    billing_mode: 'fixed',
    billing_expr: '',
  },
  {
    model_name: 'gemini-home',
    description: 'Gemini 多模态模型',
    quota_type: 0,
    model_ratio: 0.5,
    model_price: 0,
    owner_by: 'Google',
    completion_ratio: 2,
    enable_groups: ['default'],
    billing_mode: 'ratio',
    billing_expr: '',
  },
];

function renderProof(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal('fetch', fetchMock);
  return render(
    <MemoryRouter>
      <PublicModelProof />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('public homepage model proof', () => {
  it('counts all 35 managed models across seven providers and shows authoritative featured prices', async () => {
    renderProof(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse(managedPublicPricing)));
    expect(await screen.findByText('35 个实时公开模型')).toBeVisible();
    expect(screen.queryByText(/模型目录正在配置/)).not.toBeInTheDocument();
    expect(document.querySelectorAll('.model-proof__grid article')).toHaveLength(3);
    for (const [name, vendor, input, output] of [
      ['zt-claude-haiku-4.5', 'Claude', '$1.3 / 1M tokens', '$6.5 / 1M tokens'],
      ['zt-gpt-4.1', 'OpenAI', '$2.6 / 1M tokens', '$10.4 / 1M tokens'],
    ]) {
      const row = screen.getByText(name).closest('article') as HTMLElement;
      expect(within(row).getByText(vendor)).toBeVisible();
      expect(within(row).getByText(input)).toBeVisible();
      expect(within(row).getByText(output)).toBeVisible();
      expect(within(row).queryByText('按规则计费')).not.toBeInTheDocument();
    }
  });

  it('counts and displays an input-only embedding without inventing output pricing', async () => {
    const row = { ...managedPublicPricing.data[0], model_name: 'zt-text-embedding-3-small',
      provider_family: 'openai', vendor_name: 'OpenAI', supported_endpoint_types: ['embeddings'],
      input_price_per_million: '0.0260000000', output_price_per_million: '0.0000000000',
      billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '0.0260000000' }, billing_rule: 'input_only',
    };
    renderProof(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse({ ...managedPublicPricing, data: [row] })));
    expect(await screen.findByText('1 个实时公开模型')).toBeVisible();
    const card = screen.getByText(row.model_name).closest('article') as HTMLElement;
    expect(within(card).getByText('$0.026 / 1M tokens')).toBeVisible();
    expect(within(card).queryByText('输出')).not.toBeInTheDocument();
  });

  it('counts media products and advertises the three supported capability types', async () => {
    const media = {
      ...managedPublicPricing.data[0], model_name: 'zt-gp-image-2',
      provider_family: 'openai', vendor_name: 'OpenAI', modality: 'image',
      supported_endpoint_types: ['images'], input_price_per_million: '', output_price_per_million: '',
      billing_dimensions: [], sale_usd: {}, billing_rule: 'multi_dimension',
      supported_options: { sizes: ['1024x1024'], qualities: ['standard'], response_formats: ['url'], min_count: 1, max_count: 1 },
      pricing_rules: [{ id: 'image_output', conditions: { token_bucket: 'image_output' }, billing_unit: 'usd_per_million_tokens', sale_usd: { image_output: '39' } }],
      billing_unit: 'usd_per_million_tokens',
    };
    renderProof(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse({ ...managedPublicPricing, data: [media] })));
    expect(await screen.findByText('1 个实时公开模型')).toBeVisible();
    expect(screen.getByText('文本模型')).toBeVisible();
    expect(screen.getByText('图片生成')).toBeVisible();
    expect(screen.getByText('视频生成')).toBeVisible();
    const card = screen.getByText('zt-gp-image-2').closest('article') as HTMLElement;
    expect(within(card).getByText('按规格计费')).toBeVisible();
  });

  it('shows only live public models and calculated prices', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/status')) return statusResponse();
      if (url.endsWith('/api/pricing')) return pricingResponse(models);
      return jsonResponse({ success: false }, 500);
    });

    renderProof(fetchMock);

    expect(await screen.findByText('3 个实时公开模型')).toBeVisible();
    expect(screen.getByText('gpt-home')).toBeVisible();
    expect(screen.getByText('claude-home')).toBeVisible();
    expect(screen.getByText('gemini-home')).toBeVisible();

    const openAiCard = screen.getByText('gpt-home').closest('article');
    expect(openAiCard).not.toBeNull();
    expect(within(openAiCard as HTMLElement).getByText('$7.5 / 1M tokens')).toBeVisible();
    expect(screen.getByRole('link', { name: '查看全部模型与价格' })).toHaveAttribute(
      'href',
      '/models',
    );
  });

  it('shows an actionable error instead of invented fallback data', async () => {
    renderProof(vi.fn(async () => jsonResponse({ success: false }, 503)));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '暂时无法读取实时模型',
    );
    expect(screen.queryByText(/99\.9|500\+|<250ms/)).not.toBeInTheDocument();
  });
});
