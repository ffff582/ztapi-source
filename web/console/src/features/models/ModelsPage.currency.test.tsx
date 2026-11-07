import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { createZTAPIRouter } from '../../app/router';
import { managedPublicPricing } from '../home/public-pricing.fixture';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function showCatalog(rate: unknown = 7.3) {
  const fixture = { ...managedPublicPricing, data: [managedPublicPricing.data[0]] };
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const payload = url.endsWith('/api/status')
      ? { success: true, data: { quota_per_unit: 500_000, usd_exchange_rate: rate } } : fixture;
    return new Response(JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' } });
  }));
  return render(<RouterProvider router={createZTAPIRouter(['/models'])} />);
}

it('defaults to CNY with the public rate and lets the user retain original U prices', async () => {
  showCatalog();
  const article = await screen.findByRole('article', { name: managedPublicPricing.data[0].model_name });
  expect(within(article).getAllByText('9.49 元 / 1M tokens')[0]).toBeVisible();
  expect(screen.getByText(/1U = 7.3 元/)).toBeVisible();
  expect(screen.getByRole('button', { name: '人民币' })).toHaveAttribute('aria-pressed', 'true');
  fireEvent.click(screen.getByRole('button', { name: 'U' }));
  expect(within(article).getAllByText('1.3 U / 1M tokens')[0]).toBeVisible();
  expect(localStorage.getItem('ztapi.priceCurrency')).toBe('U');
});

it('keeps a readable original catalog when the public rate is invalid', async () => {
  showCatalog(-1);
  const article = await screen.findByRole('article', { name: managedPublicPricing.data[0].model_name });
  expect(within(article).getAllByText('1.3 U / 1M tokens')[0]).toBeVisible();
  expect(screen.getByRole('button', { name: '人民币' })).toBeDisabled();
  expect(screen.getByText('人民币参考汇率暂不可用，显示 U 原价。')).toBeVisible();
});
