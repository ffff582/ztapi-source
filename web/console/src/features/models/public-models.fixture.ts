import { managedPublicPricing } from '../home/public-pricing.fixture';

export const publicPricingWithEmbeddings = {
  ...managedPublicPricing,
  data: [
    ...managedPublicPricing.data,
    ...[
      ['zt-text-embedding-ada-002', '0.1300000000'],
      ['zt-text-embedding-3-small', '0.0260000000'],
    ].map(([name, price]) => ({
      ...managedPublicPricing.data[0],
      model_name: name,
      provider_family: 'openai',
      vendor_name: 'OpenAI',
      supported_endpoint_types: ['embeddings'],
      input_price_per_million: price,
      output_price_per_million: '0.0000000000',
      billing_dimensions: ['input_tokens'],
      sale_usd: { input_tokens: price },
      billing_rule: 'input_only',
    })),
  ],
};
