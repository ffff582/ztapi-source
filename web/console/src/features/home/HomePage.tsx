import { useEffect, useState } from 'react';
import { ArrowRight, CircleDollarSign, ListTree, Route } from 'lucide-react';
import { Link, useLocation } from 'react-router-dom';
import '../../brand/tokens.css';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { GatewayMotionVisual } from './GatewayMotionVisual';
import { GatewayStatusRail } from './GatewayStatusRail';
import { IntegrationWorkbench } from './IntegrationWorkbench';
import { PublicModelProof } from './PublicModelProof';
import type { ModelFamily } from './homeContent';
import { useLocale } from '../../i18n/locale';
import './home.css';

export function HomePage() {
  const { t } = useLocale();
  const [selectedFamily, setSelectedFamily] = useState<ModelFamily>('openai');
  const { hash } = useLocation();
  const sourceUrl =
    import.meta.env.VITE_ZTAPI_SOURCE_URL || '/.well-known/source';

  useEffect(() => {
    if (!hash) {
      return;
    }

    const target = document.getElementById(hash.slice(1));
    target?.scrollIntoView({ block: 'start' });
  }, [hash]);

  return (
    <div className="public-home">
      <PublicHeader />
      <main>
        <section className="gateway-hero" aria-labelledby="gateway-hero-title">
          <div className="gateway-hero__visual" aria-hidden="true">
            <GatewayMotionVisual />
          </div>
          <div className="gateway-hero__content public-shell">
            <p className="gateway-hero__eyebrow">UNIFIED MODEL API</p>
            <h1 id="gateway-hero-title">ZTAPI</h1>
            <p className="gateway-hero__title">
              <span>{t('一个 Key，连接全球主流')}</span>{' '}
              <span className="gateway-hero__title-models">{t('AI 模型')}</span>
            </p>
            <p className="gateway-hero__summary">
              {t('兼容 OpenAI SDK，统一管理调用、用量和模型路由。')}
            </p>
            <div className="gateway-hero__actions">
              <Link className="button-link button-link--primary" to="/register">
                {t('开始使用')}
                <ArrowRight aria-hidden="true" size={18} />
              </Link>
              <Link className="button-link button-link--secondary" to="/models">
                {t('查看模型价格')}
              </Link>
            </div>
            <div className="gateway-hero__endpoint">
              <span>API BASE URL</span>
              <code>https://ztapi.vip/v1</code>
            </div>
          </div>
        </section>
        <GatewayStatusRail />
        <PublicModelProof />
        <IntegrationWorkbench
          selectedFamily={selectedFamily}
          onSelectFamily={setSelectedFamily}
        />
        <section className="principles-band" aria-labelledby="principles-title">
          <div className="public-shell">
            <div className="principles-band__heading">
              <p className="section-kicker">{t('网关能力')}</p>
              <h2 id="principles-title">{t('每一次调用都清晰、可控、可追踪')}</h2>
            </div>
            <div className="principles-grid">
              <article>
                <CircleDollarSign aria-hidden="true" />
                <h3>{t('透明用量')}</h3>
                <p>{t('按实际 API 用量记录调用与费用归属。')}</p>
              </article>
              <article>
                <Route aria-hidden="true" />
                <h3>{t('可控路由')}</h3>
                <p>{t('通过统一模型名称管理可用路由。')}</p>
              </article>
              <article>
                <ListTree aria-hidden="true" />
                <h3>{t('请求记录')}</h3>
                <p>{t('在控制台查看调用结果与用量记录。')}</p>
              </article>
            </div>
          </div>
        </section>
        <section className="public-cta" aria-label={t('开始使用 ZTAPI')}>
          <div className="public-shell">
            <div>
              <p className="section-kicker">{t('从今天开始')}</p>
              <h2>{t('用一个统一接口，让模型选择更自由')}</h2>
            </div>
            <div className="public-cta__actions">
              <Link className="button-link button-link--primary" to="/register">
                {t('创建账号')}
                <ArrowRight aria-hidden="true" size={18} />
              </Link>
              <Link className="button-link button-link--secondary" to="/models">
                {t('模型与价格')}
              </Link>
            </div>
          </div>
        </section>
      </main>
      <footer className="public-footer">
        <div className="public-footer__inner public-shell">
          <span className="public-footer__brand">ZTAPI</span>
          <span>{t('统一模型 API')}</span>
          <a
            className="footer-link"
            href={sourceUrl}
            target="_blank"
            rel="noreferrer"
          >
            {t('ZTAPI 对应源码')}
          </a>
          <a
            className="footer-link"
            href="https://github.com/QuantumNous/new-api"
            target="_blank"
            rel="noreferrer"
          >
            Frontend design and development by New API contributors.
          </a>
        </div>
      </footer>
    </div>
  );
}
