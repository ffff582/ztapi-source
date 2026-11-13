import { describe, expect, it } from 'vitest';
import {
  modelGeneration,
  sortCatalogByRecency,
  sortVendorModelsByRecency,
} from './catalog-order';

function model(model_name: string, provider_family: string, modality = 'text') {
  return { model_name, provider_family, modality };
}

function names(models: { model_name: string }[]) {
  return models.map((item) => item.model_name);
}

describe('model generation', () => {
  it('reads the generation a public name carries', () => {
    expect(modelGeneration('zt-gpt-6-astra')).toBe(6);
    expect(modelGeneration('zt-gpt-5.6-sol')).toBe(5.6);
    expect(modelGeneration('zt-kimi-k3')).toBe(3);
    expect(modelGeneration('zt-kimi-k2.7-code')).toBe(2.7);
    expect(modelGeneration('zt-gpt-4o-mini-2024-07-18')).toBe(4);
    expect(modelGeneration('zt-seedance-2.5')).toBe(2.5);
  });

  it('puts a name with no generation behind the ones that have it', () => {
    const ordered = sortCatalogByRecency([
      model('zt-house-special', 'openai'),
      model('zt-gpt-4.1', 'openai'),
    ]);
    expect(names(ordered)).toEqual(['zt-gpt-4.1', 'zt-house-special']);
  });
});

describe('mixed catalog ordering', () => {
  it('leads with the newest model each vendor sells', () => {
    const ordered = sortCatalogByRecency([
      model('zt-gpt-4.1', 'openai'),
      model('zt-kimi-k2.7-code', 'moonshot'),
      model('zt-gpt-6-astra', 'openai'),
      model('zt-kimi-k3', 'moonshot'),
      model('zt-glm-5.2', 'glm'),
      model('zt-glm-5.3', 'glm'),
    ]);
    // A larger number from a vendor's older line never outranks another
    // vendor's current model: Kimi K3 leads GPT 4.1.
    expect(names(ordered)).toEqual([
      'zt-gpt-6-astra',
      'zt-kimi-k3',
      'zt-glm-5.3',
      'zt-gpt-4.1',
      'zt-kimi-k2.7-code',
      'zt-glm-5.2',
    ]);
  });

  it('ranks each modality on its own line so the newest of every kind leads', () => {
    const ordered = sortCatalogByRecency([
      model('zt-gpt-5.6-sol', 'openai'),
      model('zt-seedance-2.0', 'seedance', 'video'),
      model('zt-gpt-6-astra', 'openai'),
      model('zt-seedance-2.5', 'seedance', 'video'),
    ]);
    expect(names(ordered)).toEqual([
      'zt-gpt-6-astra',
      'zt-seedance-2.5',
      'zt-gpt-5.6-sol',
      'zt-seedance-2.0',
    ]);
  });
});

describe('one vendor table', () => {
  it('keeps chat models together instead of splitting them with the newest image model', () => {
    const ordered = sortVendorModelsByRecency([
      model('zt-gpt-5.6-sol', 'openai'),
      model('zt-gp-image-2', 'openai', 'image'),
      model('zt-gpt-6-astra', 'openai'),
      model('zt-text-embedding-3-small', 'openai', 'embedding'),
    ]);
    // The newest embedding and image models are each current, but they belong
    // after the chat models rather than between two generations of them.
    expect(names(ordered)).toEqual([
      'zt-gpt-6-astra',
      'zt-gpt-5.6-sol',
      'zt-text-embedding-3-small',
      'zt-gp-image-2',
    ]);
  });

  it('still leads each kind with its current generation', () => {
    const ordered = sortVendorModelsByRecency([
      model('zt-text-embedding-ada-002', 'openai', 'embedding'),
      model('zt-gpt-4.1', 'openai'),
      model('zt-text-embedding-3-small', 'openai', 'embedding'),
      model('zt-gpt-6-astra', 'openai'),
    ]);
    expect(names(ordered)).toEqual([
      'zt-gpt-6-astra',
      'zt-gpt-4.1',
      'zt-text-embedding-3-small',
      'zt-text-embedding-ada-002',
    ]);
  });

  it('orders a list that carries no modality by generation alone', () => {
    const ordered = sortVendorModelsByRecency([
      { model_name: 'zt-glm-5.2', provider_family: 'glm' },
      { model_name: 'zt-glm-5.3', provider_family: 'glm' },
      { model_name: 'zt-glm-4.7', provider_family: 'glm' },
    ]);
    expect(names(ordered)).toEqual(['zt-glm-5.3', 'zt-glm-5.2', 'zt-glm-4.7']);
  });
});
