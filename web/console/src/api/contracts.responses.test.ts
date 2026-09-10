import { describe, expect, it } from 'vitest';
import { parseUserModelCatalog } from './contracts';

function responseWithEndpoints(endpoints: string[], protocol = 'openai_compatible') {
  return {
    success: true,
    data: ['zt-gpt-5.4-pro'],
    catalog: [{
      model_name: 'zt-gpt-5.4-pro', provider_family: 'openai', provider_name: 'OpenAI',
      protocol, enable_groups: ['default'], supported_endpoint_types: endpoints,
      input_price_per_million: '1', output_price_per_million: '2',
      billing_dimensions: ['input_tokens', 'output_tokens'],
      sale_usd: { input_tokens: '1', output_tokens: '2' },
      billing_rule: 'token', pricing_version: 'test-v1',
    }],
  };
}

describe('Responses catalog endpoint contract', () => {
  it.each([['openai-response'], ['openai'], ['openai', 'openai-response']])(
    'accepts published OpenAI endpoint types %j without guessing from the model name',
    (...endpoints) => {
      expect(parseUserModelCatalog(responseWithEndpoints(endpoints)).catalog[0].supported_endpoint_types).toEqual(endpoints);
    },
  );

  it.each([
    { endpoints: [], protocol: 'openai_compatible' },
    { endpoints: ['openai-response', 'openai-response'], protocol: 'openai_compatible' },
    { endpoints: ['unknown'], protocol: 'openai_compatible' },
    { endpoints: ['openai', 'anthropic'], protocol: 'openai_compatible' },
    { endpoints: ['openai-response'], protocol: 'anthropic' },
    { endpoints: ['gemini', 'openai-response'], protocol: 'gemini' },
  ])('rejects incompatible or malformed endpoint types: $protocol $endpoints', ({ endpoints, protocol }) => {
    expect(() => parseUserModelCatalog(responseWithEndpoints(endpoints, protocol))).toThrow();
  });
});
