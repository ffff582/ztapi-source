import { describe, expect, it } from 'vitest';
import { parseUserModelCatalog } from './contracts';

function mediaItem(modality: 'image' | 'video', overrides: Record<string, unknown> = {}) {
  const image = modality === 'image';
  return {
    modality,
    model_name: image ? 'zt-gp-image-2' : 'zt-seedance-2',
    provider_family: image ? 'openai' : 'seedance',
    provider_name: image ? 'OpenAI' : 'Seedance',
    protocol: 'openai_compatible',
    enable_groups: ['default'],
    supported_endpoint_types: [image ? 'images' : 'video-tasks'],
    input_price_per_million: '',
    output_price_per_million: '',
    billing_dimensions: [],
    sale_usd: {},
    billing_rule: 'multi_dimension',
    pricing_version: image ? 'ztapi-snapshot-101' : 'ztapi-snapshot-102',
    supported_options: image
      ? {
          sizes: ['1024x1024', '512x512'],
          qualities: ['standard'],
          response_formats: ['url'],
          min_count: 1,
          max_count: 2,
        }
      : {
          resolutions: ['720p'],
          duration_seconds: [5],
          supports_video_input: false,
        },
    pricing_rules: image
      ? [
          {
            id: 'image_output',
            conditions: { token_bucket: 'image_output' },
            billing_unit: 'usd_per_million_tokens',
            sale_usd: { image_output: '39.00' },
          },
        ]
      : [
          {
            id: '720p_video_false',
            conditions: { resolution: '720p', contains_video_input: 'false' },
            billing_unit: 'usd_per_million_tokens',
            sale_usd: { input_tokens: '10.4192129630' },
          },
        ],
    billing_unit: 'usd_per_million_tokens',
    ...overrides,
  };
}

function envelope(item: ReturnType<typeof mediaItem>) {
  return { success: true, data: [item.model_name], catalog: [item] };
}

describe('Media catalog contract', () => {
  it.each(['image', 'video'] as const)('accepts a complete %s catalog item', (modality) => {
    const parsed = parseUserModelCatalog(envelope(mediaItem(modality))).catalog[0];
    expect(parsed.modality).toBe(modality);
    expect(parsed.pricing_rules).toHaveLength(1);
    expect(parsed.billing_unit).toBe('usd_per_million_tokens');
    expect(parsed.supported_options).toBeDefined();
  });

  it.each([
    ['video with chat endpoint', 'video', { supported_endpoint_types: ['openai'] }],
    ['image with video endpoint', 'image', { supported_endpoint_types: ['video-tasks'] }],
    ['conditional price without billing unit', 'video', { billing_unit: '' }],
    ['rule without conditions', 'video', { pricing_rules: [{ id: 'bad', conditions: {}, billing_unit: 'usd_per_million_tokens', sale_usd: { input_tokens: '1' } }] }],
    ['rule billing unit mismatch', 'image', { pricing_rules: [{ id: 'bad', conditions: { token_bucket: 'image_output' }, billing_unit: 'usd_per_image', sale_usd: { image_output: '1' } }] }],
    ['image without sizes', 'image', { supported_options: { qualities: ['standard'], response_formats: ['url'], min_count: 1, max_count: 2 } }],
    ['video without duration', 'video', { supported_options: { resolutions: ['720p'], supports_video_input: false } }],
    ['media with token prices', 'image', { input_price_per_million: '1', sale_usd: { input_tokens: '1' } }],
  ])('rejects %s', (_name, modality, overrides) => {
    expect(() => parseUserModelCatalog(envelope(mediaItem(modality as 'image' | 'video', overrides)))).toThrow();
  });
});
