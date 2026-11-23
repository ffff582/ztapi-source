import { ChevronDown, Cpu, Search, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { apiClient } from '../../api/client';
import {
  getPublicModelFamily,
  parsePricingEnvelope,
  parseRuntimeStatus,
  type PricingEnvelope,
  type PricingModel,
  type DisplayFXReference,
} from '../../api/contracts';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { ResourceDisclosureNotice } from '../../components/ResourceDisclosureNotice';
import { useLocale } from '../../i18n/locale';
import { providerFamilyRank, sortVendorModelsByRecency } from './catalog-order';
import { modelOfficialDiscount, modelPriceDetails, type ModelPriceDetail } from './pricing';
import { OfficialPriceComparison, OfficialPriceReference } from './OfficialPriceReference';
import { PriceCurrencyControl, usePriceDisplay } from './PriceCurrencyControl';
import { ImageSalePricing } from './ImageSalePricing';

type ModelCategory = 'all' | 'text' | 'image' | 'video' | 'embedding';

const categories: { id: ModelCategory; label: string }[] = [
  { id: 'all', label: '全部模型' },
  { id: 'text', label: '文本模型' },
  { id: 'image', label: '图片生成' },
  { id: 'video', label: '视频生成' },
  { id: 'embedding', label: '向量模型' },
];

function modelCategory(model: PricingModel): ModelCategory {
  if (model.modality === 'image') return 'image';
  if (model.modality === 'video') return 'video';
  if (model.modality === 'embedding' || model.supported_endpoint_types?.includes('embeddings')) return 'embedding';
  return 'text';
}

function primaryPrices(prices: ModelPriceDetail[]) {
  const input = prices.find((price) => price.dimension === 'input_tokens') ?? prices[0];
  const output = prices.find((price) => price.dimension === 'output_tokens' && price.key !== input?.key)
    ?? prices.find((price) => price.key !== input?.key);
  return [input, output].filter((price): price is ModelPriceDetail => Boolean(price));
}

export function ModelsPage() {
  const { t } = useLocale();
  const [pricing, setPricing] = useState<PricingEnvelope | null>(null);
  const [quotaPerUnit, setQuotaPerUnit] = useState<number | null>(null);
  const [displayRate, setDisplayRate] = useState<number | undefined>();
  const [displayReference, setDisplayReference] = useState<DisplayFXReference>();
  const { display, setCurrency } = usePriceDisplay(displayRate, displayReference);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<ModelCategory>('all');

  useEffect(() => {
    let active = true;
    void Promise.all([
      apiClient.getResponse<unknown>('/pricing').then(parsePricingEnvelope),
      apiClient.get<unknown>('/status').then(parseRuntimeStatus),
    ])
      .then(([pricingValue, statusValue]) => {
        if (active) {
          setPricing(pricingValue);
          setQuotaPerUnit(statusValue.quota_per_unit);
          setDisplayRate(statusValue.usd_exchange_rate);
          setDisplayReference(statusValue.display_fx);
          setStatus('ready');
        }
      })
      .catch(() => {
        if (active) setStatus('error');
      });
    return () => { active = false; };
  }, []);

  const visibleModels = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return (pricing?.models ?? []).filter((model) =>
      (category === 'all' || modelCategory(model) === category) &&
      (!needle || [model.model_name, model.vendor_name, model.owner_by, model.description]
        .some((value) => value?.toLocaleLowerCase().includes(needle))));
  }, [pricing, category, query]);

  const categoryCounts = useMemo(() => {
    const counts = categories.reduce<Record<ModelCategory, number>>((result, item) => {
      result[item.id] = 0;
      return result;
    }, { all: 0, text: 0, image: 0, video: 0, embedding: 0 });
    for (const model of pricing?.models ?? []) {
      counts.all += 1;
      counts[modelCategory(model)] += 1;
    }
    return counts;
  }, [pricing]);

  const grouped = useMemo(() => {
    const result = new Map<string, { key: string; label: string; models: PricingModel[] }>();
    for (const model of visibleModels) {
      const legacyFamily = getPublicModelFamily(model.model_name, model.owner_by);
      const key = model.provider_family ?? legacyFamily ?? (model.owner_by || 'other');
      const label = model.vendor_name ?? legacyFamily ?? (model.owner_by || t('其他'));
      const group = result.get(key) ?? { key, label, models: [] };
      group.models.push(model);
      result.set(key, group);
    }
    return [...result.values()]
      .map((group) => ({ ...group, models: sortVendorModelsByRecency(group.models) }))
      .sort((left, right) => providerFamilyRank(left.key) - providerFamilyRank(right.key) || left.label.localeCompare(right.label));
  }, [visibleModels, t]);

  const publicModelCount = pricing?.models.length ?? 0;

  return (
    <div className="models-page">
      <PublicHeader />
      <main>
        <ResourceDisclosureNotice />
        <section className="models-masthead">
          <div className="public-shell models-masthead__inner">
            <p className="console-eyebrow">{t('实时公开目录')}</p>
            <h1>{t('模型与价格')}</h1>
            <p>{t('按模型查价格，按规格看明细。')}</p>
          </div>
        </section>

        <section className="models-catalog public-shell" aria-label={t('公开模型目录')}>
          <aside className="catalog-offer" aria-label={t('价格说明')}>
            <strong>{t('逐项查看实际售价与官网参考')}</strong>
            <span>{t('售价以当前公开目录为准；不同规格、阶梯和缓存分别计价，不承诺全站统一折扣。')}</span>
          </aside>
          <PriceCurrencyControl display={display} onChange={setCurrency} />

          <div className="catalog-tools">
            <div className="catalog-tools__heading">
              <div>
                <span className="console-eyebrow">{t('实时售价')}</span>
                <h2>{t('选模型，查价格')}</h2>
              </div>
              {status === 'ready' && <span>{t('显示 {{visible}} / {{total}}', { visible: visibleModels.length, total: publicModelCount })}</span>}
            </div>
            <div className="catalog-tools__controls">
              <div className="catalog-categories" aria-label={t('模型分类')}>
                {categories.map((item) => (
                  <button key={item.id} type="button" className={category === item.id ? 'is-active' : undefined}
                    aria-pressed={category === item.id} onClick={() => setCategory(item.id)}>
                    {t(item.label)}
                    {status === 'ready' && <span className="catalog-categories__count" aria-hidden="true">{categoryCounts[item.id]}</span>}
                  </button>
                ))}
              </div>
              <label className="catalog-search">
                <Search aria-hidden="true" size={17} />
                <input type="search" aria-label={t('搜索模型')} placeholder={t('搜索模型 ID 或厂商')}
                  value={query} onChange={(event) => setQuery(event.target.value)} />
              </label>
            </div>
          </div>

          {status === 'loading' && (
            <div className="catalog-state" aria-live="polite" aria-busy="true">
              <Cpu aria-hidden="true" size={22} />{t('正在加载模型价格...')}
            </div>
          )}
          {status === 'error' && (
            <div className="catalog-state catalog-state--error" role="alert">
              <TriangleAlert aria-hidden="true" size={22} />{t('模型价格加载失败，请稍后重试。')}
            </div>
          )}
          {status === 'ready' && publicModelCount === 0 && <div className="catalog-state">{t('当前没有可展示的公开模型。')}</div>}
          {status === 'ready' && publicModelCount > 0 && visibleModels.length === 0 &&
            <div className="catalog-state">{t('没有符合筛选条件的模型。')}</div>}

          {status === 'ready' && pricing !== null && quotaPerUnit !== null && grouped.map((group) => (
            <section className="catalog-family" key={group.key}>
              <div className="catalog-family__heading">
                <h2>{group.label}</h2>
                <span>{t('{{count}} 个模型', { count: group.models.length })}</span>
              </div>
              <div className="catalog-models">
                {group.models.map((model) => {
                  const prices = modelPriceDetails(model, pricing, quotaPerUnit, display);
                  const headlinePrices = model.image_sale ? (model.image_sale.version === 2 ? prices : [prices[0], prices[2]]) : primaryPrices(prices);
                  const quoted = model.official_reference?.kind === 'supplier_quoted_official';
                  const discount = quoted ? null : modelOfficialDiscount(prices);
                  const referenceLabel = quoted ? '报价表官网基准' : '官网参考价';
                  return (
                    <article className={`catalog-model${model.image_sale ? ' catalog-model--image-sale' : ''}`} key={model.model_name} aria-label={model.model_name}>
                      <details>
                        <summary>
                          <span className="catalog-model__identity">
                            <code>{model.model_name}</code>
                            <span>{t(categories.find((item) => item.id === modelCategory(model))?.label ?? '文本模型')}</span>
                          </span>
                          <span className="catalog-model__preview">
                            {headlinePrices.map((price) => (
                              <span className="catalog-model__metric" key={price.key}>
                                <small>{t(price.label)}</small><strong>{t(price.price)}</strong>
                              </span>
                            ))}
                          </span>
                          <span className="catalog-model__end">
                            {discount && <span className="catalog-model__discount">{t('官方 {{discount}} 折', { discount })}</span>}
                            <span className="catalog-model__more">{t('查看完整价格')}<ChevronDown aria-hidden="true" size={16} /></span>
                          </span>
                        </summary>
                        <div className="catalog-model__detail">
                          {model.description && <p>{model.description}</p>}
                          {model.image_sale ? (
                            <ImageSalePricing name={model.model_name} sale={model.image_sale} display={display} />
                          ) : <>
                          <div className="catalog-model__price-head"><span>{t('规格')}</span><span>{t('我们的价格')}</span><span>{t(referenceLabel)}</span></div>
                          <ul className="catalog-model__price-list" aria-label={t('{{name}} 售价', { name: model.model_name })}>
                            {prices.map((price) => (
                              <li key={price.key}>
                                <span>{t(price.label)}</span>
                                <span className="catalog-model__sale"><small>{t('我们的价格')}</small><strong>{t(price.price)}</strong></span>
                                <span className="catalog-model__official">
                                  <small>{t(referenceLabel)}</small>{price.officialPrice ? t(price.officialPrice) : t('暂无官网参考')}
                                  <OfficialPriceComparison price={price} quoted={quoted} />
                                </span>
                              </li>
                            ))}
                          </ul>
                          <OfficialPriceReference reference={model.official_reference} />
                          <small>{t('仅比较同模型、同规格的已提供参考价；参考价不是来源认证，也不保证为官网现价。')}</small>
                          </>}
                        </div>
                      </details>
                    </article>
                  );
                })}
              </div>
            </section>
          ))}
        </section>
      </main>
    </div>
  );
}
