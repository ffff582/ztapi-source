import { cleanup, render, screen, within } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession } from '../../api/client';
import { createZTAPIRouter } from '../../app/router';
import { managedPublicPricing } from '../home/public-pricing.fixture';
import { publicPricingWithEmbeddings } from './public-models.fixture';

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
    pricing_version: 'pricing-test-v2',
  });
}

function statusResponse() {
  return jsonResponse({
    success: true,
    data: { quota_per_unit: 250_000 },
  });
}

function renderModels(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal('fetch', fetchMock);
  return render(
    <RouterProvider router={createZTAPIRouter(['/models'])} />,
  );
}

afterEach(() => {
  cleanup();
  clearAuthSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ZTAPI public model pricing', () => {
  it.each([managedPublicPricing, publicPricingWithEmbeddings])('renders every managed public model across all API providers ($data.length rows)', async (fixture) => {
    renderModels(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse(fixture)));
    expect(await screen.findByText(`${fixture.data.length} 个公开模型`)).toBeVisible();
    expect(document.querySelectorAll('.catalog-table tbody tr')).toHaveLength(fixture.data.length);
    for (const item of fixture.data) expect(screen.getByText(item.model_name, { exact: true })).toBeVisible();
    for (const vendor of new Set(fixture.data.map((item) => item.vendor_name))) {
      expect(screen.getByRole('heading', { name: vendor })).toBeVisible();
    }
    expect(screen.queryByText('当前没有可展示的公开模型。')).not.toBeInTheDocument();
    const claude = screen.getByText('zt-claude-haiku-4.5').closest('tr') as HTMLElement;
    expect(within(claude).getByText('$1.3 / 1M tokens')).toBeVisible();
    expect(within(claude).getByText('$6.5 / 1M tokens')).toBeVisible();
    expect(within(claude).getByText('$0.13 / 1M tokens')).toBeVisible();
    expect(within(claude).getByText('缓存读取')).toBeVisible();
    expect(within(claude).getAllByRole('listitem')).toHaveLength(6);
    expect(within(claude).queryByText('按规则计费')).not.toBeInTheDocument();
  });

  it('shows only exact input prices for both embeddings, with no output or legacy ratio price', async () => {
    renderModels(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse(publicPricingWithEmbeddings)));
    await screen.findByText('zt-text-embedding-ada-002');
    for (const [name, price] of [['zt-text-embedding-ada-002', '$0.13 / 1M tokens'], ['zt-text-embedding-3-small', '$0.026 / 1M tokens']]) {
      const row = screen.getByText(name).closest('tr') as HTMLElement;
      expect(within(row).getByText(price)).toBeVisible();
      expect(within(row).getAllByRole('listitem')).toHaveLength(1);
      expect(within(row).getByText('输入')).toBeVisible();
      expect(within(row).queryByText('输出')).not.toBeInTheDocument();
      expect(row).not.toHaveTextContent('$0 /');
      expect(row).not.toHaveTextContent('按规则计费');
    }
  });

  it('groups by the authoritative API provider even when model name and legacy owner disagree', async () => {
    const fixture = { ...managedPublicPricing, data: [{ ...managedPublicPricing.data[0],
      model_name: 'gpt-misleading', owner_by: 'OpenAI', provider_family: 'qwen', vendor_name: 'Qwen',
    }] };
    renderModels(vi.fn(async (input: RequestInfo | URL) => input.toString().endsWith('/api/status')
      ? statusResponse() : jsonResponse(fixture)));
    await screen.findByText('gpt-misleading');
    expect(screen.getByRole('heading', { name: 'Qwen' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'OpenAI' })).not.toBeInTheDocument();
  });

  it('uses runtime and group pricing while labeling tiered rows as dynamic', async () => {
    const rows = [
      {
        model_name: 'gpt-static',
        description: 'Static token pricing',
        quota_type: 0,
        model_ratio: 1.25,
        model_price: 0,
        owner_by: 'OpenAI',
        completion_ratio: 4,
        enable_groups: ['default'],
        billing_mode: 'ratio',
      },
      {
        model_name: 'gpt-tiered',
        description: 'Tiered pricing',
        quota_type: 0,
        model_ratio: 99,
        model_price: 0,
        owner_by: 'OpenAI',
        completion_ratio: 99,
        enable_groups: ['default'],
        billing_mode: 'tiered_expr',
        billing_expr: 'if(tokens>1000, 2, 1)',
      },
      {
        model_name: 'gpt-multimodal',
        description: 'Additional image pricing dimension',
        quota_type: 0,
        model_ratio: 1,
        model_price: 0,
        owner_by: 'OpenAI',
        completion_ratio: 2,
        image_ratio: 3,
        enable_groups: ['default'],
      },
      {
        model_name: 'claude-fixed',
        description: 'Fixed request pricing',
        quota_type: 1,
        model_ratio: 0,
        model_price: 0.2,
        owner_by: 'Anthropic',
        completion_ratio: 0,
        enable_groups: ['default'],
      },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/api/status')) {
        return statusResponse();
      }
      if (url.endsWith('/api/pricing')) {
        return pricingResponse(rows);
      }
      return jsonResponse({ success: false, message: 'unexpected' }, 500);
    });
    renderModels(fetchMock);

    const staticRow = (await screen.findByText('gpt-static')).closest('tr');
    expect(staticRow).not.toBeNull();
    expect(within(staticRow as HTMLElement).getByText('$7.5 / 1M tokens')).toBeVisible();
    expect(within(staticRow as HTMLElement).getByText('$30 / 1M tokens')).toBeVisible();

    const fixedRow = screen.getByText('claude-fixed').closest('tr');
    expect(fixedRow).not.toBeNull();
    expect(
      within(fixedRow as HTMLElement).getAllByText('$0.3 / 次'),
    ).toHaveLength(2);

    const tieredRow = screen.getByText('gpt-tiered').closest('tr');
    expect(tieredRow).not.toBeNull();
    expect(
      within(tieredRow as HTMLElement).getAllByText('按规则计费'),
    ).toHaveLength(2);
    expect(tieredRow).not.toHaveTextContent('$198');

    const multimodalRow = screen.getByText('gpt-multimodal').closest('tr');
    expect(multimodalRow).not.toBeNull();
    expect(
      within(multimodalRow as HTMLElement).getAllByText('按规则计费'),
    ).toHaveLength(2);
  });

  it.each([
    {
      caseName: 'unsupported quota type',
      overrides: { quota_type: 2 },
    },
    {
      caseName: 'string quota type',
      overrides: { quota_type: '0' },
    },
    {
      caseName: 'boolean quota type',
      overrides: { quota_type: false },
    },
    {
      caseName: 'null quota type',
      overrides: { quota_type: null },
    },
    {
      caseName: 'negative base ratio',
      overrides: { model_ratio: -1 },
    },
    {
      caseName: 'negative optional ratio',
      overrides: { cache_ratio: -0.5 },
    },
  ])('rejects $caseName pricing data', async ({ overrides }) => {
    const malformedRow = {
      model_name: 'gpt-malformed',
      description: 'Malformed pricing',
      quota_type: 0,
      model_ratio: 1,
      model_price: 0,
      owner_by: 'OpenAI',
      completion_ratio: 1,
      enable_groups: ['default'],
      ...overrides,
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/api/status')) {
        return statusResponse();
      }
      if (url.endsWith('/api/pricing')) {
        return pricingResponse([malformedRow]);
      }
      return jsonResponse({ success: false, message: 'unexpected' }, 500);
    });
    renderModels(fetchMock);

    expect(
      await screen.findByText('模型价格加载失败，请稍后重试。'),
    ).toBeVisible();
    expect(screen.queryByText('gpt-malformed')).not.toBeInTheDocument();
  });
});
