import { Activity, BarChart3, BookOpen, Boxes, KeyRound, WalletCards } from 'lucide-react';
import { useLocale } from '../../i18n/locale';

const navigation: Array<{ label: string; icon: typeof BarChart3; active?: boolean }> = [
  { label: '看板', icon: BarChart3, active: true },
  { label: '工作台', icon: Activity },
  { label: '模型市场', icon: Boxes },
  { label: 'API 文档', icon: BookOpen },
  { label: 'API Key', icon: KeyRound },
  { label: '余额充值', icon: WalletCards },
];

const recentRequests = [
  ['对话模型', '成功', '0.0042 U'],
  ['推理模型', '成功', '0.0068 U'],
  ['图片模型', '成功', '0.0120 U'],
] as const;

export function DashboardPreview() {
  const { t } = useLocale();

  return (
    <div className="dashboard-preview" data-testid="dashboard-preview">
      <div className="dashboard-preview__bar">
        <div className="dashboard-preview__brand">
          <img src="/brand/ztapi-mark.png" alt="" width="22" height="22" />
          <strong>ZTAPI</strong>
        </div>
        <span className="dashboard-preview__search">{t('搜索模型、文档或功能')}</span>
        <span className="dashboard-preview__user">Z&nbsp;&nbsp;{t('开发者')}⌄</span>
      </div>
      <div className="dashboard-preview__body">
        <aside className="dashboard-preview__nav" aria-label={t('用户台预览导航')}>
          {navigation.map(({ label, icon: Icon, active }) => (
            <div className={`dashboard-preview__nav-item${active ? ' is-active' : ''}`} key={label}>
              <Icon aria-hidden="true" size={14} />
              <span>{t(label)}</span>
            </div>
          ))}
        </aside>
        <div className="dashboard-preview__content">
          <div className="dashboard-preview__heading">
            <div>
              <small>{t('用户台预览')}</small>
              <h2>{t('使用概览')}</h2>
            </div>
            <span>{t('近 7 天')}⌄</span>
          </div>
          <div className="dashboard-preview__metrics">
            <div><small>{t('当前余额')}</small><strong>-- U</strong></div>
            <div><small>{t('历史消费')}</small><strong>-- U</strong></div>
            <div><small>{t('请求次数')}</small><strong>--</strong></div>
            <div><small>{t('Token 用量')}</small><strong>--</strong></div>
          </div>
          <div className="dashboard-preview__quick-start">
            <div><small>{t('快速开始')}</small><strong>{t('创建 Key，选择模型，发起第一笔请求')}</strong></div>
            <span>{t('查看接入指南')} →</span>
          </div>
          <div className="dashboard-preview__lower">
            <div className="dashboard-preview__table">
              <div className="dashboard-preview__section-title"><strong>{t('最近使用记录')}</strong><span>{t('查看全部')} →</span></div>
              <div className="dashboard-preview__table-head"><span>{t('模型')}</span><span>{t('状态')}</span><span>{t('费用')}</span></div>
              {recentRequests.map(([model, status, charge]) => (
                <div className="dashboard-preview__table-row" key={model}>
                  <span>{t(model)}</span><span className="dashboard-preview__success">● {t(status)}</span><span>{charge}</span>
                </div>
              ))}
            </div>
            <div className="dashboard-preview__chart">
              <div className="dashboard-preview__section-title"><strong>{t('消费趋势')}</strong><span>{t('近 7 天')}</span></div>
              <div className="dashboard-preview__bars" aria-hidden="true">
                <i style={{ height: '28%' }} /><i style={{ height: '46%' }} /><i style={{ height: '36%' }} /><i style={{ height: '70%' }} /><i style={{ height: '52%' }} /><i style={{ height: '84%' }} /><i style={{ height: '64%' }} />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
