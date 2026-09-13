export interface AuthUser {
  id: number;
  username: string;
  role: number;
  group: string;
}

export interface AuthSessionData {
  access_token: string;
  expires_in: number;
  user: AuthUser;
}

export interface AuthCredentials {
  username: string;
  password: string;
}

export interface ApiSuccess<T> {
  success: true;
  data: T;
}

export interface ApiFailure {
  success: false;
  message: string;
}

export type ApiResponse<T> = ApiSuccess<T> | ApiFailure;

export interface PageEnvelope<T> {
  page: number;
  page_size: number;
  total: number;
  items: T[];
}

export interface PricingModel {
  modality?: UserModelModality;
  provider_family?: string;
  vendor_name?: string;
  input_price_per_million?: string;
  output_price_per_million?: string;
  billing_dimensions?: string[];
  sale_usd?: Record<string, string>;
  billing_rule?: UserModelCatalogItem['billing_rule'];
  supported_endpoint_types?: UserModelEndpointType[];
  supported_options?: UserModelSupportedOptions;
  pricing_rules?: UserModelPricingRule[];
  billing_unit?: string;
  model_name: string;
  description: string;
  quota_type: 0 | 1;
  model_ratio: number;
  model_price: number;
  owner_by: string;
  completion_ratio: number;
  cache_ratio?: number;
  create_cache_ratio?: number;
  image_ratio?: number;
  audio_ratio?: number;
  audio_completion_ratio?: number;
  enable_groups: string[];
  billing_mode: string;
  billing_expr: string;
}

export interface PricingEnvelope {
  models: PricingModel[];
  group_ratio: Record<string, number>;
  usable_group: Record<string, string>;
  pricing_version: string;
}

export interface RuntimeStatus {
  quota_per_unit: number;
}

export interface UserTopUp {
  id: number;
  amount: number;
  money: number;
  trade_no: string;
  payment_provider: string;
  payment_method: string;
  create_time: number;
  complete_time: number;
  status: string;
}

export type PublicModelFamily = 'OpenAI' | 'Claude' | 'Gemini';

export type UserModelProtocol =
  | 'openai_compatible'
  | 'anthropic'
  | 'gemini';

export type UserModelModality = 'text' | 'embedding' | 'image' | 'video';

export type UserModelEndpointType =
  | 'openai'
  | 'openai-response'
  | 'embeddings'
  | 'anthropic'
  | 'gemini'
  | 'images'
  | 'video-tasks';

export interface UserModelSupportedOptions {
  sizes?: string[];
  qualities?: string[];
  response_formats?: string[];
  min_count?: number;
  max_count?: number;
  resolutions?: string[];
  duration_seconds?: number[];
  supports_video_input?: boolean;
}

export interface UserModelPricingRule {
  id: string;
  conditions: Record<string, string>;
  billing_unit: string;
  sale_usd: Record<string, string>;
}

export interface UserModelCatalogItem {
  modality: UserModelModality;
  model_name: string;
  provider_family: string;
  provider_name: string;
  protocol: UserModelProtocol;
  enable_groups: string[];
  supported_endpoint_types: UserModelEndpointType[];
  input_price_per_million: string;
  output_price_per_million: string;
  billing_dimensions: string[];
  sale_usd: Record<string, string>;
  billing_rule: 'token' | 'multi_dimension' | 'input_only';
  pricing_version: string;
  supported_options?: UserModelSupportedOptions;
  pricing_rules?: UserModelPricingRule[];
  billing_unit?: string;
}

export interface UserModelCatalogEnvelope {
  models: string[];
  catalog: UserModelCatalogItem[];
}

export interface UserLogItem {
  timestamp: number;
  request_id: string;
  model: string;
  status: 'success' | 'error' | 'info';
  latency: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  billed_amount: number;
  error_code?: string;
}

export interface UserLogStat {
  rpm: number;
  tpm: number;
}

export interface UserToken {
  id: number;
  status: number;
  name: string;
  created_time: number;
  accessed_time: number;
  expired_time: number;
  remain_quota: number;
  unlimited_quota: boolean;
  model_limits_enabled: boolean;
  model_limits: string;
  allow_ips: string;
  key_prefix: string;
}

export interface CreatedToken {
  id: number;
  key: string;
  key_prefix: string;
}

