import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { SupportedModelsPage } from './SupportedModelsPage';

afterEach(() => { cleanup(); clearAuthSession(); vi.unstubAllGlobals(); });

it('uses authenticated catalog FX without a second request or any financial mutation', async () => {
  setAuthSession({ access_token: 'currency-fixture-token', expires_in: 900, user: { id: 7, username: 'fixture', role: 1, group: 'default' } });
  const item = {
    model_name: 'zt-currency-fixture', provider_family: 'openai', provider_name: 'OpenAI', protocol: 'openai_compatible',
    modality: 'text', enable_groups: ['default'], supported_endpoint_types: ['openai'],
    input_price_per_million: '1', output_price_per_million: '2', billing_dimensions: ['input_tokens', 'output_tokens'],
    sale_usd: { input_tokens: '1', output_tokens: '2' }, billing_rule: 'token', pricing_version: 'v1',
  };
  const fetchMock = vi.fn(async () => new Response(JSON.stringify({ success: true, data: [item.model_name], catalog: [item], display_cny_per_unit: 7.3 }), { headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetchMock);
  render(<SupportedModelsPage />);
  const row = (await screen.findByText(item.model_name)).closest('tr') as HTMLElement;
  expect(within(row).getByText('7.3 元 / 1M tokens')).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'U' }));
  expect(within(row).getByText('1 U / 1M tokens')).toBeVisible();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
