import { expect, it } from 'vitest';
import type { PricingEnvelope, PricingModel } from '../../api/contracts';
import { modelOfficialDiscount, modelPriceDetails } from './pricing';
import { publicImageItem } from './image-sale.fixture';

const pricing: PricingEnvelope = { models: [], group_ratio: { default: 99 }, usable_group: { default: 'Default' }, pricing_version: 'offline' };

it.each([
  [undefined, ['0.30 U / 张', '0.30 U / 张', '0.60 U / 张']],
  [{ currency: 'CNY' as const, cnyPerUnit: 6.5 }, ['1.95 元 / 张', '1.95 元 / 张', '3.90 元 / 张']],
  [{ currency: 'CNY' as const, cnyPerUnit: NaN }, ['0.30 U / 张', '0.30 U / 张', '0.60 U / 张']],
])('formats the three image tiers using only display FX (%j)', (display, expected) => {
  const model = publicImageItem() as unknown as PricingModel;
  const details = modelPriceDetails(model, pricing, 500_000, display);
  expect(details.map((price) => price.label)).toEqual(['1K', '2K', '4K']);
  expect(details.map((price) => price.price)).toEqual(expected);
});

it('does not derive a token official discount for image-sale models even with stale reference fields', () => {
  const item = publicImageItem();
  const model = { ...item, official_usd: { images: '1' },
    pricing_rules: item.pricing_rules.map((rule) => ({ ...rule, official_usd: { images: '1' } })),
  } as unknown as PricingModel;
  const prices = modelPriceDetails(model, pricing, 500_000);
  expect(prices.every((price) => price.officialPrice === undefined)).toBe(true);
  expect(modelOfficialDiscount(prices)).toBeNull();
});
