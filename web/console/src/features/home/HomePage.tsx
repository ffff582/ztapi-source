import { useEffect } from 'react';
import { ArrowRight, CircleDollarSign, Gift, ListTree, Route, Send } from 'lucide-react';
import { Link, useLocation } from 'react-router-dom';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { GatewayStatusRail } from './GatewayStatusRail';
import { PublicModelProof } from './PublicModelProof';
import { useLocale } from '../../i18n/locale';
import { GettingStartedPaths } from './GettingStartedPaths';
import { DashboardPreview } from './DashboardPreview';
import './home.css';

export function HomePage() {
  const { t } = useLocale();
  const { hash } = useLocation();
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
        <section className="gateway-hero" aria-label={t('ZTAPI')}>
          <div className="gateway-hero__content public-shell">
            <div className="gateway-hero__copy">
              <p className="gateway-hero__eyebrow">ZTAPI UNIFIED AI API</p>
              <h1 id="gateway-hero-title" className="gateway-hero__title">
                {t('一个接口，连接主流 AI 模型')}
              </h1>
            </div>
            <p className="gateway-hero__summary">
              {t('用一套统一的 API 接入文本、推理、图片和视频模型，清晰管理调用、用量和费用。')}
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
            <div className="gateway-hero__proofs" aria-label={t('平台特点')}>
              <span><b>01</b>{t('统一接口')}</span>
              <span><b>02</b>{t('透明计费')}</span>
              <span><b>03</b>{t('清晰日志')}</span>
            </div>
          </div>
          <div className="gateway-hero__visual" aria-label={t('ZTAPI 用户台预览')}>
            <DashboardPreview />
          </div>
        </section>
        <GatewayStatusRail />
        <section className="topup-promotion" aria-label={t('充值福利')}>
          <div className="topup-promotion__inner public-shell">
            <Gift aria-hidden="true" size={28} />
            <div>
              <p className="section-kicker">{t('充值福利')}</p>
              <h2>{t('充值即赠 5% 使用额度')}</h2>
              <p>{t('充值 100 U，到账可用 105 U')}</p>
            </div>
            <div className="topup-promotion__actions">
              <Link className="button-link button-link--primary" to="/register">
                {t('立即开始')}
                <ArrowRight aria-hidden="true" size={18} />
              </Link>
              <a
                className="button-link button-link--support"
                href="https://t.me/gan66"
                target="_blank"
                rel="noreferrer"
              >
                <Send aria-hidden="true" size={17} />
                {t('联系 Telegram 客服')}
              </a>
            </div>
          </div>
        </section>
        <PublicModelProof />
        <GettingStartedPaths />
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
        </div>
      </footer>
    </div>
  );
}
