import { expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DataContractError, parsePricingModels, parseUserModelCatalog } from '../../api/contracts';
import { LocaleProvider } from '../../i18n/locale';
import { ImageSalePricing } from './ImageSalePricing';
import { partialImageSale, partialImageItem, publicImageItem } from './image-sale.fixture';

it('parses partial v2 without requiring a 4K price', () => {
  const item = partialImageItem();
  const parsed = parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [item] }).catalog[0];
  expect(parsed.image_sale).toEqual(partialImageSale);
  expect(parsed.pricing_rules).toHaveLength(3);
  expect(parsePricingModels([publicImageItem(item)])[0].image_sale).toEqual(partialImageSale);
});

it.each(['duplicate', 'missing', 'unapproved operation'])('rejects inconsistent operation-specific prices: %s', (mode) => {
  const item = partialImageItem();
  if (mode === 'duplicate') item.pricing_rules[2] = item.pricing_rules[0];
  if (mode === 'missing') item.pricing_rules.pop();
  if (mode === 'unapproved operation') item.pricing_rules[1].conditions.image_operation = 'edit';
  expect(() => parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [item] })).toThrow(DataContractError);
});

it('filters broad public supported options to approved v2 capabilities', () => {
  const item = partialImageItem();
  const parsed = parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [item] }).catalog[0];
  expect(parsed.supported_options).toMatchObject({ sizes: ['1024x1024', '1152x2048'], qualities: ['low'], max_count: 1, edit_input: { max_files: 1 } });
  expect(parsed.supported_options?.render_options).toHaveLength(2);
  expect(parsePricingModels([publicImageItem(item)])[0].supported_options).toEqual(parsed.supported_options);
});

it.each([
  { max_images: 2 }, { max_reference_images: 2 }, { max_prompt_tokens: 2001 },
  { base_prices: { ...partialImageSale.base_prices, '4K': '0.60' } },
  { base_prices: { '1K': 0.3, '2K': '0.30' } },
  { base_prices: { '1K': '0.30', '2K': '0.60' } },
  { allowed_requests: [] },
  { allowed_requests: [{ image_operation: 'edit', size: '1152x2048', resolution: '2K', quality: 'low' }] },
  { allowed_requests: [{ image_operation: 'generation', size: '1024x1024', resolution: '1K', quality: 'high' }] },
  { allowed_requests: [partialImageSale.allowed_requests[0], partialImageSale.allowed_requests[0]] },
])('rejects unsafe partial sale metadata on both endpoints: %j', (patch) => {
  const item = partialImageItem();
  const broken = { ...item, image_sale: { ...partialImageSale, ...patch } };
  expect(() => parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [broken] })).toThrow(DataContractError);
  expect(() => parsePricingModels([publicImageItem(broken)])).toThrow(DataContractError);
});

it('renders only approved prices and closed extras for v2', () => {
  render(<LocaleProvider><ImageSalePricing name="partial" sale={partialImageSale as never} display={{ currency: 'U' }} /></LocaleProvider>);
  expect(screen.getAllByRole('listitem')).toHaveLength(2);
  expect(screen.queryByText('4K')).toBeNull();
  expect(screen.getByText(/额外档位尚未开放/)).toBeVisible();
  expect(document.body.textContent).not.toContain('+0.02');
});
