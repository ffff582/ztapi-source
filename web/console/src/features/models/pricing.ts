import type { PricingEnvelope, PricingModel } from '../../api/contracts';

const dimensionDetails: Record<string, { label: string; unit: string }> = {
  input_tokens: { label: '输入', unit: '1M tokens' },
  output_tokens: { label: '输出', unit: '1M tokens' },
  cache_read: { label: '缓存读取', unit: '1M tokens' },
  cache_write: { label: '缓存写入', unit: '1M tokens' },
  cache_write_5m: { label: '5 分钟缓存写入', unit: '1M tokens' },
  cache_write_1h: { label: '1 小时缓存写入', unit: '1M tokens' },
  image: { label: '图片', unit: '计费单位' },
  audio: { label: '音频', unit: '计费单位' },
  request: { label: '请求', unit: '次' },
};
const dimensionOrder = Object.keys(dimensionDetails);

export interface ModelPriceDetail {
  key: string;
  dimension: string;
  label: string;
  price: string;
}

function rankDimension(dimension: string) {
  const index = dimensionOrder.indexOf(dimension);
  return index < 0 ? dimensionOrder.length : index;
}

function orderedDimensions(dimensions: string[]) {
  return [...dimensions].sort(
    (a, b) => rankDimension(a) - rankDimension(b) || a.localeCompare(b),
  );
}

function formatSalePrice(value: string | undefined, unit: string) {
  if (typeof value !== 'string' || value === '') return null;
  const [whole, fraction = ''] = value.split('.');
  const trimmed = fraction.replace(/0+$/, '');
  return `$${whole}${trimmed ? `.${trimmed}` : ''} / ${unit}`;
}

export function modelPriceDetails(
  model: PricingModel,
  pricing: PricingEnvelope,
  quotaPerUnit: number,
): ModelPriceDetail[] {
  // A model priced by tier publishes one set of prices per tier instead of a
  // single rate, so every tier is listed with the condition it applies under.
  if (model.token_price_rules && model.token_price_rules.length > 0) {
    return model.token_price_rules.flatMap((rule, index) =>
      orderedDimensions(Object.keys(rule.sale_usd)).flatMap((dimension) => {
        const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
        const price = formatSalePrice(rule.sale_usd[dimension], detail.unit);
        if (price === null) return [];
        const condition = rule.conditions.join('、');
        return [{
          key: `${index}-${dimension}`,
          dimension,
          label: condition ? `${detail.label}（${condition}）` : detail.label,
          price,
        }];
      }),
    );
  }

  const saleUSD = model.sale_usd;
  if (!saleUSD || !model.billing_dimensions) {
    const legacy = modelPrices(model, pricing, quotaPerUnit);
    return [
      { key: 'input_tokens', dimension: 'input_tokens', label: '输入', price: legacy.input },
      { key: 'output_tokens', dimension: 'output_tokens', label: '输出', price: legacy.output },
    ];
  }
  return orderedDimensions(model.billing_dimensions).flatMap((dimension) => {
    const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
    const price = formatSalePrice(saleUSD[dimension], detail.unit);
    if (price === null) return [];
    return [{ key: dimension, dimension, label: detail.label, price }];
  });
}

function formatPrice(value: number) {
  return new Intl.NumberFormat('en-US', {
    minimumFractionDigits: 0,
    maximumFractionDigits: 6,
  }).format(value);
}

function effectiveGroupRatio(model: PricingModel, pricing: PricingEnvelope) {
  const enabledGroups = model.enable_groups.includes('all')
    ? Object.keys(pricing.usable_group)
    : model.enable_groups;
  if (enabledGroups.length === 0) return null;

  const ratios = enabledGroups.map((group) => pricing.group_ratio[group]);
  if (ratios.some((ratio) => ratio === undefined)) return null;
  const firstRatio = ratios[0];
  return ratios.some((ratio) => ratio !== firstRatio) ? null : firstRatio;
}

export function modelPrices(
  model: PricingModel,
  pricing: PricingEnvelope,
  quotaPerUnit: number,
) {
  if (
    (model.billing_mode !== '' && model.billing_mode !== 'ratio') ||
    model.billing_expr.trim() !== '' ||
    (model.quota_type === 0 &&
      [
        model.cache_ratio,
        model.create_cache_ratio,
        model.image_ratio,
        model.audio_ratio,
        model.audio_completion_ratio,
      ].some((ratio) => ratio !== undefined))
  ) {
    return { input: '按规则计费', output: '按规则计费' };
  }

  const groupRatio = effectiveGroupRatio(model, pricing);
  if (groupRatio === null) {
    return { input: '按规则计费', output: '按规则计费' };
  }

  if (model.quota_type === 0) {
    const input = model.model_ratio * (1_000_000 / quotaPerUnit) * groupRatio;
    const output = input * model.completion_ratio;
    if (!Number.isFinite(input) || !Number.isFinite(output)) {
      return { input: '按规则计费', output: '按规则计费' };
    }
    return {
      input: `$${formatPrice(input)} / 1M tokens`,
      output: `$${formatPrice(output)} / 1M tokens`,
    };
  }

  const fixedPrice = model.model_price * groupRatio;
  if (!Number.isFinite(fixedPrice)) {
    return { input: '按规则计费', output: '按规则计费' };
  }
  const fixed = `$${formatPrice(fixedPrice)} / 次`;
  return { input: fixed, output: fixed };
}