export interface USDTTopUpInfo {
  enabled: boolean;
  network: 'tron-mainnet';
  asset: 'USDT';
  minimum_top_up: number;
  order_ttl_seconds: number;
}

export type USDTTopUpOrderStatus =
  | 'pending'
  | 'confirming'
  | 'settled'
  | 'expired'
  | 'manual_review';

export interface USDTTopUpOrder {
  id: number;
  trade_no: string;
  credit_units: number;
  pay_amount: string;
  receiving_address: string;
  network: 'tron-mainnet';
  asset: 'USDT';
  expires_at: number;
  status: USDTTopUpOrderStatus;
  tx_id?: string;
  settled_at?: number;
  review_reason?: string;
}

export class DataContractError extends Error {
  constructor() {
    super('Unexpected API response.');
    this.name = 'DataContractError';
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function finiteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

function integer(value: unknown): value is number {
  return finiteNumber(value) && Number.isInteger(value);
}

function nonNegativeInteger(value: unknown): value is number {
  return integer(value) && value >= 0;
}

function requiredString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

function positiveDecimalString(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    /^(?:0|[1-9]\d*)(?:\.\d+)?$/.test(value) &&
    Number.isFinite(Number(value)) &&
    Number(value) > 0
  );
}

function canonicalDecimalString(value: string) {
  const [integerPart, fractionPart = ''] = value.split('.');
  const trimmedFraction = fractionPart.replace(/0+$/, '');
  return trimmedFraction.length > 0
    ? `${integerPart}.${trimmedFraction}`
    : integerPart;
}

function parseRequiredStringArray(value: unknown): string[] {
  if (
    !Array.isArray(value) ||
    value.length === 0 ||
    value.some((item) => !requiredString(item))
  ) {
    throw new DataContractError();
  }
  return [...value];
}

function parseStringArray(value: unknown): string[] {
  if (!Array.isArray(value) || value.some((item) => !requiredString(item))) {
    throw new DataContractError();
  }
  return [...value];
}

function parseRequiredStringRecord(value: unknown): Record<string, string> {
  if (!isRecord(value)) {
    throw new DataContractError();
  }
  const result: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (!requiredString(key) || !requiredString(entry)) {
      throw new DataContractError();
    }
    result[key] = entry;
  }
  return result;
}

function parseDecimalStringRecord(value: unknown): Record<string, string> {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const result: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (!requiredString(key) || !positiveDecimalString(entry)) {
      throw new DataContractError();
    }
    result[key] = entry;
  }
  return result;
}

