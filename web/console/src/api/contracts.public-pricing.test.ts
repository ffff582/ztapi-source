import { describe, expect, it } from 'vitest';
import { getPublicModelFamily, parsePricingEnvelope } from './contracts';
import { managedPublicPricing } from '../features/home/public-pricing.fixture';

describe('managed anonymous pricing projection', () => {
  it('preserves API family and exact dimensional prices for the actual 35-model schema', () => {
    const result = parsePricingEnvelope(managedPublicPricing);
    expect(result.models).toHaveLength(35);
    expect(result.models[0]).toMatchObject({
      provider_family: 'anthropic', vendor_name: 'Claude', owner_by: '',
      input_price_per_million: '1.3000000000', output_price_per_million: '6.5000000000',
      sale_usd: managedPublicPricing.data[0].sale_usd,
      billing_dimensions: managedPublicPricing.data[0].billing_dimensions,
    });
  });

  it('prefers API family over misleading model names and legacy owner fields', () => {
    expect(getPublicModelFamily('gpt-misleading', 'openai', 'anthropic')).toBe('Claude');
    expect(getPublicModelFamily('zt-gpt-public', '', 'openai')).toBe('OpenAI');
    expect(getPublicModelFamily('zt-gemini-public', '', 'google')).toBe('Gemini');
    expect(getPublicModelFamily('gpt-misleading', 'openai', 'qwen')).toBeNull();
  });

  it.each([
    { vendor_name: '' }, { provider_family: '' }, { input_price_per_million: '999' },
    { sale_usd: { input_tokens: '1.3' } }, { billing_dimensions: ['input_tokens'] },
    { output_price_per_million: '0' }, { supported_endpoint_types: ['unknown'] },
  ])('rejects incomplete managed prices rather than falling back to legacy ratios: %j', (overrides) => {
    expect(() => parsePricingEnvelope({ ...managedPublicPricing, data: [{ ...managedPublicPricing.data[0], ...overrides }] })).toThrow();
  });

  it('accepts the anonymous embedding schema without requiring a nonexistent modality field', () => {
    const row = { ...managedPublicPricing.data[0], model_name: 'zt-text-embedding-3-small',
      provider_family: 'openai', vendor_name: 'OpenAI', supported_endpoint_types: ['embeddings'],
      input_price_per_million: '0.0260000000', output_price_per_million: '0.0000000000',
      billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '0.0260000000' }, billing_rule: 'input_only',
    };
    expect(parsePricingEnvelope({ ...managedPublicPricing, data: [row] }).models[0]).toMatchObject({
      provider_family: 'openai', billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '0.0260000000' },
    });
  });
});
