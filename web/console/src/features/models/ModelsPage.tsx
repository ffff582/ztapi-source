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
import { modelPriceDetails } from './pricing';

export function ModelsPage() {
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
      const label = model.vendor_name ?? legacyFamily ?? (model.owner_by || '其他');
      const group = result.get(key) ?? { key, label, models: [] };
      group.models.push(model);
      result.set(key, group);
    }
    for (const group of result.values()) {
      group.models.sort((a, b) => a.model_name.localeCompare(b.model_name));
    }
    return [...result.values()];
  }, [pricing]);

  const publicModelCount = pricing?.models.length ?? 0;

  return (
    <div className="models-page">
      <PublicHeader />
      <main>
        <section className="models-masthead">
          <div className="public-shell models-masthead__inner">
            <p className="console-eyebrow">实时公开目录</p>
            <h1>模型与价格</h1>
            <p>公开模型与实时售价</p>
          </div>
        </section>
        <section className="models-catalog public-shell" aria-label="公开模型目录">
          {status === 'ready' && <p>{publicModelCount} 个公开模型</p>}
          {status === 'loading' && (
            <div className="catalog-state" aria-live="polite" aria-busy="true">
              <Cpu aria-hidden="true" size={22} />
              正在加载模型价格...
            </div>
          )}
          {status === 'error' && (
            <div className="catalog-state catalog-state--error" role="alert">
              <TriangleAlert aria-hidden="true" size={22} />
              模型价格加载失败，请稍后重试。
            </div>
          )}
          {status === 'ready' && publicModelCount === 0 && (
            <div className="catalog-state">当前没有可展示的公开模型。</div>
          )}
          {status === 'ready' &&
            pricing !== null &&
            quotaPerUnit !== null &&
            grouped.map((group) => {
              return (
                <section className="catalog-family" key={group.key}>
                  <div className="catalog-family__heading">
                    <h2>{group.label}</h2>
                    <span>{group.models.length} 个模型</span>
                  </div>
                  <div className="console-table-wrap" role="region" aria-label={`${group.label} 模型价格表格`} tabIndex={0}>
                    <table className="console-table catalog-table">
                      <thead>
                        <tr>
                          <th scope="col">公开模型</th>
                          <th scope="col">说明</th>
                          <th scope="col">售价明细</th>
                        </tr>
                      </thead>
                      <tbody>
                        {group.models.map((model) => {
                          const prices = modelPriceDetails(
                            model,
                            pricing,
                            quotaPerUnit,
                          );
                          return (
                            <tr key={model.model_name}>
                              <td>
                                <code>{model.model_name}</code>
                              </td>
                              <td>{model.description || '—'}</td>
                              <td className="model-price-cell">
                                <ul className="model-price-list" aria-label={`${model.model_name} 售价`}>
                                  {prices.map((price) => (
                                    <li key={price.dimension}>
                                      <span>{price.label}</span>
                                      <strong>{price.price}</strong>
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
