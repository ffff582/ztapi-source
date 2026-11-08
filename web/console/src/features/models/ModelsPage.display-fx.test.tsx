import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { createZTAPIRouter } from '../../app/router';
import { managedPublicPricing } from '../home/public-pricing.fixture';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function showReference(reference: unknown) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const payload = url.endsWith('/api/display-fx') ? { success: true, data: reference }
      : url.endsWith('/api/status') ? { success: true, data: { quota_per_unit: 500_000, usd_exchange_rate: 7.3 } }
        : { ...managedPublicPricing, data: [managedPublicPricing.data[0]] };
    return new Response(JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' } });
  }));
  return render(<RouterProvider router={createZTAPIRouter(['/models'])} />);
}

it('uses the independent dated reference without changing the original U price', async () => {
  showReference({ available: true, cny_per_unit: '6.67129422', rate_date: '2026-11-06', source: 'ecb_usd_cny_reference', stale: false });
  const article = await screen.findByRole('article', { name: managedPublicPricing.data[0].model_name });
  expect(await within(article).findAllByText('8.67268 元 / 1M tokens')).not.toHaveLength(0);
  expect(screen.getByText(/2026-11-06/)).toBeVisible();
  expect(screen.getByText(/USDT.*成交价/)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'U', exact: true }));
  expect(within(article).getAllByText('1.3 U / 1M tokens')[0]).toBeVisible();
});

it('labels the retained reference as cached instead of calling it realtime', async () => {
  showReference({ available: true, cny_per_unit: '6.67', rate_date: '2026-11-06', source: 'ecb_usd_cny_reference', stale: true });
  expect(await screen.findByText(/最近有效缓存/)).toBeVisible();
});

it('does not accept an invalid remote reference as a price multiplier', async () => {
  showReference({ available: true, cny_per_unit: '-1', rate_date: '2026-11-06', source: 'ecb_usd_cny_reference' });
  const article = await screen.findByRole('article', { name: managedPublicPricing.data[0].model_name });
  expect(within(article).getAllByText('9.49 元 / 1M tokens')[0]).toBeVisible();
  expect(screen.getByText(/配置参考汇率，非实时行情/)).toBeVisible();
});
