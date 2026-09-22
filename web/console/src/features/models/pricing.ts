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
  officialPrice?: string;
  savingsPercent?: number;
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

// Prices are quoted in U, the currency customers top up and are billed in.
// Billing keeps every published decimal; a reader only needs enough of them to
// compare models, so the display rounds a long tail away.
export function formatUnitPrice(value: string, unit: string) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return `${value} U / ${unit}`;
  // Six significant digits keep even the cheapest model's price visible while
  // dropping a ten-decimal tail nobody compares prices with.
  let text = Math.abs(amount) >= 1e-6 ? amount.toPrecision(6) : amount.toFixed(10);
  if (text.includes('e')) text = amount.toFixed(10);
  const [whole, fraction = ''] = text.split('.');
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const trimmed = fraction.replace(/0+$/, '');
  return `${grouped}${trimmed ? `.${trimmed}` : ''} U / ${unit}`;
}

function formatSalePrice(value: string | undefined, unit: string) {
  if (typeof value !== 'string' || value === '') return null;
  return formatUnitPrice(value, unit);
}

function comparisonFields(saleRaw: string | undefined, officialRaw: string | undefined, unit: string) {
  if (typeof officialRaw !== 'string' || officialRaw === '') return {};
  const sale = Number(saleRaw);
  const official = Number(officialRaw);
  const savingsPercent = Number.isFinite(sale) && Number.isFinite(official) && official > sale
    ? Math.round((1 - sale / official) * 100)
    : undefined;
  return {
    officialPrice: formatUnitPrice(officialRaw, unit),
    ...(savingsPercent === undefined ? {} : { savingsPercent }),
  };
}

const mediaConditionLabels: Record<string, Record<string, string>> = {
  token_bucket: {
    text_input: '文本输入',
    text_cached_input: '文本缓存输入',
    image_input: '图片输入',
    image_cached_input: '图片缓存输入',
    image_output: '图片输出',
  },
  contains_video_input: {
    true: '含视频输入',
    false: '无视频输入',
  },
  prompt_tokens_tier: {
    lte_200k: '输入不超过 200K',
    gt_200k: '输入超过 200K',
  },
};

function mediaRuleLabel(conditions: Record<string, string>, fallback: string) {
  const condition = Object.entries(conditions)
    .map(([key, value]) => mediaConditionLabels[key]?.[value] ?? value)
    .filter(Boolean)
    .join(' · ');
  return condition || fallback;
}

export function modelPriceDetails(
  model: PricingModel,
  pricing: PricingEnvelope,
  quotaPerUnit: number,
): ModelPriceDetail[] {
  if (model.pricing_rules && model.pricing_rules.length > 0) {
    return model.pricing_rules.flatMap((rule) =>
      orderedDimensions(Object.keys(rule.sale_usd)).flatMap((dimension) => {
        const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
        const unit = rule.billing_unit === 'usd_per_million_tokens' ? '1M tokens' : rule.billing_unit;
        const price = formatSalePrice(rule.sale_usd[dimension], unit);
        if (price === null) return [];
        return [{
          key: `${rule.id}-${dimension}`,
          dimension,
          label: mediaRuleLabel(rule.conditions, detail.label),
          price,
          ...comparisonFields(rule.sale_usd[dimension], rule.official_usd?.[dimension], unit),
        }];
      }),
    );
  }

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
          ...comparisonFields(rule.sale_usd[dimension], rule.official_usd?.[dimension], detail.unit),
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
    return [{
      key: dimension,
      dimension,
      label: detail.label,
      price,
      ...comparisonFields(saleUSD[dimension], model.official_usd?.[dimension], detail.unit),
    }];
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
      input: `${formatPrice(input)} U / 1M tokens`,
      output: `${formatPrice(output)} U / 1M tokens`,
    };
  }

  const fixedPrice = model.model_price * groupRatio;
  if (!Number.isFinite(fixedPrice)) {
    return { input: '按规则计费', output: '按规则计费' };
  }
  const fixed = `${formatPrice(fixedPrice)} U / 次`;
  return { input: fixed, output: fixed };
}