function parseUserModelCatalogItem(value: unknown): UserModelCatalogItem {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const forbiddenFields = [
    'source_model',
    'channel_id',
    'channel_ids',
    'price_source_id',
    'evidence_id',
  ];
  if (forbiddenFields.some((field) => field in value)) {
    throw new DataContractError();
  }

  const protocol = value.protocol;
  const endpointTypes = parseRequiredStringArray(value.supported_endpoint_types);
  const enableGroups = parseRequiredStringArray(value.enable_groups);
  const modality = value.modality ?? 'text';
  const media = modality === 'image' || modality === 'video';
  const billingDimensions = media
    ? parseStringArray(value.billing_dimensions)
    : parseRequiredStringArray(value.billing_dimensions);
  const saleUSD = parseDecimalStringRecord(value.sale_usd);
  const inputPrice = value.input_price_per_million;
  const outputPrice = value.output_price_per_million;
  const billingRule = value.billing_rule;
  const isEmbedding = modality === 'embedding';
  const allowedEndpoints: Record<UserModelProtocol, UserModelEndpointType[]> = {
    openai_compatible: ['openai', 'openai-response', 'embeddings', 'images', 'video-tasks'],
    anthropic: ['anthropic'],
    gemini: ['gemini'],
  };
  if (
    !requiredString(value.model_name) ||
    !requiredString(value.provider_family) ||
    !requiredString(value.provider_name) ||
    !['openai_compatible', 'anthropic', 'gemini'].includes(String(protocol)) ||
    endpointTypes.some(
      (endpoint) => !allowedEndpoints[protocol as UserModelProtocol].includes(endpoint as UserModelEndpointType),
    ) ||
    new Set(endpointTypes).size !== endpointTypes.length ||
    !['text', 'embedding', 'image', 'video'].includes(String(modality)) ||
    (endpointTypes.includes('embeddings') !== isEmbedding) ||
    (isEmbedding && (endpointTypes.length !== 1 || endpointTypes[0] !== 'embeddings')) ||
    (modality === 'image' && (endpointTypes.length !== 1 || endpointTypes[0] !== 'images')) ||
    (modality === 'video' && (endpointTypes.length !== 1 || endpointTypes[0] !== 'video-tasks')) ||
    ((modality === 'text' || isEmbedding) && endpointTypes.some((endpoint) => endpoint === 'images' || endpoint === 'video-tasks')) ||
    typeof inputPrice !== 'string' ||
    (media ? inputPrice !== '' : !positiveDecimalString(inputPrice)) ||
    typeof outputPrice !== 'string' ||
    (media ? outputPrice !== '' : isEmbedding ? !/^0(?:\.0+)?$/.test(outputPrice) : !positiveDecimalString(outputPrice)) ||
    !['token', 'multi_dimension', 'input_only'].includes(String(billingRule)) ||
    !requiredString(value.pricing_version)
  ) {
    throw new DataContractError();
  }

  if (media) {
    if (
      billingDimensions.length !== 0 ||
      Object.keys(saleUSD).length !== 0 ||
      billingRule !== 'multi_dimension' ||
      !requiredString(value.billing_unit) ||
      !isRecord(value.supported_options) ||
      !Array.isArray(value.pricing_rules) ||
      value.pricing_rules.length === 0
    ) {
      throw new DataContractError();
    }
    const options = value.supported_options;
    const supportedOptions: UserModelSupportedOptions = {};
    if (modality === 'image') {
      supportedOptions.sizes = parseRequiredStringArray(options.sizes);
      supportedOptions.qualities = parseRequiredStringArray(options.qualities);
      supportedOptions.response_formats = parseRequiredStringArray(options.response_formats);
      if (!integer(options.min_count) || !integer(options.max_count) || options.min_count < 1 || options.max_count < options.min_count) {
        throw new DataContractError();
      }
      if ('resolutions' in options || 'duration_seconds' in options || 'supports_video_input' in options) {
        throw new DataContractError();
      }
      supportedOptions.min_count = options.min_count;
      supportedOptions.max_count = options.max_count;
    } else {
      supportedOptions.resolutions = parseRequiredStringArray(options.resolutions);
      if (!Array.isArray(options.duration_seconds) || options.duration_seconds.length === 0 || options.duration_seconds.some((duration) => !integer(duration) || duration <= 0) || typeof options.supports_video_input !== 'boolean') {
        throw new DataContractError();
      }
      if ('sizes' in options || 'qualities' in options || 'response_formats' in options || 'min_count' in options || 'max_count' in options) {
        throw new DataContractError();
      }
      supportedOptions.duration_seconds = [...options.duration_seconds];
      supportedOptions.supports_video_input = options.supports_video_input;
    }
    const pricingRules = value.pricing_rules.map((rule): UserModelPricingRule => {
      if (!isRecord(rule) || !requiredString(rule.id) || !requiredString(rule.billing_unit)) {
        throw new DataContractError();
      }
      const conditions = parseRequiredStringRecord(rule.conditions);
      const ruleSaleUSD = parseDecimalStringRecord(rule.sale_usd);
      if (
        Object.keys(conditions).length === 0 ||
        Object.keys(ruleSaleUSD).length === 0 ||
        (value.billing_unit !== 'mixed' && rule.billing_unit !== value.billing_unit)
      ) {
        throw new DataContractError();
      }
      return { id: rule.id, conditions, billing_unit: rule.billing_unit, sale_usd: ruleSaleUSD };
    });
    return {
      modality: modality as UserModelModality,
      model_name: value.model_name,
      provider_family: value.provider_family,
      provider_name: value.provider_name,
      protocol: protocol as UserModelProtocol,
      enable_groups: enableGroups,
      supported_endpoint_types: endpointTypes as UserModelEndpointType[],
      input_price_per_million: inputPrice,
      output_price_per_million: outputPrice,
      billing_dimensions: billingDimensions,
      sale_usd: saleUSD,
      billing_rule: billingRule as UserModelCatalogItem['billing_rule'],
      pricing_version: value.pricing_version,
      supported_options: supportedOptions,
      pricing_rules: pricingRules,
      billing_unit: value.billing_unit,
    };
  }

  const dimensionSet = new Set(billingDimensions);
  const saleDimensions = Object.keys(saleUSD);
  const expectedBillingRule =
    isEmbedding ? 'input_only' : billingDimensions.length === 2 &&
    billingDimensions[0] === 'input_tokens' &&
    billingDimensions[1] === 'output_tokens'
      ? 'token'
      : 'multi_dimension';
  if (
    (isEmbedding && (billingDimensions.length !== 1 || billingDimensions[0] !== 'input_tokens')) ||
    dimensionSet.size !== billingDimensions.length ||
    saleDimensions.length !== dimensionSet.size ||
    saleDimensions.some((dimension) => !dimensionSet.has(dimension)) ||
    !saleUSD.input_tokens ||
    (!isEmbedding && !saleUSD.output_tokens) ||
    canonicalDecimalString(inputPrice) !==
      canonicalDecimalString(saleUSD.input_tokens) ||
    (!isEmbedding && canonicalDecimalString(outputPrice) !==
      canonicalDecimalString(saleUSD.output_tokens)) ||
    billingRule !== expectedBillingRule
  ) {
    throw new DataContractError();
  }

  return {
    modality: modality as UserModelModality,
    model_name: value.model_name,
    provider_family: value.provider_family,
    provider_name: value.provider_name,
    protocol: protocol as UserModelProtocol,
    enable_groups: enableGroups,
    supported_endpoint_types: endpointTypes as UserModelEndpointType[],
    input_price_per_million: inputPrice,
    output_price_per_million: outputPrice,
    billing_dimensions: billingDimensions,
    sale_usd: saleUSD,
    billing_rule: billingRule as UserModelCatalogItem['billing_rule'],
    pricing_version: value.pricing_version,
  };
}

