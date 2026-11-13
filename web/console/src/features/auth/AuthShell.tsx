import type { PropsWithChildren } from 'react';
import { Link } from 'react-router-dom';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { useLocale } from '../../i18n/locale';
import { AuthTopology } from './AuthTopology';
import './auth.css';

type AuthShellProps = PropsWithChildren<{
  eyebrow: string;
  title: string;
  intro: string;
}>;

export function AuthShell({
  eyebrow,
  title,
  intro,
  children,
}: AuthShellProps) {
  const { t } = useLocale();

  return (
    <div className="auth-page">
      <PublicHeader />
      <div className="auth-shell">
      <aside className="auth-context" aria-label={t('ZTAPI 网关信息')}>
        <Link className="auth-context__brand" to="/" aria-label={t('ZTAPI 首页')}>
          <img src="/brand/ztapi-mark.png" alt="" width="32" height="32" />
          <span>ZTAPI</span>
        </Link>
        <div className="auth-context__message">
          <p>UNIFIED MODEL GATEWAY</p>
          <h2>
            <span className="auth-context__headline-line">{t('一个接口，')}</span>
            <span className="auth-context__headline-line">{t('连接模型与业务。')}</span>
          </h2>
        </div>
        <AuthTopology />
        <ul className="auth-context__facts">
          <li>{t('OpenAI 兼容接口')}</li>
          <li>{t('按量计费')}</li>
          <li>{t('可配置路由')}</li>
        </ul>
        <code>https://ztapi.vip/v1</code>
      </aside>
      <main className="auth-workspace">
        <Link className="auth-home-link" to="/">
          {t('返回首页')}
        </Link>
        <section className="auth-form-panel" aria-labelledby="auth-heading">
          <p className="auth-kicker">{eyebrow}</p>
          <h1 id="auth-heading">{title}</h1>
          <p className="auth-intro">{intro}</p>
          {children}
        </section>
      </main>
      </div>
    </div>
  );
}
