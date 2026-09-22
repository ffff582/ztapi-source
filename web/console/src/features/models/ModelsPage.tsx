import { Cpu, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { apiClient } from '../../api/client';
import {
  getPublicModelFamily,
  parsePricingEnvelope,
  parseRuntimeStatus,
  type PricingEnvelope,
  type PricingModel,
} from '../../api/contracts';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { useLocale } from '../../i18n/locale';
import { providerFamilyRank, sortVendorModelsByRecency } from './catalog-order';
import { modelPriceDetails } from './pricing';

export function ModelsPage() {
  const { t } = useLocale();
  const [pricing, setPricing] = useState<PricingEnvelope | null>(null);
  const [quotaPerUnit, setQuotaPerUnit] = useState<number | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');

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

  const grouped = useMemo(() => {
    const result = new Map<string, { key: string; label: string; models: PricingModel[] }>();
    for (const model of pricing?.models ?? []) {
      const legacyFamily = getPublicModelFamily(model.model_name, model.owner_by);
      const key = model.provider_family ?? legacyFamily ?? (model.owner_by || 'other');
      const label = model.vendor_name ?? legacyFamily ?? (model.owner_by || t('其他'));
      const group = result.get(key) ?? { key, label, models: [] };
      group.models.push(model);
      result.set(key, group);
    }
    // Each vendor leads with what it currently sells, and the vendors keep the
    // same order on every visit rather than the order the API happened to
    // return them in.
    return [...result.values()]
      .map((group) => ({ ...group, models: sortVendorModelsByRecency(group.models) }))
      .sort(
        (left, right) =>
          providerFamilyRank(left.key) - providerFamilyRank(right.key) ||
          left.label.localeCompare(right.label),
      );
  }, [pricing, t]);

  const publicModelCount = pricing?.models.length ?? 0;

  return (
    <div className="models-page">
      <PublicHeader />
      <main>
        <section className="models-masthead">
          <div className="public-shell models-masthead__inner">
            <p className="console-eyebrow">{t('实时公开目录')}</p>
            <h1>{t('模型与价格')}</h1>
            <p>{t('公开模型与实时售价')}</p>
          </div>
        </section>
        <section className="models-catalog public-shell" aria-label={t('公开模型目录')}>
          <aside className="catalog-offer" aria-label={t('综合优惠约 20%')}>
            <strong>{t('综合优惠约 20%')}</strong>
            <span>{t('模型价格对比官方更优惠，充值再额外赠送 5% 使用额度。')}</span>
          </aside>
          {status === 'ready' && <p>{t('{{count}} 个公开模型', { count: publicModelCount })}</p>}
          {status === 'loading' && (
            <div className="catalog-state" aria-live="polite" aria-busy="true">
              <Cpu aria-hidden="true" size={22} />
              {t('正在加载模型价格...')}
            </div>
          )}
          {status === 'error' && (
            <div className="catalog-state catalog-state--error" role="alert">
              <TriangleAlert aria-hidden="true" size={22} />
              {t('模型价格加载失败，请稍后重试。')}
            </div>
          )}
          {status === 'ready' && publicModelCount === 0 && (
            <div className="catalog-state">{t('当前没有可展示的公开模型。')}</div>
          )}
          {status === 'ready' &&
            pricing !== null &&
            quotaPerUnit !== null &&
            grouped.map((group) => {
              return (
                <section className="catalog-family" key={group.key}>
                  <div className="catalog-family__heading">
                    <h2>{group.label}</h2>
                    <span>{t('{{count}} 个模型', { count: group.models.length })}</span>
                  </div>
                  <div className="console-table-wrap" role="region" aria-label={t('{{name}} 模型价格表格', { name: group.label })} tabIndex={0}>
                    <table className="console-table catalog-table">
                      <thead>
                        <tr>
                          <th scope="col">{t('公开模型')}</th>
                          <th scope="col">{t('说明')}</th>
                          <th scope="col">{t('价格对比')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {group.models.map((model) => {
                          const prices = modelPriceDetails(
                            model,
                            pricing,
                            quotaPerUnit,
                          );
                          const hasComparison = prices.some((price) => price.officialPrice !== undefined);
                          return (
                            <tr key={model.model_name}>
                              <td>
                                <code>{model.model_name}</code>
                              </td>
                              <td>{model.description || '—'}</td>
                              <td className="model-price-cell">
                                {hasComparison && (
                                  <div className="catalog-price-columns" aria-hidden="true">
                                    <span>{t('规格')}</span>
                                    <span>{t('我们的价格')}</span>
                                    <span>{t('官方价格')}</span>
                                    <span>{t('节省')}</span>
                                  </div>
                                )}
                                <ul className={`model-price-list catalog-price-list${hasComparison ? '' : ' catalog-price-list--simple'}`} aria-label={t('{{name}} 售价', { name: model.model_name })}>
                                  {prices.map((price) => (
                                    <li key={price.key}>
                                      <span>{t(price.label)}</span>
                                      <strong>{t(price.price)}</strong>
                                      {hasComparison && <del>{price.officialPrice ? t(price.officialPrice) : '—'}</del>}
                                      {hasComparison && (
                                        price.savingsPercent === undefined
                                          ? <span className="catalog-price-missing">—</span>
                                          : <em>{t('节省 {{percent}}%', { percent: price.savingsPercent })}</em>
                                      )}
                                    </li>
                                  ))}
                                </ul>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                </section>
              );
            })}
        </section>
      </main>
    </div>
  );
}
