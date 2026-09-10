import { Copy, Search, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { apiClient } from '../../api/client';
import { UsageBillingNote } from '../../components/UsageBillingNote';
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

function pricingRuleLabel(conditions: Record<string, string>) {
  return Object.entries(conditions)
    .map(([key, value]) => conditionLabels[key]?.[value] ?? (key === 'resolution' ? value : `${key}: ${value}`))
    .join(' · ');
}

function supportedOptionsLabel(item: UserModelCatalogItem) {
  const options = item.supported_options;
  if (!options) return '';
  if (item.modality === 'image') {
    return [
      options.sizes?.join(' / '), options.qualities?.join(' / '),
      options.response_formats?.join(' / '),
      options.min_count && options.max_count ? `${options.min_count}-${options.max_count} 张` : '',
    ].filter(Boolean).join(' · ');
  }
  return [
    options.resolutions?.join(' / '),
    options.duration_seconds?.map((duration) => `${duration} 秒`).join(' / '),
    options.supports_video_input ? '支持视频输入' : '无视频输入',
  ].filter(Boolean).join(' · ');
}

function matchesSearch(item: UserModelCatalogItem, query: string) {
  const normalized = query.trim().toLocaleLowerCase();
  if (normalized.length === 0) {
    return true;
  }
  return [
    item.model_name,
    item.provider_name,
    ...item.supported_endpoint_types.map((endpoint) => endpointDetails[endpoint].label),
  ].some((value) => value.toLocaleLowerCase().includes(normalized));
}

export function SupportedModelsPage() {
  const [catalog, setCatalog] = useState<UserModelCatalogItem[]>([]);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [query, setQuery] = useState('');
  const [provider, setProvider] = useState('all');
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
            [...response.catalog].sort((a, b) =>
              a.model_name.localeCompare(b.model_name),
            ),
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
  }, [provider, query]);

  const providers = useMemo(
    () =>
      [...new Set(catalog.map((item) => item.provider_name))].sort((a, b) =>
        a.localeCompare(b),
      ),
    [catalog],
  );
  const visibleModels = useMemo(
    () =>
      catalog.filter(
        (item) =>
          (provider === 'all' || item.provider_name === provider) &&
          matchesSearch(item, query),
      ),
    [catalog, provider, query],
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
          <p className="console-eyebrow">服务目录</p>
          <h1>模型支持</h1>
        </div>
        <p>仅展示当前账户分组实际可调用的公开模型与实时售价。</p>
      </header>

      <section className="console-section" aria-labelledby="supported-models-heading">
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">当前账户</p>
            <h2 id="supported-models-heading">可调用模型</h2>
          </div>
          {status === 'ready' && <span>{catalog.length} 个可用模型</span>}
        </div>

        <div className="model-support-billing-note">
          <UsageBillingNote />
        </div>

        {status === 'loading' && (
          <div className="console-state" aria-live="polite" aria-busy="true">
            正在加载模型目录...
          </div>
        )}
        {status === 'error' && (
          <div className="console-state console-state--error" role="alert">
            <TriangleAlert aria-hidden="true" size={19} />
            模型目录加载失败，请刷新后重试。
          </div>
        )}
        {status === 'ready' && catalog.length === 0 && (
          <div className="console-state">当前账户暂无可调用模型。</div>
        )}
        {status === 'ready' && catalog.length > 0 && (
          <>
            <div className="model-support-toolbar">
              <label className="model-support-search">
                <Search aria-hidden="true" size={17} />
                <input
                  aria-label="搜索模型"
                  placeholder="搜索模型 ID 或厂商"
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>
              <label className="model-support-provider">
                <span>厂商</span>
                <select
                  aria-label="厂商筛选"
                  value={provider}
                  onChange={(event) => setProvider(event.target.value)}
                >
                  <option value="all">全部厂商</option>
                  {providers.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
              </label>
              <span className="model-support-result-count">
                显示 {visibleModels.length} / {catalog.length}
              </span>
              <span className="model-support-copy-status" role="status" aria-live="polite">
                {copyStatus?.state === 'success' && `${copyStatus.modelName} 已复制`}
                {copyStatus?.state === 'error' &&
                  `${copyStatus.modelName} 复制失败，请手动复制`}
              </span>
            </div>

            {visibleModels.length === 0 ? (
              <div className="console-state">没有符合筛选条件的模型。</div>
            ) : (
              <div className="console-table-wrap model-support-table-wrap">
                <table className="console-table model-support-table" role="table">
                  <thead role="rowgroup">
                    <tr role="row">
                      <th role="columnheader" scope="col">模型 ID</th>
                      <th role="columnheader" scope="col">厂商</th>
                      <th role="columnheader" scope="col">接口协议</th>
                      <th role="columnheader" scope="col">调用地址</th>
                      <th role="columnheader" scope="col">售价明细</th>
                      <th role="columnheader" scope="col">操作</th>
                    </tr>
                  </thead>
                  <tbody role="rowgroup">
                    {visibleModels.map((item) => {
                      const details = item.supported_endpoint_types.map((endpoint) => endpointDetails[endpoint]);
                      const media = item.modality === 'image' || item.modality === 'video';
                      return (
                        <tr key={item.model_name} role="row">
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">模型 ID</span>
                            <code>{item.model_name}</code>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">厂商</span>
                            <span className="model-provider">{item.provider_name}</span>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">接口协议</span>
                            <span>{details.map((detail) => detail.label).join(' / ')}</span>
                          </td>
                          <td role="cell">
                            <span aria-hidden="true" className="model-cell-label">调用地址</span>
                            <div>
                              {details.map((detail) => (
                                <div key={detail.endpoint}><code>{detail.endpoint}</code></div>
                              ))}
                            </div>
                          </td>
                          <td className="model-price-cell" role="cell">
                            <span aria-hidden="true" className="model-cell-label">售价明细</span>
                            {media ? (
                              <div className="model-media-pricing">
                                <small>{supportedOptionsLabel(item)}</small>
                                <ul aria-label={`${item.model_name} 条件售价`} className="model-price-list">
                                  {item.pricing_rules?.map((rule) => (
                                    <li key={rule.id}>
                                      <span>{pricingRuleLabel(rule.conditions)}</span>
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
                              <ul aria-label={`${item.model_name} 售价`} className="model-price-list">
                                {orderedBillingDimensions(item.billing_dimensions).map((dimension) => {
                                  const price = billingDimensionDetail(dimension);
                                  return (
                                    <li key={dimension}>
                                      <span>{price.label}</span>
                                      <strong>{formatPrice(item.sale_usd[dimension], price.unit)}</strong>
                                    </li>
                                  );
                                })}
                              </ul>
                            )}
                          </td>
                          <td className="model-support-actions" role="cell">
                            <span aria-hidden="true" className="model-cell-label">操作</span>
                            <button
                              aria-label={`复制 ${item.model_name}`}
                              className="console-icon-action model-copy"
                              title="复制模型 ID"
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