export function parseUserModelCatalog(value: unknown): UserModelCatalogEnvelope {
  if (!isRecord(value) || value.success !== true || !Array.isArray(value.data)) {
    throw new DataContractError();
  }

  const models = value.data.map((model) => {
    if (!requiredString(model)) {
      throw new DataContractError();
    }
    return model;
  });
  if (!Array.isArray(value.catalog)) {
    throw new DataContractError();
  }
  const catalog = value.catalog.map(parseUserModelCatalogItem);
  const modelNames = new Set(models);
  const catalogNames = new Set(catalog.map((item) => item.model_name));
  if (
    modelNames.size !== models.length ||
    catalogNames.size !== catalog.length ||
    modelNames.size !== catalogNames.size ||
    [...modelNames].some((model) => !catalogNames.has(model))
  ) {
    throw new DataContractError();
  }

  return { models, catalog };
}

export function parseAuthUser(value: unknown): AuthUser {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  if (
    !integer(value.id) ||
    value.id <= 0 ||
    !requiredString(value.username) ||
    !integer(value.role) ||
    typeof value.group !== 'string'
  ) {
    throw new DataContractError();
  }

  return {
    id: value.id,
    username: value.username,
    role: value.role,
    group: value.group,
  };
}

export function parseUSDTTopUpInfo(value: unknown): USDTTopUpInfo {
  if (
    !isRecord(value) ||
    typeof value.enable_usdt_trc20_topup !== 'boolean' ||
    value.usdt_trc20_network !== 'tron-mainnet' ||
    value.usdt_trc20_asset !== 'USDT' ||
    !integer(value.usdt_trc20_min_topup) ||
    value.usdt_trc20_min_topup < 1 ||
    !integer(value.usdt_trc20_order_ttl_seconds) ||
    value.usdt_trc20_order_ttl_seconds < 1
  ) {
    throw new DataContractError();
  }

  return {
    enabled: value.enable_usdt_trc20_topup,
    network: value.usdt_trc20_network,
    asset: value.usdt_trc20_asset,
    minimum_top_up: value.usdt_trc20_min_topup,
    order_ttl_seconds: value.usdt_trc20_order_ttl_seconds,
  };
}

