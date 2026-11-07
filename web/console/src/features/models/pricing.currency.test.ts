import { describe, expect, it } from 'vitest';
import { parsePricingEnvelope, parseRuntimeStatus, parseUserModelCatalog } from '../../api/contracts';
import { managedPublicPricing } from '../home/public-pricing.fixture';
import { formatUnitPrice, modelPriceDetails } from './pricing';

const cny = { currency: 'CNY' as const, cnyPerUnit: 7.3 };

describe('reference-currency model prices', () => {
  it('converts the published amount without changing the original U amount', () => {
    expect(formatUnitPrice('2.5045', '1M tokens', cny)).toBe('18.2829 元 / 1M tokens');
    expect(formatUnitPrice('2.5045', '1M tokens')).toBe('2.5045 U / 1M tokens');
    expect(formatUnitPrice('0.00000001', '次', cny)).toBe('0.000000073 元 / 次');
  });

  it.each([undefined, 0, -7.3, NaN, Infinity, 1e-9, 1e9])('falls back to U for unavailable or invalid FX %s', (rate) => {
    expect(formatUnitPrice('2.5045', '1M tokens', { currency: 'CNY', cnyPerUnit: rate })).toBe('2.5045 U / 1M tokens');
  });

  it('does not present a legacy normalized U reference as a known native CNY price', () => {
    const pricing = parsePricingEnvelope(managedPublicPricing);
    const model = {
      ...pricing.models[0], billing_dimensions: ['input_tokens'],
      sale_usd: { input_tokens: '1.6198' }, official_usd: { input_tokens: '1.3574660633484163' },
      official_reference: { url: 'https://example.com/pricing', checked_at: '2026-11-07', kind: 'official_snapshot' as const },
    };
    const [price] = modelPriceDetails(model, pricing, 500_000, cny);
    expect(price.price).toBe('11.8245 元 / 1M tokens');
    expect(price.officialPrice).toBe('1.35747 U / 1M tokens');
    expect(price.officialDifferencePercent).toBeUndefined();
    expect(price.officialDiscount).toBeUndefined();
  });

  it('supports an unambiguous English CNY unit', () => {
    expect(formatUnitPrice('2.5045', '1M tokens', { ...cny, cnyLabel: 'CNY' })).toBe('18.2829 CNY / 1M tokens');
  });

  it('preserves native CNY official prices instead of applying the current FX twice', () => {
    const pricing = parsePricingEnvelope(managedPublicPricing);
    const model = {
      ...pricing.models[0], billing_dimensions: ['input_tokens'],
      sale_usd: { input_tokens: '1.6198' },
      official_usd: { input_tokens: '1.357466063348416289592760180995475113122' },
      official_reference: {
        url: 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/',
        checked_at: '2026-11-07', kind: 'official_snapshot' as const,
        original_currency: 'CNY' as const, original_units_per_usd: '6.63',
      },
    };
    const [price] = modelPriceDetails(model, pricing, 250_000, cny);
    expect(price.price).toBe('11.8245 元 / 1M tokens');
    expect(price.officialPrice).toBe('9 元 / 1M tokens');
    expect(price.officialDifferencePercent).toBeCloseTo((1.6198 * 7.3 / 9 - 1) * 100, 9);
    expect(modelPriceDetails(model, pricing, 250_000)[0].price).toBe('1.6198 U / 1M tokens');
  });

  it('uses one display rate for USD references and never fabricates absent cache references', () => {
    const pricing = parsePricingEnvelope(managedPublicPricing);
    const model = {
      ...pricing.models[0],
      official_reference: { url: 'https://example.com/pricing', checked_at: '2026-11-07', kind: 'official_snapshot' as const, original_currency: 'USD' as const, original_units_per_usd: '1' },
      token_price_rules: [{ conditions: [], sale_usd: { input_tokens: '2.5045', cache_write: '5' }, official_usd: { input_tokens: '5' } }],
    };
    const prices = modelPriceDetails(model, pricing, 250_000, cny);
    expect(prices[0].officialPrice).toBe('36.5 元 / 1M tokens');
    expect(prices[0].officialDiscount).toBe(5.01);
    expect(prices[1].price).toBe('36.5 元 / 1M tokens');
    expect(prices[1].officialPrice).toBeUndefined();
  });

  it('keeps an entirely unclassified legacy official reference in U rather than inventing CNY comparison', () => {
    const pricing = parsePricingEnvelope(managedPublicPricing);
    const model = { ...pricing.models[0], billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '1.6198' }, official_usd: { input_tokens: '1.3574660633484163' } };
    const [price] = modelPriceDetails(model, pricing, 500_000, cny);
    expect(price.officialPrice).toBe('1.35747 U / 1M tokens');
    expect(price.officialDifferencePercent).toBeUndefined();
  });

  it.each([
    { original_currency: 'CNY' },
    { original_currency: 'CNY', original_units_per_usd: '0' },
    { original_currency: 'USD', original_units_per_usd: '2' },
    { original_currency: 'GBP', original_units_per_usd: '7.3' },
  ])('preserves both catalog kinds when optional native metadata is malformed: %j', (invalid) => {
    const raw = {
      ...managedPublicPricing.data[0],
      official_reference: { url: 'https://example.com/pricing', checked_at: '2026-11-07', kind: 'official_snapshot', ...invalid },
    };
    const publicCatalog = parsePricingEnvelope({ ...managedPublicPricing, data: [raw] });
    expect(publicCatalog.models).toHaveLength(1);
    expect(publicCatalog.models[0].official_reference?.original_currency).toBeUndefined();
    const authenticated = parseUserModelCatalog({
      success: true, data: [raw.model_name], catalog: [{ ...raw, provider_name: raw.vendor_name, modality: 'text', protocol: 'openai_compatible', pricing_version: 'v1' }],
    });
    expect(authenticated.models).toEqual([raw.model_name]);
    expect(authenticated.catalog[0].official_reference?.original_units_per_usd).toBeUndefined();
  });

  it('converts media prices without changing their specification or billing unit', () => {
    const pricing = parsePricingEnvelope(managedPublicPricing);
    const model = {
      ...pricing.models[0], pricing_rules: [{ id: 'edit-image', conditions: { token_bucket: 'image_input' }, billing_unit: 'usd_per_million_tokens', sale_usd: { image_input: '7.8057' } }],
    };
    const [price] = modelPriceDetails(model, pricing, 250_000, cny);
    expect(price.price).toBe('56.9816 元 / 1M tokens');
    expect(price.label).toContain('图片输入');
  });

  it('reads display FX separately from quota and ignores invalid optional FX', () => {
    expect(parseRuntimeStatus({ quota_per_unit: 500_000, usd_exchange_rate: 7.3 })).toEqual({ quota_per_unit: 500_000, usd_exchange_rate: 7.3 });
    expect(parseRuntimeStatus({ quota_per_unit: 500_000, usd_exchange_rate: -1 })).toEqual({ quota_per_unit: 500_000 });
  });
});
