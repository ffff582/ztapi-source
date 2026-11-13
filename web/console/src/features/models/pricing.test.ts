import { describe, expect, it } from 'vitest';
import { parsePricingEnvelope, type PricingModel } from '../../api/contracts';
import { managedPublicPricing } from '../home/public-pricing.fixture';
import { modelOfficialDiscount, modelPriceDetails } from './pricing';

function details(overrides: Partial<PricingModel>) {
  const pricing = parsePricingEnvelope(managedPublicPricing);
  return modelPriceDetails({ ...pricing.models[0], ...overrides }, pricing, 250_000);
}

describe('public price comparisons', () => {
  it('shows conservative two-decimal folds for the reduced GPT prices', () => {
    const prices = details({ billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '0.7013' }, official_usd: { input_tokens: '2' } });
    expect(prices[0].officialDiscount).toBe(3.51);
    expect(modelOfficialDiscount(prices)).toBe('3.51');
  });
  it('keeps above-official and equal references without advertising a model-wide discount', () => {
    const prices = details({
      billing_dimensions: ['input_tokens', 'output_tokens', 'cache_read'],
      sale_usd: { input_tokens: '6', output_tokens: '24', cache_read: '1' },
      official_usd: { input_tokens: '10', output_tokens: '20', cache_read: '1' },
    });
    expect(prices[0]).toMatchObject({ officialPrice: '10 U / 1M tokens', officialDiscount: 6 });
    expect(prices[1]).toMatchObject({ officialPrice: '20 U / 1M tokens', officialDifferencePercent: 20 });
    expect(prices[2]).toMatchObject({ officialDifferencePercent: 0 });
    expect(modelOfficialDiscount(prices)).toBeNull();
  });

  it('compares each tier and cache dimension only against its own published reference', () => {
    const prices = details({
      official_usd: { input_tokens: '999', cache_read: '999' },
      token_price_rules: [
        { conditions: ['输入 ≤200K'], sale_usd: { input_tokens: '6', cache_read: '0.5' }, official_usd: { input_tokens: '10' } },
        { conditions: ['输入 >200K'], sale_usd: { input_tokens: '12', cache_read: '2' }, official_usd: { input_tokens: '20', cache_read: '1' } },
      ],
    });
    expect(prices.map(({ label, officialPrice, officialDifferencePercent }) => ({ label, officialPrice, officialDifferencePercent }))).toEqual([
      { label: '输入（输入 ≤200K）', officialPrice: '10 U / 1M tokens', officialDifferencePercent: -40 },
      { label: '缓存读取（输入 ≤200K）', officialPrice: undefined, officialDifferencePercent: undefined },
      { label: '输入（输入 >200K）', officialPrice: '20 U / 1M tokens', officialDifferencePercent: -40 },
      { label: '缓存读取（输入 >200K）', officialPrice: '1 U / 1M tokens', officialDifferencePercent: 100 },
    ]);
    expect(modelOfficialDiscount(prices)).toBeNull();
  });

  it('retains media billing dimensions as well as the rule conditions', () => {
    const prices = details({
      pricing_rules: [{
        id: 'image-tier', conditions: { prompt_tokens_tier: 'gt_200k' }, billing_unit: 'usd_per_million_tokens',
        sale_usd: { input_tokens: '6', output_tokens: '24' }, official_usd: { input_tokens: '10', output_tokens: '20' },
      }],
    });
    expect(prices.map((price) => price.label)).toEqual(['输入（输入超过 200K）', '输出（输入超过 200K）']);
    expect(prices[1]).toMatchObject({ officialDifferencePercent: 20 });
  });

  it('shows published zero references but never divides by them or invents missing references', () => {
    const prices = details({
      billing_dimensions: ['input_tokens', 'output_tokens', 'cache_read'],
      sale_usd: { input_tokens: '0', output_tokens: '1', cache_read: '1' },
      official_usd: { input_tokens: '10', output_tokens: '0' },
    });
    expect(prices[0]).toMatchObject({ officialPrice: '10 U / 1M tokens', officialDifferencePercent: -100 });
    expect(prices[1].officialPrice).toBe('0 U / 1M tokens');
    expect(prices[1].officialDifferencePercent).toBeUndefined();
    expect(prices[2].officialPrice).toBeUndefined();
    expect(modelOfficialDiscount(prices)).toBeNull();
  });

  it('does not round a slight saving into an advertised official 10-fold discount', () => {
    const prices = details({ billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '9.999' }, official_usd: { input_tokens: '10' } });
    expect(modelOfficialDiscount(prices)).toBeNull();
  });
});
