import { ArrowUpRight, Cpu, TriangleAlert } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { apiClient } from '../../api/client';
import {
  getPublicModelFamily,
  parsePricingEnvelope,
  parseRuntimeStatus,
  type PricingEnvelope,
  type PricingModel,
  type PublicModelFamily,
} from '../../api/contracts';
import { modelPrices } from '../models/pricing';
import { useLocale } from '../../i18n/locale';

const families: PublicModelFamily[] = ['OpenAI', 'Claude', 'Gemini'];
const preferredModels: Record<PublicModelFamily, string> = {
  OpenAI: 'zt-gpt-5.6-sol',
  Claude: 'zt-claude-sonnet-5',
  Gemini: 'zt-gemini-3.5-flash',
};
const capabilities = ['文本模型', '图片生成', '视频生成'] as const;

function chooseFeaturedModels(models: PricingModel[]) {
  return families.flatMap((family) => {
    const familyModels = models
      .filter((model) => getPublicModelFamily(model.model_name, model.owner_by, model.provider_family) === family)
      .sort((a, b) => a.model_name.localeCompare(b.model_name));
    const preferred = familyModels.find((model) => model.model_name === preferredModels[family]);
    return preferred ? [preferred] : familyModels.slice(0, 1);
  });
}

function managedPrice(value: string) {
  const [whole, fraction = ''] = value.split('.');
  const trimmed = fraction.replace(/0+$/, '');
  return `$${whole}${trimmed ? `.${trimmed}` : ''} / 1M tokens`;
}

export function PublicModelProof() {
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
        if (!active) return;
        setPricing(pricingValue);
        setQuotaPerUnit(statusValue.quota_per_unit);
        setStatus('ready');
      })
      .catch(() => {
        if (active) setStatus('error');
      });

    return () => {
      active = false;
    };
  }, []);

  const publicModels = useMemo(
    () => pricing?.models ?? [],
    [pricing],
  );
  const featuredModels = useMemo(
    () => chooseFeaturedModels(publicModels),
    [publicModels],
  );

  return (
    <section className="model-proof" aria-labelledby="model-proof-title">
      <div className="public-shell">
        <div className="model-proof__heading">
          <div>
            <p className="section-kicker">{t('实时模型目录')}</p>
            <h2 id="model-proof-title">{t('文本、图片与视频，通过一个账户统一调用')}</h2>
          </div>
          <Link to="/models">
            {t('查看全部模型与价格')}
            <ArrowUpRight aria-hidden="true" size={18} />
          </Link>
        </div>

        <div className="model-proof__families" aria-label={t('支持的能力类型')}>
          {capabilities.map((capability) => (
            <span key={capability}>{t(capability)}</span>
          ))}
        </div>

        {status === 'loading' && (
          <>
            <div className="model-proof__state" aria-live="polite" aria-busy="true">
              <Cpu aria-hidden="true" size={20} />
              {t('正在读取实时模型...')}
            </div>
            <div className="model-proof__grid model-proof__grid--loading" aria-hidden="true">
              {families.map((family) => (
                <article key={family}>
                  <span className="model-proof__skeleton model-proof__skeleton--label" />
                  <span className="model-proof__skeleton model-proof__skeleton--title" />
                  <span className="model-proof__skeleton model-proof__skeleton--body" />
                  <span className="model-proof__skeleton model-proof__skeleton--price" />
                </article>
              ))}
            </div>
          </>
        )}
        {status === 'error' && (
          <div className="model-proof__state model-proof__state--error" role="alert">
            <TriangleAlert aria-hidden="true" size={20} />
            {t('暂时无法读取实时模型，请稍后查看模型价格页。')}
          </div>
        )}
        {status === 'ready' && publicModels.length === 0 && (
          <div className="model-proof__state model-proof__state--empty">
            {t('模型目录正在配置，开放后将在这里展示实时价格。')}
          </div>
        )}
        {status === 'ready' && pricing !== null && quotaPerUnit !== null && publicModels.length > 0 && (
          <>
            <p className="model-proof__count">{t('{{count}} 个实时公开模型', { count: publicModels.length })}</p>
            <div className="model-proof__grid">
              {featuredModels.map((model) => {
                const family = model.vendor_name ?? getPublicModelFamily(model.model_name, model.owner_by);
                const media = model.modality === 'image' || model.modality === 'video';
                const prices = !media && model.sale_usd
                  ? { input: managedPrice(model.sale_usd.input_tokens), output: model.sale_usd.output_tokens ? managedPrice(model.sale_usd.output_tokens) : null }
                  : !media ? modelPrices(model, pricing, quotaPerUnit) : null;
                return (
                  <article key={model.model_name}>
                    <div className="model-proof__card-top">
                      <span>{family}</span>
                      <code>{model.model_name}</code>
                    </div>
                    <p>{model.description || (model.modality === 'image' ? t('当前可用的图片生成模型') : model.modality === 'video' ? t('当前可用的视频生成模型') : t('当前可用的公开模型'))}</p>
                    <dl>
                      {media ? (
                        <div>
                          <dt>{t('价格')}</dt>
                          <dd>{t('按规格计费')}</dd>
                        </div>
                      ) : (
                        <>
                          <div>
                            <dt>{t('输入')}</dt>
                            <dd>{t(prices?.input ?? '')}</dd>
                          </div>
                          {prices?.output !== null && <div>
                            <dt>{t('输出')}</dt>
                            <dd>{t(prices?.output ?? '')}</dd>
                          </div>}
                        </>
                      )}
                    </dl>
                  </article>
                );
              })}
            </div>
          </>
        )}
      </div>
    </section>
  );
}
