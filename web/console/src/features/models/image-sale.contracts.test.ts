import { describe, expect, it } from 'vitest';
import { DataContractError, parsePricingModels, parseUserModelCatalog } from '../../api/contracts';
import { imageCatalogItem, imageSale, partialImageItem, publicImageItem } from './image-sale.fixture';

function parsePrivate(overrides: Record<string, unknown> = {}) {
  const item = imageCatalogItem(overrides);
  return parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [item] }).catalog[0];
}

describe('per-image catalog contracts', () => {
  it.each([imageCatalogItem, partialImageItem])('accepts native per-image catalog rows without legacy token price placeholders', (fixture) => {
    const item: Record<string, unknown> = fixture();
    delete item.input_price_per_million;
    delete item.output_price_per_million;
    const row = parseUserModelCatalog({ success: true, data: [item.model_name], catalog: [item] }).catalog[0];
    expect(row.input_price_per_million).toBe('');
    expect(row.output_price_per_million).toBe('');
    expect(row.billing_rule).toBe('per_image');
  });

  it('preserves the complete image sale in the authenticated catalog', () => {
    expect(parsePrivate()).toMatchObject({
      image_sale: imageSale, billing_rule: 'per_image', billing_dimensions: ['images'],
      sale_usd: { images: '0.30' }, pricing_rules: imageCatalogItem().pricing_rules,
    });
  });

  it('preserves the complete image sale in anonymous pricing without inventing official prices', () => {
    const item = parsePricingModels([publicImageItem()])[0];
    expect(item).toMatchObject({ image_sale: imageSale, billing_unit: 'usd_per_image', billing_rule: 'per_image' });
    expect(item.official_usd).toBeUndefined();
    expect(item.official_reference).toBeUndefined();
  });

  it.each([
    ['null', null],
    ['unsupported version', { ...imageSale, version: 2 }],
    ['wrong unit', { ...imageSale, billing_unit: 'usd_per_million_tokens' }],
    ['missing tier', { ...imageSale, base_prices: { '1K': '0.30', '2K': '0.30' } }],
    ['unknown tier', { ...imageSale, base_prices: { ...imageSale.base_prices, '8K': '1' } }],
    ['numeric price', { ...imageSale, base_prices: { ...imageSale.base_prices, '1K': 0.3 } }],
    ['negative price', { ...imageSale, prompt_block_price: '-0.02' }],
    ['zero block', { ...imageSale, prompt_block_tokens: 0 }],
    ['fractional allowance', { ...imageSale, included_prompt_tokens: 1.5 }],
    ['negative references', { ...imageSale, included_reference_units: -1 }],
    ['invalid area', { ...imageSale, reference_unit_pixels: 0 }],
    ['missing fee', { ...imageSale, reference_unit_price: undefined }],
    ['extra field', { ...imageSale, source_model: 'private' }],
  ])('rejects malformed image_sale: %s on both endpoints', (_name, sale) => {
    expect(() => parsePrivate({ image_sale: sale })).toThrow(DataContractError);
    expect(() => parsePricingModels([publicImageItem({ image_sale: sale })])).toThrow(DataContractError);
  });

  it.each([
    ['no sale metadata', { image_sale: undefined }],
    ['token billing', { billing_rule: 'multi_dimension' }],
    ['wrong dimension', { billing_dimensions: ['input_tokens'] }],
    ['wrong base price', { sale_usd: { images: '0.60' } }],
    ['null legacy input', { input_price_per_million: null }],
    ['null legacy output', { output_price_per_million: null }],
    ['token input on image billing', { input_price_per_million: '1' }],
    ['token output on image billing', { output_price_per_million: '1' }],
    ['missing resolution', { pricing_rules: imageCatalogItem().pricing_rules.slice(1) }],
    ['duplicate resolution', { pricing_rules: Array(3).fill(imageCatalogItem().pricing_rules[0]) }],
    ['wrong tier price', { pricing_rules: imageCatalogItem().pricing_rules.map((rule) => ({ ...rule, sale_usd: { images: '1' } })) }],
    ['token tier dimension', { pricing_rules: imageCatalogItem().pricing_rules.map((rule) => ({ ...rule, sale_usd: { input_tokens: '0.30' } })) }],
    ['video modality', { modality: 'video', supported_endpoint_types: ['video-tasks'] }],
  ])('rejects inconsistent per-image metadata: %s', (_name, overrides) => {
    expect(() => parsePrivate(overrides)).toThrow(DataContractError);
  });
});
