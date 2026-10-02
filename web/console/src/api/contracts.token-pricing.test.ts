import { describe, expect, it } from 'vitest';
import { parsePricingEnvelope, parseUserModelCatalog } from './contracts';
import { managedPublicPricing } from '../features/home/public-pricing.fixture';

const tieredRules = [
  {
    conditions: ['输入 ≤272K'],
    sale_usd: { input_tokens: '3.9000000000', output_tokens: '19.5000000000', cache_read: '0.3900000000' },
  },
  {
    conditions: ['输入 >272K'],
    sale_usd: { input_tokens: '7.8000000000', output_tokens: '29.2500000000', cache_read: '0.7800000000' },
  },
];

function tieredCatalog(overrides: Record<string, unknown> = {}) {
  const item = {
    modality: 'text', model_name: 'zt-gpt-5.6-sol', provider_family: 'openai', provider_name: 'OpenAI',
    protocol: 'openai_compatible', enable_groups: ['default'], supported_endpoint_types: ['openai'],
    input_price_per_million: '', output_price_per_million: '',
    billing_dimensions: ['input_tokens', 'output_tokens', 'cache_read'],
    sale_usd: {},
    billing_rule: 'multi_dimension', pricing_version: 'price-v2', token_price_rules: tieredRules,
    ...overrides,
  };
  return { success: true, data: [item.model_name], catalog: [item] };
}

describe('text token tier contracts', () => {
  it('preserves the complete tier prices and conditions in the account catalog', () => {
    expect(parseUserModelCatalog(tieredCatalog()).catalog[0].token_price_rules).toEqual(tieredRules);
  });

  it('projects the same tier contract in anonymous managed pricing', () => {
    const row = {
      ...managedPublicPricing.data[0],
      billing_dimensions: ['input_tokens', 'output_tokens'],
      billing_rule: 'token',
      input_price_per_million: '', output_price_per_million: '', sale_usd: {},
      token_price_rules: tieredRules.map((rule) => ({
        ...rule, sale_usd: { input_tokens: rule.sale_usd.input_tokens, output_tokens: rule.sale_usd.output_tokens },
      })),
    };
    expect(parsePricingEnvelope({ ...managedPublicPricing, data: [row] }).models[0].token_price_rules)
      .toEqual(row.token_price_rules);
  });

  it('accepts one unconditional tier as the default price', () => {
    const rules = [{ conditions: [], sale_usd: tieredRules[0].sale_usd }];
    expect(parseUserModelCatalog(tieredCatalog({ token_price_rules: rules })).catalog[0].token_price_rules)
      .toEqual(rules);
  });

  it.each([
    { name: 'missing sale dimension', rules: [{ conditions: ['输入 ≤272K'], sale_usd: { input_tokens: '3.9', output_tokens: '19.5' } }] },
    { name: 'extra sale dimension', rules: [{ conditions: ['输入 ≤272K'], sale_usd: { ...tieredRules[0].sale_usd, unknown: '1' } }] },
    { name: 'invalid amount', rules: [{ conditions: ['输入 ≤272K'], sale_usd: { ...tieredRules[0].sale_usd, output_tokens: '0' } }] },
    { name: 'blank condition', rules: [{ conditions: ['  '], sale_usd: tieredRules[0].sale_usd }] },
    { name: 'duplicate condition', rules: [tieredRules[0], tieredRules[0]] },
    { name: 'duplicate default tier', rules: [
      { conditions: [], sale_usd: tieredRules[0].sale_usd },
      { conditions: [], sale_usd: tieredRules[1].sale_usd },
    ] },
    { name: 'missing conditions', rules: [{ sale_usd: tieredRules[0].sale_usd }] },
    { name: 'unexpected rule field', rules: [{ ...tieredRules[0], source_model: 'secret-upstream-id' }] },
  ])('rejects $name rather than publishing incomplete tier pricing', ({ rules }) => {
    expect(() => parseUserModelCatalog(tieredCatalog({ token_price_rules: rules }))).toThrow();
  });

  it('rejects token tiers on media models, leaving media pricing_rules separate', () => {
    const media = tieredCatalog({
      modality: 'image', supported_endpoint_types: ['images'], input_price_per_million: '',
      output_price_per_million: '', billing_dimensions: [], sale_usd: {},
      billing_rule: 'multi_dimension', supported_options: {
        sizes: ['1024x1024'], qualities: ['standard'], response_formats: ['url'], min_count: 1, max_count: 1,
      },
      pricing_rules: [{ id: 'image', conditions: { token_bucket: 'image_output' }, billing_unit: 'usd_per_million_tokens', sale_usd: { image_output: '39' } }],
      billing_unit: 'usd_per_million_tokens',
    });
    expect(() => parseUserModelCatalog(media)).toThrow();
  });
});
