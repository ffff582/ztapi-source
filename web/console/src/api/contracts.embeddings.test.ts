import { describe, expect, it } from 'vitest';
import { parseUserModelCatalog } from './contracts';

function embedding(overrides: Record<string, unknown> = {}) {
  return {
    model_name: 'zt-text-embedding-ada-002', modality: 'embedding',
    provider_family: 'openai', provider_name: 'OpenAI', protocol: 'openai_compatible',
    supported_endpoint_types: ['embeddings'], enable_groups: ['default'],
    input_price_per_million: '0.1300000000', output_price_per_million: '0.0000000000',
    billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '0.1300000000' },
    billing_rule: 'input_only', pricing_version: 'offline-fixture', ...overrides,
  };
}

function envelope(catalog: ReturnType<typeof embedding>[]) {
  return { success: true, data: catalog.map((item) => item.model_name), catalog };
}

describe('Embedding catalog contract', () => {
  it('accepts all 37 text and two input-only embeddings without dropping catalog entries', () => {
    const text = Array.from({ length: 37 }, (_, index) => embedding({
      model_name: `text-fixture-${index}`, modality: 'text',
      supported_endpoint_types: [index % 2 ? 'openai' : 'openai-response'],
      output_price_per_million: '2', billing_dimensions: ['input_tokens', 'output_tokens'],
      sale_usd: { input_tokens: '0.1300000000', output_tokens: '2' }, billing_rule: 'token',
    }));
    const catalog = [...text, embedding(), embedding({
      model_name: 'zt-text-embedding-3-small', input_price_per_million: '0.0260000000',
      sale_usd: { input_tokens: '0.0260000000' },
    })];
    const result = parseUserModelCatalog(envelope(catalog));
    expect(result.catalog).toHaveLength(39);
    expect(result.models).toEqual(catalog.map((item) => item.model_name));
    expect(result.catalog[37]).toMatchObject({
      supported_endpoint_types: ['embeddings'], billing_rule: 'input_only',
      output_price_per_million: '0.0000000000', billing_dimensions: ['input_tokens'],
      sale_usd: { input_tokens: '0.1300000000' },
    });
  });

  it.each([
    { output_price_per_million: '0.01' }, { output_price_per_million: '-0' },
    { output_price_per_million: '0e0' }, { output_price_per_million: 0 },
    { output_price_per_million: '' }, { output_price_per_million: '00' },
    { input_price_per_million: '0', sale_usd: { input_tokens: '0' } },
    { input_price_per_million: '0.2' },
    { supported_endpoint_types: ['embeddings', 'openai'] },
    { supported_endpoint_types: ['embeddings', 'openai-response'] },
    { supported_endpoint_types: ['embeddings', 'embeddings'] },
    { supported_endpoint_types: ['openai-embedding'] },
    { protocol: 'anthropic' }, { modality: 'text' },
    { billing_dimensions: ['input_tokens', 'output_tokens'] },
    { billing_dimensions: ['input_tokens', 'cache_read'], sale_usd: { input_tokens: '0.13', cache_read: '1' } },
    { sale_usd: { input_tokens: '0.13', output_tokens: '0' } },
    { sale_usd: { input_tokens: '0.13', output_tokens: '1' } },
    { billing_rule: 'token' }, { billing_rule: 'multi_dimension' },
    { supported_endpoint_types: ['openai'], modality: 'text' },
    { supported_endpoint_types: ['openai-response'], modality: 'text' },
  ])('rejects inconsistent input-only pricing or endpoints: %j', (overrides) => {
    expect(() => parseUserModelCatalog(envelope([embedding(overrides)]))).toThrow();
  });

  it.each(['openai', 'openai-response'])('still rejects zero output prices for %s text models', (endpoint) => {
    expect(() => parseUserModelCatalog(envelope([embedding({
      modality: 'text', supported_endpoint_types: [endpoint], billing_rule: 'token',
      billing_dimensions: ['input_tokens', 'output_tokens'],
      sale_usd: { input_tokens: '0.13', output_tokens: '0' },
    })]))).toThrow();
  });
});
