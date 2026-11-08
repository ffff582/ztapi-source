export const imageSale = {
  version: 1,
  billing_unit: 'usd_per_image',
  base_prices: { '1K': '0.30', '2K': '0.30', '4K': '0.60' },
  included_prompt_tokens: 2000,
  prompt_block_tokens: 2000,
  prompt_block_price: '0.02',
  included_reference_units: 1,
  reference_unit_pixels: 4194304,
  reference_unit_price: '0.05',
} as const;

export function imageCatalogItem(overrides: Record<string, unknown> = {}) {
  return {
    modality: 'image', model_name: 'zt-image-per-picture',
    provider_family: 'openai', provider_name: 'OpenAI', protocol: 'openai_compatible',
    enable_groups: ['default'], supported_endpoint_types: ['images'],
    input_price_per_million: '', output_price_per_million: '',
    billing_dimensions: ['images'], sale_usd: { images: '0.30' },
    billing_rule: 'per_image', billing_unit: 'usd_per_image', pricing_version: 'offline-image-v1',
    image_sale: structuredClone(imageSale),
    supported_options: {
      sizes: ['1024x1024', '2048x2048', '4096x4096'], qualities: ['standard'],
      response_formats: ['url'], min_count: 1, max_count: 10,
      render_options: [
        { aspect_ratio: '1:1', resolution: '1K', size: '1024x1024', quality: 'standard' },
        { aspect_ratio: '1:1', resolution: '2K', size: '2048x2048', quality: 'standard' },
        { aspect_ratio: '1:1', resolution: '4K', size: '4096x4096', quality: 'standard' },
      ],
    },
    pricing_rules: Object.entries(imageSale.base_prices).map(([resolution, price]) => ({
      id: `image-${resolution}`, conditions: { resolution },
      billing_unit: 'usd_per_image', sale_usd: { images: price },
    })),
    ...overrides,
  };
}

export function publicImageItem(overrides: Record<string, unknown> = {}) {
  return {
    ...imageCatalogItem(), vendor_name: 'OpenAI', description: '',
    quota_type: 0, model_ratio: 0, model_price: 0, owner_by: 'openai', completion_ratio: 0,
    billing_mode: 'ratio', billing_expr: '', ...overrides,
  };
}

export const imageDisplayFX = {
  available: true, source: 'ecb_usd_cny_reference', cny_per_unit: '6.5',
  rate_date: '2026-11-06', stale: false,
};

export const publicImagePricing = {
  success: true, data: [publicImageItem()], group_ratio: { default: 1 },
  usable_group: { default: 'Default' }, pricing_version: 'offline-image-v1',
};