export function parseUSDTTopUpOrder(value: unknown): USDTTopUpOrder {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const status = value.status;
  const txID = value.tx_id;
  const settledAt = value.settled_at;
  const reviewReason = value.review_reason;
  if (
    !integer(value.id) ||
    value.id < 1 ||
    !requiredString(value.trade_no) ||
    !integer(value.credit_units) ||
    value.credit_units < 1 ||
    typeof value.pay_amount !== 'string' ||
    !/^\d+\.\d{2}$/.test(value.pay_amount) ||
    !requiredString(value.receiving_address) ||
    value.network !== 'tron-mainnet' ||
    value.asset !== 'USDT' ||
    !integer(value.expires_at) ||
    value.expires_at < 1 ||
    !['pending', 'confirming', 'settled', 'expired', 'manual_review'].includes(String(status)) ||
    (txID !== undefined && typeof txID !== 'string') ||
    (settledAt !== undefined && (!integer(settledAt) || settledAt < 1)) ||
    (reviewReason !== undefined && typeof reviewReason !== 'string')
  ) {
    throw new DataContractError();
  }

  return {
    id: value.id,
    trade_no: value.trade_no,
    credit_units: value.credit_units,
    pay_amount: value.pay_amount,
    receiving_address: value.receiving_address,
    network: value.network,
    asset: value.asset,
    expires_at: value.expires_at,
    status: status as USDTTopUpOrderStatus,
    ...(typeof txID === 'string' ? { tx_id: txID } : {}),
    ...(typeof settledAt === 'number' ? { settled_at: settledAt } : {}),
    ...(typeof reviewReason === 'string' ? { review_reason: reviewReason } : {}),
  };
}

export function getPublicModelFamily(
  modelName: string,
  ownerBy = '',
  providerFamily?: string,
): PublicModelFamily | null {
  if (providerFamily !== undefined) {
    const families: Record<string, PublicModelFamily> = {
      openai: 'OpenAI', anthropic: 'Claude', google: 'Gemini',
    };
    return families[providerFamily] ?? null;
  }
  const name = modelName.trim().toLowerCase();
  const owner = ownerBy.trim().toLowerCase();

  if (
    name.startsWith('gpt-') ||
    name.startsWith('chatgpt-') ||
    /^o[134](?:-|$)/.test(name) ||
    owner === 'openai'
  ) {
    return 'OpenAI';
  }
  if (name.startsWith('claude-') || owner === 'anthropic') {
    return 'Claude';
  }
  if (name.startsWith('gemini-') || owner === 'google') {
    return 'Gemini';
  }
  return null;
}

export function parsePricingModels(value: unknown): PricingModel[] {
  if (!Array.isArray(value)) {
    throw new DataContractError();
  }

  return value.map((item) => {
    if (
      !isRecord(item) ||
      !requiredString(item.model_name) ||
      (item.description !== undefined && typeof item.description !== 'string') ||
      (item.quota_type !== 0 && item.quota_type !== 1) ||
      !finiteNumber(item.model_ratio) ||
      item.model_ratio < 0 ||
      !finiteNumber(item.model_price) ||
      item.model_price < 0 ||
      typeof item.owner_by !== 'string' ||
      !finiteNumber(item.completion_ratio) ||
      item.completion_ratio < 0 ||
      !Array.isArray(item.enable_groups) ||
      item.enable_groups.some((group) => !requiredString(group)) ||
      (item.billing_mode !== undefined &&
        typeof item.billing_mode !== 'string') ||
      (item.billing_expr !== undefined && typeof item.billing_expr !== 'string')
    ) {
      throw new DataContractError();
    }

    let managed: Partial<PricingModel> = {};
    if (['provider_family', 'vendor_name', 'sale_usd', 'billing_dimensions'].some((key) => key in item)) {
      const endpoints = parseRequiredStringArray(item.supported_endpoint_types);
      // Anonymous pricing uses the same managed prices, but omits protocol and modality.
      const catalog = parseUserModelCatalogItem({
        ...item,
        provider_name: item.vendor_name,
        protocol: endpoints[0] === 'anthropic' ? 'anthropic' : endpoints[0] === 'gemini' ? 'gemini' : 'openai_compatible',
        modality: item.modality ?? (endpoints.includes('embeddings') ? 'embedding' : endpoints.includes('images') ? 'image' : endpoints.includes('video-tasks') ? 'video' : 'text'),
      });
      managed = {
        provider_family: catalog.provider_family,
        vendor_name: catalog.provider_name,
        input_price_per_million: catalog.input_price_per_million,
        output_price_per_million: catalog.output_price_per_million,
        billing_dimensions: catalog.billing_dimensions,
        sale_usd: catalog.sale_usd,
        billing_rule: catalog.billing_rule,
        supported_endpoint_types: catalog.supported_endpoint_types,
        modality: catalog.modality,
        supported_options: catalog.supported_options,
        pricing_rules: catalog.pricing_rules,
        billing_unit: catalog.billing_unit,
      };
    }

    const optionalRatio = (ratio: unknown) => {
      if (ratio === undefined) {
        return undefined;
      }
      if (!finiteNumber(ratio) || ratio < 0) {
        throw new DataContractError();
      }
      return ratio;
    };

    return {
      model_name: item.model_name,
      description: item.description ?? '',
      quota_type: item.quota_type,
      model_ratio: item.model_ratio,
      model_price: item.model_price,
      owner_by: item.owner_by,
      completion_ratio: item.completion_ratio,
      cache_ratio: optionalRatio(item.cache_ratio),
      create_cache_ratio: optionalRatio(item.create_cache_ratio),
      image_ratio: optionalRatio(item.image_ratio),
      audio_ratio: optionalRatio(item.audio_ratio),
      audio_completion_ratio: optionalRatio(item.audio_completion_ratio),
      enable_groups: [...item.enable_groups],
      billing_mode: item.billing_mode ?? 'ratio',
      billing_expr: item.billing_expr ?? '',
      ...managed,
    };
  });
}

