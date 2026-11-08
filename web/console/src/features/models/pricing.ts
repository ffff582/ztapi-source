import type { OfficialPriceReference, PricingEnvelope, PricingModel } from '../../api/contracts';

export interface PriceDisplay {
  currency: 'CNY' | 'U';
  cnyPerUnit?: number | null;
  cnyLabel?: '元' | 'CNY';
  referenceDate?: string;
  referenceStale?: boolean;
}

export function isValidDisplayRate(rate: unknown): rate is number {
  return typeof rate === 'number' && Number.isFinite(rate) && rate >= 0.000001 && rate <= 1_000_000;
}

export function priceDisplayFactor(display?: PriceDisplay) {
  const rate = display?.cnyPerUnit;
  return display?.currency === 'CNY' && isValidDisplayRate(rate) ? rate : 1;
}

function isCNYDisplay(display?: PriceDisplay) {
  const rate = display?.cnyPerUnit;
  return display?.currency === 'CNY' && isValidDisplayRate(rate);
}

const unitPriceFormatter = new Intl.NumberFormat('en-US', { maximumSignificantDigits: 6 });

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
  officialDiscount?: number;
  officialDifferencePercent?: number;
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
export function formatUnitPrice(value: string, unit: string, display?: PriceDisplay) {
  const amount = Number(value) * priceDisplayFactor(display);
  if (!Number.isFinite(amount)) return `${value} U / ${unit}`;
  return `${unitPriceFormatter.format(amount)} ${isCNYDisplay(display) ? display?.cnyLabel ?? '元' : 'U'} / ${unit}`;
}

function formatSalePrice(value: string | undefined, unit: string, display?: PriceDisplay) {
  if (typeof value !== 'string' || value === '') return null;
  return formatUnitPrice(value, unit, display);
}

function comparisonFields(saleRaw: string | undefined, officialRaw: string | undefined, unit: string, display?: PriceDisplay, metadata?: OfficialPriceReference) {
  if (typeof officialRaw !== 'string' || officialRaw.trim() === '') return {};
  const sale = Number(saleRaw) * priceDisplayFactor(display);
  const originalRate = Number(metadata?.original_units_per_usd);
  const nativeCNY = isCNYDisplay(display) && metadata?.original_currency === 'CNY' && Number.isFinite(originalRate) && originalRate > 0;
  const official = Number(officialRaw) * (nativeCNY ? originalRate : priceDisplayFactor(display));
  if (!Number.isFinite(official) || official < 0) return {};
  if (isCNYDisplay(display) && !metadata?.original_currency) {
    return { officialPrice: formatUnitPrice(officialRaw, unit) };
  }
  const reference = { officialPrice: nativeCNY
    ? `${unitPriceFormatter.format(official)} ${display?.cnyLabel ?? '元'} / ${unit}` : formatUnitPrice(officialRaw, unit, display) };
  if (typeof saleRaw !== 'string' || saleRaw.trim() === '' || !Number.isFinite(sale) || sale < 0 || official === 0) return reference;
  const difference = ((sale - official) / official) * 100;
  if (!Number.isFinite(difference)) return reference;
  return {
    ...reference,
    officialDifferencePercent: Number(difference.toPrecision(12)),
    ...(official > sale ? { savingsPercent: Math.round(-difference) } : {}),
    ...(official > sale ? { officialDiscount: Math.ceil((sale / official) * 1000 - 1e-8) / 100 } : {}),
  };
}

export function modelOfficialDiscount(prices: ModelPriceDetail[]) {
  if (prices.length === 0 || prices.some((price) => !price.officialPrice || price.officialDiscount === undefined ||
    !Number.isFinite(price.officialDiscount) || price.officialDiscount < 0 || price.officialDiscount >= 10)) return null;
  const discounts = prices.map((price) => price.officialDiscount as number);
  const lowest = Math.min(...discounts);
  const highest = Math.max(...discounts);
  return lowest === highest ? `${lowest}` : `${lowest}–${highest}`;
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
  return condition ? `${fallback}（${condition}）` : fallback;
}

export function modelPriceDetails(
  model: PricingModel,
  pricing: PricingEnvelope,
  quotaPerUnit: number,
  display?: PriceDisplay,
): ModelPriceDetail[] {
  if (model.pricing_rules && model.pricing_rules.length > 0) {
    return model.pricing_rules.flatMap((rule) =>
      orderedDimensions(Object.keys(rule.sale_usd)).flatMap((dimension) => {
        const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
        const unit = rule.billing_unit === 'usd_per_million_tokens' ? '1M tokens' : rule.billing_unit;
        const price = formatSalePrice(rule.sale_usd[dimension], unit, display);
        if (price === null) return [];
        return [{
          key: `${rule.id}-${dimension}`,
          dimension,
          label: mediaRuleLabel(rule.conditions, detail.label),
          price,
          ...comparisonFields(rule.sale_usd[dimension], rule.official_usd?.[dimension], unit, display, model.official_reference),
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
        const price = formatSalePrice(rule.sale_usd[dimension], detail.unit, display);
        if (price === null) return [];
        const condition = rule.conditions.join('、');
        return [{
          key: `${index}-${dimension}`,
          dimension,
          label: condition ? `${detail.label}（${condition}）` : detail.label,
          price,
          ...comparisonFields(rule.sale_usd[dimension], rule.official_usd?.[dimension], detail.unit, display, model.official_reference),
        }];
      }),
    );
  }

  const saleUSD = model.sale_usd;
  if (!saleUSD || !model.billing_dimensions) {
    const legacy = modelPrices(model, pricing, quotaPerUnit, display);
    return [
      { key: 'input_tokens', dimension: 'input_tokens', label: '输入', price: legacy.input },
      { key: 'output_tokens', dimension: 'output_tokens', label: '输出', price: legacy.output },
    ];
  }
  return orderedDimensions(model.billing_dimensions).flatMap((dimension) => {
    const detail = dimensionDetails[dimension] ?? { label: dimension, unit: '计费单位' };
    const price = formatSalePrice(saleUSD[dimension], detail.unit, display);
    if (price === null) return [];
    return [{
      key: dimension,
      dimension,
      label: detail.label,
      price,
      ...comparisonFields(saleUSD[dimension], model.official_usd?.[dimension], detail.unit, display, model.official_reference),
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
  display?: PriceDisplay,
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
      input: display ? formatUnitPrice(String(input), '1M tokens', display) : `${formatPrice(input)} U / 1M tokens`,
      output: display ? formatUnitPrice(String(output), '1M tokens', display) : `${formatPrice(output)} U / 1M tokens`,
    };
  }

  const fixedPrice = model.model_price * groupRatio;
  if (!Number.isFinite(fixedPrice)) {
    return { input: '按规则计费', output: '按规则计费' };
  }
  const fixed = display ? formatUnitPrice(String(fixedPrice), '次', display) : `${formatPrice(fixedPrice)} U / 次`;
  return { input: fixed, output: fixed };
}
