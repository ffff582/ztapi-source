import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { LocaleProvider } from '../../i18n/locale';
import { ModelsPage } from './ModelsPage';
import { SupportedModelsPage } from './SupportedModelsPage';
import { imageCatalogItem, imageDisplayFX, publicImageItem, publicImagePricing } from './image-sale.fixture';

afterEach(() => { clearAuthSession(); vi.unstubAllGlobals(); });

const staleReference = { kind: 'official_snapshot', url: 'https://example.invalid/pricing', checked_at: '2026-11-06' };

function showPage(account: boolean, rateAvailable = true, english = false) {
  if (english) localStorage.setItem('ztapi.locale', 'en');
  if (account) setAuthSession({ access_token: 'offline-fixture-only', expires_in: 900,
    user: { id: 7, username: 'fixture', role: 1, group: 'default' } });
  const item = imageCatalogItem({ official_reference: staleReference, official_usd: { images: '1' } });
  const publicItem = publicImageItem({ official_reference: staleReference,
    pricing_rules: item.pricing_rules.map((rule) => ({ ...rule, official_usd: { images: '1' } })) });
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const pathname = new URL(String(input), 'http://localhost').pathname;
    if (init?.method && init.method !== 'GET') throw new Error(`Unexpected mutation: ${pathname}`);
    const body = pathname === '/api/pricing' ? { ...publicImagePricing, data: [publicItem] }
      : pathname === '/api/user/models' ? { success: true, data: [item.model_name], catalog: [item],
        ...(rateAvailable ? { display_cny_per_unit: 7.3, display_fx: imageDisplayFX } : {}) }
        : pathname === '/api/status' ? { success: true, data: { quota_per_unit: 500_000,
          ...(rateAvailable ? { usd_exchange_rate: 7.3, display_fx: imageDisplayFX } : {}) } }
          : null;
    if (!body) throw new Error(`Unexpected offline request: ${pathname}`);
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  }));
  render(<LocaleProvider><MemoryRouter>{account ? <SupportedModelsPage /> : <ModelsPage />}</MemoryRouter></LocaleProvider>);
}

async function imageRegion(account: boolean) {
  const name = await screen.findByText('zt-image-per-picture');
  const region = (account ? name.closest('tr') : name.closest('article')) as HTMLElement;
  if (!account) fireEvent.click(region.querySelector('summary') as HTMLElement);
  return region;
}

it.each([false, true])('shows all image tiers and surcharges, switches reference FX without token discounts (account=%s)', async (account) => {
  showPage(account);
  const region = await imageRegion(account);
  const list = within(region).getByRole('list', { name: 'zt-image-per-picture 按张售价' });
  expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual([
    '1K1.95 元 / 张', '2K1.95 元 / 张', '4K3.90 元 / 张',
  ]);
  expect(region).toHaveTextContent('基础包含 2000 个本地估算 Token 和 1 份标准参考图档');
  expect(region).toHaveTextContent('额外参考图档 +0.325 元 / 每张输出图');
  expect(region).toHaveTextContent('提示词每多不足或满 2000 Token，+0.13 元 / 每张输出图');
  expect(region).toHaveTextContent('2048x2048');
  expect(region).toHaveTextContent('4194304');
  expect(region).toHaveTextContent('向上取整');
  expect(region).toHaveTextContent('实际费用在派发前确定');
  expect(region).not.toHaveTextContent(/1M tokens|usd_per_image|官网|官方.*折/);
  expect(within(region).queryByRole('link', { name: /官网/ })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'U' }));
  expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual([
    '1K0.30 U / 张', '2K0.30 U / 张', '4K0.60 U / 张',
  ]);
  expect(region).toHaveTextContent('额外参考图档 +0.05 U / 每张输出图');
  expect(region).toHaveTextContent('提示词每多不足或满 2000 Token，+0.02 U / 每张输出图');
  fireEvent.click(screen.getByRole('button', { name: '人民币' }));
  expect(within(list).getByText('3.90 元 / 张')).toBeVisible();
});

it.each([false, true])('falls back to U when no reference FX is available (account=%s)', async (account) => {
  showPage(account, false);
  const region = await imageRegion(account);
  expect(within(region).getAllByText('0.30 U / 张').length).toBeGreaterThanOrEqual(2);
  expect(screen.getByRole('button', { name: '人民币' })).toBeDisabled();
});

it.each([false, true])('localizes per-image units and terms in English (account=%s)', async (account) => {
  showPage(account, true, true);
  const region = await imageRegion(account);
  expect(region).toHaveTextContent('3.90 CNY / image');
  expect(region).toHaveTextContent('2000 locally estimated tokens');
  expect(region).toHaveTextContent('Each additional reference unit');
  expect(region).toHaveTextContent('rounded up');
  expect(region).toHaveTextContent('before dispatch');
  expect(region).not.toHaveTextContent(/张|图档|派发|官网/);
});