function parseNumberRecord(value: unknown): Record<string, number> {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const result: Record<string, number> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (!requiredString(key) || !finiteNumber(entry) || entry < 0) {
      throw new DataContractError();
    }
    result[key] = entry;
  }
  return result;
}

function parseStringRecord(value: unknown): Record<string, string> {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const result: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (!requiredString(key) || typeof entry !== 'string') {
      throw new DataContractError();
    }
    result[key] = entry;
  }
  return result;
}

export function parsePricingEnvelope(value: unknown): PricingEnvelope {
  if (
    !isRecord(value) ||
    value.success !== true ||
    !requiredString(value.pricing_version)
  ) {
    throw new DataContractError();
  }

  return {
    models: parsePricingModels(value.data),
    group_ratio: parseNumberRecord(value.group_ratio),
    usable_group: parseStringRecord(value.usable_group),
    pricing_version: value.pricing_version,
  };
}

export function parseRuntimeStatus(value: unknown): RuntimeStatus {
  if (
    !isRecord(value) ||
    !finiteNumber(value.quota_per_unit) ||
    value.quota_per_unit <= 0
  ) {
    throw new DataContractError();
  }

  return { quota_per_unit: value.quota_per_unit };
}

export function parseAccountQuota(value: unknown): number {
  if (!isRecord(value) || !integer(value.quota) || !Number.isSafeInteger(value.quota)) {
    throw new DataContractError();
  }
  return value.quota;
}

export function parseUserTopUpPage(value: unknown): PageEnvelope<UserTopUp> {
  if (!isRecord(value)) {
    throw new DataContractError();
  }
  return parsePageEnvelope(value, (item) => {
    if (
      !isRecord(item) || !nonNegativeInteger(item.id) ||
      !nonNegativeInteger(item.amount) || !Number.isSafeInteger(item.amount) ||
      !finiteNumber(item.money) || item.money < 0 ||
      !requiredString(item.trade_no) || typeof item.payment_provider !== 'string' ||
      typeof item.payment_method !== 'string' || !requiredString(item.status) ||
      !nonNegativeInteger(item.create_time) || !nonNegativeInteger(item.complete_time)
    ) {
      throw new DataContractError();
    }
    return {
      id: item.id, amount: item.amount, money: item.money, trade_no: item.trade_no,
      payment_provider: item.payment_provider, payment_method: item.payment_method,
      create_time: item.create_time, complete_time: item.complete_time, status: item.status,
    };
  });
}

function parsePageEnvelope<T>(
  value: unknown,
  parseItem: (item: unknown) => T,
): PageEnvelope<T> {
  if (
    !isRecord(value) ||
    !integer(value.page) ||
    value.page < 1 ||
    !nonNegativeInteger(value.page_size) ||
    !nonNegativeInteger(value.total) ||
    !Array.isArray(value.items)
  ) {
    throw new DataContractError();
  }

  return {
    page: value.page,
    page_size: value.page_size,
    total: value.total,
    items: value.items.map(parseItem),
  };
}

