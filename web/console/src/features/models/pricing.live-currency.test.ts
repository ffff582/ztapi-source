import { expect, it } from 'vitest';
import { parsePricingEnvelope } from '../../api/contracts';
import fixture from './live-pricing-20261107.fixture.json';
import benchmarks from '../../../../../server/model/ztapi_official_comparison_20261107.json';
import { modelPriceDetails } from './pricing';

it('converts every current published dimension without modifying any U price or specification', () => {
  const raw = structuredClone(fixture);
  for (const item of raw.data) {
    const benchmark = benchmarks.models.find((row) => row.public_name === item.model_name);
    if (benchmark && item.official_reference) Object.assign(item.official_reference, benchmark.reference);
  }
  const pricing = parsePricingEnvelope(raw);
  const before = JSON.stringify(pricing);
  expect(pricing.models).toHaveLength(16);
  let count = 0;
  for (const model of pricing.models) {
    const u = modelPriceDetails(model, pricing, 500_000);
    const cny = modelPriceDetails(model, pricing, 500_000, { currency: 'CNY', cnyPerUnit: 7.3 });
    expect(cny.map((price) => [price.key, price.label, price.dimension])).toEqual(u.map((price) => [price.key, price.label, price.dimension]));
    for (const [index, price] of cny.entries()) {
      expect(price.price).toContain(' 元 / ');
      expect(u[index].price).toContain(' U / ');
      expect(Boolean(price.officialPrice)).toBe(Boolean(u[index].officialPrice));
      count++;
    }
    if (model.model_name === 'zt-deepseek-v4-pro') expect(cny.find((price) => price.dimension === 'input_tokens')?.officialPrice).toBe('9 元 / 1M tokens');
    if (model.model_name === 'zt-glm-5') expect(cny[0].officialPrice).toBe('4 元 / 1M tokens');
    if (model.model_name === 'zt-claude-fable-5') expect(cny.find((price) => price.dimension === 'cache_write')?.officialPrice).toBeUndefined();
  }
  expect(count).toBe(78);
  expect(JSON.stringify(pricing)).toBe(before);
});
