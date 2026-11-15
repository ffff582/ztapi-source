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

export const partialImageSale = {
  ...imageSale, version: 2, base_prices: { '1K': '0.30', '2K': '0.30' },
  max_prompt_tokens: 2000, max_reference_images: 1, max_images: 1,
  allowed_requests: [
    { image_operation: 'generation', size: '1024x1024', quality: 'low', resolution: '1K' },
    { image_operation: 'generation', size: '1152x2048', quality: 'low', resolution: '2K' },
    { image_operation: 'edit', size: '1024x1024', quality: 'low', resolution: '1K' },
  ],
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

export function partialImageItem() {
  const pricingRules = partialImageSale.allowed_requests.map((allowed) => ({
      id: `image_${allowed.image_operation}_${allowed.resolution}`,
      conditions: { ...allowed }, billing_unit: 'usd_per_image',
      sale_usd: { images: partialImageSale.base_prices[allowed.resolution] },
    }));
  return { ...imageCatalogItem({ image_sale: partialImageSale,
    supported_options: {
      sizes: ['1024x1024', '1152x2048', '4096x4096'], qualities: ['low', 'high'],
      response_formats: ['url'], min_count: 1, max_count: 10,
      supports_edits: true,
      edit_input: { max_files: 15, max_bytes: 10485760, max_total_bytes: 52428800, mime_types: ['image/png', 'image/jpeg', 'image/webp'] },
      render_options: [
        { aspect_ratio: '1:1', resolution: '1K', size: '1024x1024', quality: 'low' },
        { aspect_ratio: '9:16', resolution: '2K', size: '1152x2048', quality: 'low' },
        { aspect_ratio: '1:1', resolution: '4K', size: '4096x4096', quality: 'high' },
      ],
    },
  }), pricing_rules: pricingRules };
}

export const imageDisplayFX = {
  available: true, source: 'ecb_usd_cny_reference', cny_per_unit: '6.5',
  rate_date: '2026-11-06', stale: false,
};

export const publicImagePricing = {
  success: true, data: [publicImageItem()], group_ratio: { default: 1 },
  usable_group: { default: 'Default' }, pricing_version: 'offline-image-v1',
};
