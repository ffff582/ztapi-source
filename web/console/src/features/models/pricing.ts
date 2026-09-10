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

export function modelPriceDetails(model: PricingModel, pricing: PricingEnvelope, quotaPerUnit: number) {
  const saleUSD = model.sale_usd;
  if (!saleUSD || !model.billing_dimensions) {
    const legacy = modelPrices(model, pricing, quotaPerUnit);
    return [
      { dimension: 'input_tokens', label: '输入', price: legacy.input },
      { dimension: 'output_tokens', label: '输出', price: legacy.output },
    ];
  }
  const rank = (dimension: string) => {
    const index = dimensionOrder.indexOf(dimension);
    return index < 0 ? dimensionOrder.length : index;
  };
  return [...model.billing_dimensions]
    .sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
    .map((dimension) => {
      const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
      const [whole, fraction = ''] = saleUSD[dimension].split('.');
      const trimmed = fraction.replace(/0+$/, '');
      return {
        dimension, label: detail.label,
        price: `$${whole}${trimmed ? `.${trimmed}` : ''} / ${detail.unit}`,
      };
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
