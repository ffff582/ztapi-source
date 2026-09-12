import { Copy, Search, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { apiClient } from '../../api/client';
import { UsageBillingNote } from '../../components/UsageBillingNote';
import { useLocale } from '../../i18n/locale';
import {
  parseUserModelCatalog,
  type UserModelCatalogItem,
  type UserModelEndpointType,
} from '../../api/contracts';

const endpointDetails: Record<
  UserModelEndpointType,
  { label: string; endpoint: string }
> = {
  openai: {
    label: 'OpenAI 兼容',
    endpoint: '/v1/chat/completions',
  },
  'openai-response': {
    label: 'OpenAI Responses',
    endpoint: '/v1/responses',
  },
  embeddings: {
    label: '文本向量 Embeddings',
    endpoint: 'POST /v1/embeddings',
  },
  anthropic: {
    label: 'Anthropic Messages',
    endpoint: '/v1/messages',
  },
  gemini: {
    label: 'Gemini GenerateContent',
    endpoint: '/v1beta/models/{model}:generateContent',
  },
  images: {
    label: '图片生成',
    endpoint: '/v1/images/generations',
  },
  'video-tasks': {
    label: '异步视频任务',
    endpoint: '/v1/video/generations',
  },
};

const billingDimensionDetails: Record<string, { label: string; unit: string }> = {
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

const billingDimensionOrder = [
  'input_tokens',
  'output_tokens',
  'cache_read',
  'cache_write',
  'cache_write_5m',
  'cache_write_1h',
  'image',
  'audio',
  'request',
];

type ModelCategory =
  | 'all'
  | 'openai'
  | 'claude'
  | 'gemini'
  | 'domestic'
  | 'embedding'
  | 'image'
  | 'video';

const modelCategories: Array<{ id: ModelCategory; label: string }> = [
  { id: 'all', label: '全部' },
  { id: 'openai', label: 'OpenAI' },
  { id: 'claude', label: 'Claude' },
  { id: 'gemini', label: 'Gemini' },
  { id: 'domestic', label: '国产模型' },
  { id: 'embedding', label: '向量模型' },
  { id: 'image', label: '图片模型' },
  { id: 'video', label: '视频模型' },
];

const mainstreamModelOrder = [
  'zt-gpt-5.6-sol',
  'zt-claude-sonnet-5',
  'zt-gemini-3.5-flash',
  'zt-gpt-5.6-luna',
  'zt-gpt-5.5',
  'zt-claude-opus-4.8',
  'zt-gemini-3.1-pro-preview',
  'zt-gpt-5.4',
  'zt-claude-sonnet-4.6',
  'zt-gemini-3-flash-preview',
  'zt-deepseek-v4-pro',
  'zt-qwen-3.8-max',
  'zt-kimi-k2.7-code',
  'zt-glm-5.2',
  'zt-glm-5.1',
  'zt-deepseek-v4-flash',
] as const;

const mainstreamModelRanks = new Map<string, number>(
  mainstreamModelOrder.map((modelName, index) => [modelName, index]),
);

const domesticFamilies = new Set(['deepseek', 'glm', 'kimi', 'moonshot', 'qwen']);

function matchesCategory(item: UserModelCatalogItem, category: ModelCategory) {
  const family = item.provider_family.toLocaleLowerCase();
  switch (category) {
    case 'all':
      return true;
    case 'openai':
      return family === 'openai';
    case 'claude':
      return family === 'anthropic' || family === 'claude';
    case 'gemini':
      return family === 'google' || family === 'gemini';
    case 'domestic':
      return domesticFamilies.has(family);
    case 'embedding':
    case 'image':
    case 'video':
      return item.modality === category;
  }
}

function compareModelPopularity(left: UserModelCatalogItem, right: UserModelCatalogItem) {
  const unranked = mainstreamModelOrder.length;
  const leftRank = mainstreamModelRanks.get(left.model_name) ?? unranked;
  const rightRank = mainstreamModelRanks.get(right.model_name) ?? unranked;
  if (leftRank !== rightRank) {
    return leftRank - rightRank;
  }
  const modalityRank = { text: 0, embedding: 1, image: 2, video: 3 };
  return (
    modalityRank[left.modality] - modalityRank[right.modality] ||
    left.model_name.localeCompare(right.model_name)
  );
}

function orderedBillingDimensions(dimensions: string[]) {
  return [...dimensions].sort((left, right) => {
    const leftIndex = billingDimensionOrder.indexOf(left);
    const rightIndex = billingDimensionOrder.indexOf(right);
    const leftRank = leftIndex === -1 ? Number.MAX_SAFE_INTEGER : leftIndex;
    const rightRank = rightIndex === -1 ? Number.MAX_SAFE_INTEGER : rightIndex;
    return leftRank - rightRank || left.localeCompare(right);
  });
}

function formatDecimal(value: string) {
  const [whole, fraction = ''] = value.split('.');
  const groupedWhole = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const trimmedFraction = fraction.replace(/0+$/, '');
  return trimmedFraction.length > 0
    ? `${groupedWhole}.${trimmedFraction}`
    : groupedWhole;
}

function billingDimensionDetail(dimension: string) {
  return (
    billingDimensionDetails[dimension] ?? {
      label: dimension,
      unit: '计费单位',
    }
  );
}

function formatPrice(value: string, unit: string) {
  return `$${formatDecimal(value)} / ${unit}`;
}

const conditionLabels: Record<string, Record<string, string>> = {
  token_bucket: {
    text_input: '文本输入', text_cached_input: '文本缓存输入', image_input: '图片输入',
    image_cached_input: '图片缓存输入', image_output: '图片输出',
  },
  contains_video_input: { true: '含视频输入', false: '无视频输入' },
};

function mediaBillingUnit(unit: string) {
  return unit === 'usd_per_million_tokens' ? '1M tokens' : unit;
}

function pricingRuleLabel(conditions: Record<string, string>, t: (key: string) => string) {
  return Object.entries(conditions)
    .map(([key, value]) => t(conditionLabels[key]?.[value] ?? (key === 'resolution' ? value : `${key}: ${value}`)))
    .join(' · ');
}

function supportedOptionsLabel(item: UserModelCatalogItem, t: (key: string, values?: Record<string, string | number>) => string) {
  const options = item.supported_options;
  if (!options) return '';
  if (item.modality === 'image') {
    return [
      options.sizes?.join(' / '), options.qualities?.join(' / '),
      options.response_formats?.join(' / '),
      options.min_count && options.max_count ? t('{{min}}-{{max}} 张', { min: options.min_count, max: options.max_count }) : '',
    ].filter(Boolean).join(' · ');
  }
  return [
    options.resolutions?.join(' / '),
    options.duration_seconds?.map((duration) => t('{{count}} 秒', { count: duration })).join(' / '),
    options.supports_video_input ? t('支持视频输入') : t('无视频输入'),
  ].filter(Boolean).join(' · ');
}

function matchesSearch(item: UserModelCatalogItem, query: string, t: (key: string) => string) {
  const normalized = query.trim().toLocaleLowerCase();
  if (normalized.length === 0) {
    return true;
  }
  return [
    item.model_name,
    item.provider_name,
    ...item.supported_endpoint_types.flatMap((endpoint) => [endpointDetails[endpoint].label, t(endpointDetails[endpoint].label)]),
  ].some((value) => value.toLocaleLowerCase().includes(normalized));
}

export function SupportedModelsPage() {
  const { t } = useLocale();
  const [catalog, setCatalog] = useState<UserModelCatalogItem[]>([]);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<ModelCategory>('all');
  const [copyStatus, setCopyStatus] = useState<{
    modelName: string;
    state: 'success' | 'error';
  } | null>(null);
  const copyAttempt = useRef(0);

  useEffect(() => {
    let active = true;
    void apiClient
      .getResponse<unknown>('/user/models')
      .then(parseUserModelCatalog)
      .then((response) => {
        if (active) {
          setCatalog(
            [...response.catalog].sort(compareModelPopularity),
          );
          setStatus('ready');
        }
      })
      .catch(() => {
        if (active) {
          setStatus('error');
        }
      });

    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    copyAttempt.current += 1;
    setCopyStatus(null);
  }, [category, query]);

  const availableCategories = useMemo(
    () =>
      modelCategories.filter(
        ({ id }) => id === 'all' || catalog.some((item) => matchesCategory(item, id)),
      ),
    [catalog],
  );
  const visibleModels = useMemo(
    () =>
      catalog.filter(
        (item) =>
          matchesCategory(item, category) &&
          matchesSearch(item, query, t),
      ),
    [catalog, category, query, t],
  );

  async function copyModelID(modelName: string) {
    const attempt = ++copyAttempt.current;
    setCopyStatus(null);
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable');
      }
      await navigator.clipboard.writeText(modelName);
      if (attempt === copyAttempt.current) {
        setCopyStatus({ modelName, state: 'success' });
      }
    } catch {
      if (attempt === copyAttempt.current) {
        setCopyStatus({ modelName, state: 'error' });
      }
    }
  }

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{t('服务目录')}</p>
          <h1>{t('模型支持')}</h1>
        </div>
        <p>{t('仅展示当前账户分组实际可调用的公开模型与实时售价。')}</p>
      </header>

      <section className="console-section" aria-labelledby="supported-models-heading">
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">{t('当前账户')}</p>
            <h2 id="supported-models-heading">{t('可调用模型')}</h2>
          </div>
          {status === 'ready' && <span>{t('{{count}} 个可用模型', { count: catalog.length })}</span>}
        </div>

        <div className="model-support-billing-note">
          <UsageBillingNote />
        </div>

        {status === 'loading' && (
          <div className="console-state" aria-live="polite" aria-busy="true">
            {t('正在加载模型目录...')}
          </div>
        )}
        {status === 'error' && (
          <div className="console-state console-state--error" role="alert">
            <TriangleAlert aria-hidden="true" size={19} />
            {t('模型目录加载失败，请刷新后重试。')}
          </div>
        )}
        {status === 'ready' && catalog.length === 0 && (
          <div className="console-state">{t('当前账户暂无可调用模型。')}</div>
        )}
        {status === 'ready' && catalog.length > 0 && (
          <>
            <div className="model-support-toolbar">
              <div aria-label={t('模型分类')} className="model-support-categories" role="tablist">
                {availableCategories.map(({ id, label }) => (
                  <button
                    aria-selected={category === id}
                    className={category === id ? 'is-active' : undefined}
                    key={id}
                    role="tab"
                    type="button"
                    onClick={() => setCategory(id)}
                  >
                    {t(label)}
                  </button>
                ))}
              </div>
              <label className="model-support-search">
                <Search aria-hidden="true" size={17} />
                <input
                  aria-label={t('搜索模型')}
                  placeholder={t('搜索模型 ID 或厂商')}
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>
              <span className="model-support-result-count">
                {t('显示 {{visible}} / {{total}}', { visible: visibleModels.length, total: catalog.length })}
              </span>
              <span className="model-support-copy-status" role="status" aria-live="polite">
                {copyStatus?.state === 'success' && t('{{name}} 已复制', { name: copyStatus.modelName })}
                {copyStatus?.state === 'error' &&
                  t('{{name}} 复制失败，请手动复制', { name: copyStatus.modelName })}
              </span>
            </div>

            {visibleModels.length === 0 ? (
              <div className="console-state">{t('没有符合筛选条件的模型。')}</div>
            ) : (
              <div className="console-table-wrap model-support-table-wrap">
                <table className="console-table model-support-table" role="table">
                  <thead role="rowgroup">
                    <tr role="row">
                      <th role="columnheader" scope="col">{t('模型 ID')}</th>
                      <th role="columnheader" scope="col">{t('厂商')}</th>
                      <th role="columnheader" scope="col">{t('接口协议')}</th>
                      <th role="columnheader" scope="col">{t('调用地址')}</th>
                      <th role="columnheader" scope="col">{t('售价明细')}</th>
                      <th role="columnheader" scope="col">{t('操作')}</th>
                    </tr>
                  </thead>
                  <tbody role="rowgroup">
                    {visibleModels.map((item) => {
                      const details = item.supported_endpoint_types.map((endpoint) => endpointDetails[endpoint]);
                      const media = item.modality === 'image' || item.modality === 'video';
                      return (
                        <tr key={item.model_name} role="row">
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('模型 ID')}</span>
                            <code>{item.model_name}</code>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('厂商')}</span>
                            <span className="model-provider">{item.provider_name}</span>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('接口协议')}</span>
                            <span>{details.map((detail) => t(detail.label)).join(' / ')}</span>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('调用地址')}</span>
                            <div>
                              {details.map((detail) => (
                                <div key={detail.endpoint}><code>{detail.endpoint}</code></div>
                              ))}
                            </div>
                          </td>
                          <td className="model-price-cell" role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('售价明细')}</span>
                            {media ? (
                              <div className="model-media-pricing">
                                <small>{supportedOptionsLabel(item, t)}</small>
                                <ul aria-label={t('{{name}} 条件售价', { name: item.model_name })} className="model-price-list">
                                  {item.pricing_rules?.map((rule) => (
                                    <li key={rule.id}>
                                      <span>{pricingRuleLabel(rule.conditions, t)}</span>
                                      <strong>
                                        {Object.values(rule.sale_usd).map((value) =>
                                          formatPrice(value, mediaBillingUnit(rule.billing_unit)),
                                        ).join(' / ')}
                                      </strong>
                                    </li>
                                  ))}
                                </ul>
                              </div>
                            ) : (
                              <ul aria-label={t('{{name}} 售价', { name: item.model_name })} className="model-price-list">
                                {orderedBillingDimensions(item.billing_dimensions).map((dimension) => {
                                  const price = billingDimensionDetail(dimension);
                                  return (
                                    <li key={dimension}>
                                      <span>{t(price.label)}</span>
                                      <strong>{formatPrice(item.sale_usd[dimension], t(price.unit))}</strong>
                                    </li>
                                  );
                                })}
                              </ul>
                            )}
                          </td>
                          <td className="model-support-actions" role="cell">
                            <span aria-hidden="true" className="model-cell-label">{t('操作')}</span>
                            <button
                              aria-label={t('复制 {{name}}', { name: item.model_name })}
                              className="console-icon-action model-copy"
                              title={t('复制模型 ID')}
                              type="button"
                              onClick={() => void copyModelID(item.model_name)}
                            >
                              <Copy aria-hidden="true" size={16} />
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  );
}
