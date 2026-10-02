import { ArrowRight, Boxes, KeyRound, PlayCircle, ScrollText } from 'lucide-react';
import { Link } from 'react-router-dom';
import type { LucideIcon } from 'lucide-react';
import { useLocale } from '../../i18n/locale';

type GettingStartedPath = {
  label: string;
  description: string;
  to: string;
  icon: LucideIcon;
};

const paths: GettingStartedPath[] = [
  {
    label: '查看模型目录',
    description: '按能力、厂商和实时售价找到适合的模型。',
    to: '/models',
    icon: Boxes,
  },
  {
    label: '创建 API Key',
    description: '注册账号后生成密钥，完整密钥只展示一次。',
    to: '/register',
    icon: KeyRound,
  },
  {
    label: '查看使用日志',
    description: '调用后在控制台查看模型、用量、费用和状态。',
    to: '/register',
    icon: ScrollText,
  },
  {
    label: '在线测试',
    description: '在控制台验证模型返回和本次费用。',
    to: '/register',
    icon: PlayCircle,
  },
];

export function GettingStartedPaths() {
  const { t } = useLocale();

  return (
    <section id="quickstart" className="integration-band" aria-label={t('快速接入')}>
      <div className="public-shell integration-band__inner">
        <div className="getting-started__heading">
          <div>
            <p className="section-kicker">{t('快速集成')}</p>
            <h2>{t('一个接口，快速接入你需要的模型')}</h2>
          </div>
          <p>{t('统一密钥、模型目录、使用日志和账单，按量调用，不需要先理解复杂的供应商配置。')}</p>
        </div>

        <div className="getting-started__layout">
          <article className="getting-started__featured">
            <div className="getting-started__featured-top">
              <span className="getting-started__step">01</span>
              <div>
                <p className="getting-started__eyebrow">{t('3 步完成接入')}</p>
                <h3>{t('注册、改一行配置、开始调用')}</h3>
              </div>
            </div>
            <p>{t('保留熟悉的 OpenAI SDK，只需要准备 ZTAPI Key、模型 ID 和 Base URL。')}</p>
            <Link className="getting-started__primary-link" to="/docs/integration">
              {t('查看集成指南')}
              <ArrowRight aria-hidden="true" size={18} />
            </Link>
            <ol className="getting-started__sequence" aria-label={t('接入流程')}>
              <li><span>01</span><strong>{t('创建 API Key')}</strong><small>{t('注册并进入控制台')}</small></li>
              <li><span>02</span><strong>{t('修改 Base URL')}</strong><small>{t('只替换 API 地址和模型 ID')}</small></li>
              <li><span>03</span><strong>{t('发送第一笔请求')}</strong><small>{t('在使用日志核对费用')}</small></li>
            </ol>
          </article>

          <nav className="getting-started__list" aria-label={t('服务与支持')}>
            {paths.map(({ label, description, to, icon: Icon }) => (
              <Link className="getting-started__item" key={label} to={to}>
                <Icon aria-hidden="true" size={21} />
                <span>
                  <strong>{t(label)}</strong>
                  <small>{t(description)}</small>
                </span>
                <ArrowRight aria-hidden="true" size={18} />
              </Link>
            ))}
          </nav>
        </div>
      </div>
    </section>
  );
}