function parseUserLogItem(value: unknown): UserLogItem {
  if (!isRecord(value)) {
    throw new DataContractError();
  }

  const allowedKeys = new Set([
    'timestamp',
    'request_id',
    'model',
    'status',
    'latency',
    'prompt_tokens',
    'completion_tokens',
    'total_tokens',
    'billed_amount',
    'error_code',
  ]);
  if (Object.keys(value).some((key) => !allowedKeys.has(key))) {
    throw new DataContractError();
  }
  if (
    !nonNegativeInteger(value.timestamp) ||
    typeof value.request_id !== 'string' ||
    typeof value.model !== 'string' ||
    !['success', 'error', 'info'].includes(String(value.status)) ||
    !nonNegativeInteger(value.latency) ||
    !nonNegativeInteger(value.prompt_tokens) ||
    !nonNegativeInteger(value.completion_tokens) ||
    !nonNegativeInteger(value.total_tokens) ||
    !finiteNumber(value.billed_amount) ||
    value.billed_amount < 0 ||
    (value.error_code !== undefined &&
      (typeof value.error_code !== 'string' ||
        value.error_code.length === 0 ||
        value.error_code.length > 64 ||
        !/^[A-Za-z0-9_.:-]+$/.test(value.error_code)))
  ) {
    throw new DataContractError();
  }

  return {
    timestamp: value.timestamp,
    request_id: value.request_id,
    model: value.model,
    status: value.status as UserLogItem['status'],
    latency: value.latency,
    prompt_tokens: value.prompt_tokens,
    completion_tokens: value.completion_tokens,
    total_tokens: value.total_tokens,
    billed_amount: value.billed_amount,
    ...(value.error_code === undefined ? {} : { error_code: value.error_code }),
  };
}

export function parseUserLogPage(value: unknown): PageEnvelope<UserLogItem> {
  return parsePageEnvelope(value, parseUserLogItem);
}

export function parseUserLogStat(value: unknown): UserLogStat {
  if (
    !isRecord(value) ||
    !nonNegativeInteger(value.rpm) ||
    !nonNegativeInteger(value.tpm) ||
    !finiteNumber(value.quota)
  ) {
    throw new DataContractError();
  }

  return {
    rpm: value.rpm,
    tpm: value.tpm,
  };
}

function parseUserToken(value: unknown): UserToken {
  if (!isRecord(value) || 'key' in value || 'key_hash' in value) {
    throw new DataContractError();
  }

  const allowIPs = value.allow_ips;
  if (
    !integer(value.id) ||
    value.id <= 0 ||
    !integer(value.status) ||
    !requiredString(value.name) ||
    !nonNegativeInteger(value.created_time) ||
    !nonNegativeInteger(value.accessed_time) ||
    !integer(value.expired_time) ||
    !nonNegativeInteger(value.remain_quota) ||
    typeof value.unlimited_quota !== 'boolean' ||
    typeof value.model_limits_enabled !== 'boolean' ||
    typeof value.model_limits !== 'string' ||
    (allowIPs !== null && allowIPs !== undefined && typeof allowIPs !== 'string') ||
    typeof value.key_prefix !== 'string' ||
    !value.key_prefix.startsWith('sk-zt-') ||
    !value.key_prefix.endsWith('...')
  ) {
    throw new DataContractError();
  }

  return {
    id: value.id,
    status: value.status,
    name: value.name,
    created_time: value.created_time,
    accessed_time: value.accessed_time,
    expired_time: value.expired_time,
    remain_quota: value.remain_quota,
    unlimited_quota: value.unlimited_quota,
    model_limits_enabled: value.model_limits_enabled,
    model_limits: value.model_limits,
    allow_ips: typeof allowIPs === 'string' ? allowIPs : '',
    key_prefix: value.key_prefix,
  };
}

export function parseUserTokenPage(value: unknown): PageEnvelope<UserToken> {
  return parsePageEnvelope(value, parseUserToken);
}

export function parseUserTokenResponse(value: unknown): UserToken {
  return parseUserToken(value);
}

export function parseCreatedToken(value: unknown): CreatedToken {
  if (
    !isRecord(value) ||
    !integer(value.id) ||
    value.id <= 0 ||
    !requiredString(value.key) ||
    !value.key.startsWith('sk-zt-') ||
    !requiredString(value.key_prefix) ||
    !value.key_prefix.startsWith('sk-zt-') ||
    value.key_prefix.endsWith('...')
  ) {
    throw new DataContractError();
  }

  return {
    id: value.id,
    key: value.key,
    key_prefix: value.key_prefix,
  };
}
