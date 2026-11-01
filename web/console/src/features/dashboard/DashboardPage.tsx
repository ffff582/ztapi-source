import { Activity, Check, Circle, Gauge, ListChecks, RefreshCw } from 'lucide-react';
import { useEffect, useState, useSyncExternalStore } from 'react';
import { apiClient, getAuthSession, subscribeAuthSession } from '../../api/client';
import { loadUsageStatistics, usageRangeTimestamps, type UsageRange, type UsageStatistics } from '../../api/usage-statistics';
import { AccountBalance } from '../wallet/AccountBalance';
import {
  DataContractError,
  parseAuthUser,
  parseUserLogStat,
  parseUserTokenPage,
  type AuthUser,
  type UserLogItem,
  type UserLogStat,
} from '../../api/contracts';
import { localeTag, useLocale } from '../../i18n/locale';
import { onboardingProgress } from '../onboarding/onboarding';

interface DashboardData {
  user: AuthUser;
  stat: UserLogStat;
  logs: UserLogItem[];
  total: number;
  tokenCount: number;
  statistics: UsageStatistics;
  range: UsageRange;
}

type DashboardRange = UsageRange;

const dashboardRanges: Array<{ value: DashboardRange; label: string }> = [
  { value: 'today', label: '今天' },
  { value: 'yesterday', label: '昨天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
];

function formatTimestamp(timestamp: number, locale: 'zh-CN' | 'en') {
  return new Intl.DateTimeFormat(localeTag(locale), {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(timestamp * 1000));
}

export function DashboardPage() {
  const { locale, t } = useLocale();
  const [data, setData] = useState<DashboardData | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [range, setRange] = useState<DashboardRange>('7d');
  const [revision, setRevision] = useState(0);
  const userID = useSyncExternalStore(subscribeAuthSession, getAuthSession, getAuthSession)?.user.id;

  useEffect(() => {
    const controller = new AbortController();
    setStatus('loading');
    setData(null);
    const timestamps = usageRangeTimestamps(range);
    const rangeQuery = new URLSearchParams({
      start_timestamp: String(timestamps.start),
      end_timestamp: String(timestamps.end),
    }).toString();
    void Promise.all([
      apiClient.get<unknown>('/auth/session', { signal: controller.signal }),
      apiClient.get<unknown>(`/log/self/stat?${rangeQuery}`, { signal: controller.signal }),
      loadUsageStatistics(timestamps, controller.signal),
      apiClient.get<unknown>('/token/?p=1&page_size=1', { signal: controller.signal }),
    ])
      .then(([userValue, statValue, statistics, tokenValue]) => {
        const user = parseAuthUser(userValue);
        if (user.id !== userID) throw new DataContractError();
        const tokenPage = parseUserTokenPage(tokenValue);
        if (!controller.signal.aborted && getAuthSession()?.user.id === userID) {
          setData({
            user,
            stat: parseUserLogStat(statValue),
            logs: statistics.recent,
            total: statistics.requestCount,
            tokenCount: tokenPage.total,
            statistics,
            range,
          });
          setStatus('ready');
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setStatus('error');
          controller.abort();
        }
      });
    return () => {
      controller.abort();
    };
  }, [range, revision, userID]);

  return (
    <div className="console-page">
      <header className="console-page__header dashboard-page__header">
        <div>
          <p className="console-eyebrow">{t('控制台')}</p>
          <h1>{t('看板')}</h1>
          <span className="dashboard-page__subtitle">{t('模型、账单、API 文档与调用记录集中在一个工作台。')}</span>
        </div>
        <div className="dashboard-periods" aria-label={t('统计范围')}>
          {dashboardRanges.map((option) => (
            <button
              aria-pressed={range === option.value}
              className={range === option.value ? 'is-active' : undefined}
              key={option.value}
              onClick={() => setRange(option.value)}
              type="button"
            >
              {t(option.label)}
            </button>
          ))}
        </div>
      </header>

      <div className="dashboard-account-balance">
        <AccountBalance />
      </div>

      {status === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          {t('正在加载使用数据...')}
        </div>
      )}
      {status === 'error' && (
        <div className="console-state console-state--error" role="alert">
          <span>{locale === 'en' ? 'Period statistics could not be verified. No partial totals are shown. Retry or select a shorter range.' : '期间统计未能完整核验，未展示部分合计。请重试或缩短范围。'}</span>
          <button className="console-icon-action" type="button" onClick={() => setRevision((value) => value + 1)}
            aria-label={locale === 'en' ? 'Retry statistics' : '重试统计'} title={locale === 'en' ? 'Retry statistics' : '重试统计'}>
            <RefreshCw aria-hidden="true" size={18} />
          </button>
        </div>
      )}
      {status === 'ready' && data !== null && data.range === range && data.user.id === userID && (
        <>
          {(() => {
            const modelCounts = data.logs.reduce<Record<string, number>>((counts, log) => {
              counts[log.model || t('未知模型')] = (counts[log.model || t('未知模型')] || 0) + 1;
              return counts;
            }, {});
            const topModels = Object.entries(modelCounts).sort((left, right) => right[1] - left[1]).slice(0, 4);
            const recentSettled = data.logs.filter((log) => log.status === 'success');
            return (
              <>
                <section className="dashboard-metrics" aria-label={t('关键统计')}>
                  <div className="dashboard-metric-card dashboard-metric-card--spend">
                    <span>{locale === 'en' ? 'Settled charge' : '已结算费用'}</span>
                    <strong>{data.statistics.settledCharge.toFixed(6)} U</strong>
                    <small>{locale === 'en' ? 'Selected period, excluding pending holds' : '所选期间，不含待核账预留'}</small>
                  </div>
                  <div className="dashboard-metric-card dashboard-metric-card--requests">
                    <span>{locale === 'en' ? 'Request records' : '请求记录数'}</span>
                    <strong>{data.total}</strong>
                    <small>{locale === 'en' ? 'Selected period, settled and failed records' : '所选期间，已结算与失败记录'}</small>
                  </div>
                  <div className="dashboard-metric-card dashboard-metric-card--tokens">
                    <span>{locale === 'en' ? 'Settled tokens' : '已结算 Token'}</span>
                    <strong>{data.statistics.settledTokens.toLocaleString(localeTag(locale))}</strong>
                    <small>{locale === 'en' ? 'Selected period, input + output' : '所选期间，输入 + 输出'}</small>
                  </div>
                </section>

                <section className="dashboard-analysis" aria-label={t('调用分析')}>
                  <div className="dashboard-analysis__panel">
                    <div className="dashboard-analysis__heading">
                      <div><p className="console-eyebrow">{t('最近请求')}</p><h2>{locale === 'en' ? 'Recent settled charges' : '最近已结算费用'}</h2></div>
                      <span>{t('按当前展示记录')}</span>
                    </div>
                    <div className="dashboard-bars" aria-label={t('最近记录消费趋势')}>
                      {recentSettled.length === 0 ? <span>{t('暂无足够数据')}</span> : recentSettled.map((log) => (
                        <div key={`${log.timestamp}-${log.request_id}`} className="dashboard-bars__item">
                          <i style={{ height: `${Math.max(12, Math.min(100, log.billed_amount * 10000))}%` }} />
                          <small>{log.model || t('未知')}</small>
                        </div>
                      ))}
                    </div>
                  </div>
                  <div className="dashboard-analysis__panel">
                    <div className="dashboard-analysis__heading">
                      <div><p className="console-eyebrow">{t('调用分布')}</p><h2>{locale === 'en' ? 'Recent-record model distribution' : '最近记录模型分布'}</h2></div>
                      <span>{t('{{count}} 条记录', { count: data.logs.length })}</span>
                    </div>
                    <div className="dashboard-model-list">
                      {topModels.length === 0 ? <span>{t('当前账户还没有请求记录。')}</span> : topModels.map(([model, count]) => (
                        <div key={model}><span>{model}</span><strong>{count}</strong></div>
                      ))}
                    </div>
                  </div>
                </section>
              </>
            );
          })()}

          {(() => {
            const progress = onboardingProgress(data.user.id, {
              tokenCount: data.tokenCount,
              requestCount: data.total,
            });
            const steps = [
              { complete: progress.hasKey, label: '创建 API Key', href: '/console/keys' },
              { complete: progress.selectedModel, label: '选择并测试模型', href: '/console/test' },
              { complete: progress.sentRequest, label: '发送首个请求', href: '/console/test' },
              { complete: progress.reviewedLogs, label: '查看费用日志', href: '/console/logs' },
            ];
            return (
              <section className="onboarding-progress" aria-label={t('首次接入进度')}>
                <div className="onboarding-progress__header">
                  <div>
                    <p className="console-eyebrow">{t('快速开始')}</p>
                    <h2>{t('完成首次接入')}</h2>
                  </div>
                  <strong>{t('{{count}} / 4 已完成', { count: progress.completed })}</strong>
                </div>
                <ol>
                  {steps.map((step, index) => (
                    <li data-complete={String(step.complete)} key={step.label}>
                      <span className="onboarding-progress__icon">
                        {step.complete ? <Check aria-hidden="true" size={16} /> : <Circle aria-hidden="true" size={16} />}
                      </span>
                      <div>
                        <small>{String(index + 1).padStart(2, '0')}</small>
                        <strong>{t(step.label)}</strong>
                      </div>
                      <a href={step.href}>{step.complete ? t('查看') : t('去完成')}</a>
                    </li>
                  ))}
                </ol>
              </section>
            );
          })()}

          <section className="dashboard-summary" aria-label={t('实时统计')}>
            <div className="summary-metric">
              <Activity aria-hidden="true" size={20} />
              <span>{t('当前账户')}</span>
              <strong>{data.user.username}</strong>
            </div>
            <div className="summary-metric">
              <Gauge aria-hidden="true" size={20} />
              <span>{locale === 'en' ? 'Settled requests in the last minute' : '最近一分钟已结算请求'}</span>
              <strong>{data.stat.rpm}</strong>
            </div>
            <div className="summary-metric">
              <ListChecks aria-hidden="true" size={20} />
              <span>{locale === 'en' ? 'Settled tokens in the last minute' : '最近一分钟已结算 Token'}</span>
              <strong>{data.stat.tpm}</strong>
            </div>
          </section>

          <section className="console-section" aria-labelledby="recent-log-heading">
            <div className="console-section__heading">
              <div>
                <p className="console-eyebrow">{t('最近活动')}</p>
                <h2 id="recent-log-heading">{t('最近请求')}</h2>
              </div>
              <span>{t('{{count}} 条', { count: data.total })}</span>
            </div>
            {data.logs.length === 0 ? (
              <div className="console-state">{t('当前账户还没有请求记录。')}</div>
            ) : (
              <div className="console-table-wrap usage-log-table-wrap">
                <table className="console-table usage-log-table">
                  <thead>
                    <tr>
                      <th scope="col">{t('模型')}</th>
                      <th scope="col">{locale === 'en' ? 'Settled charge' : '已结算费用'}</th>
                      <th scope="col">{t('Token 用量')}</th>
                      <th scope="col">{t('状态与耗时')}</th>
                      <th scope="col">{t('调用时间')}</th>
                      <th scope="col">{t('请求 ID')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.logs.map((log) => (
                      <tr key={`${log.timestamp}-${log.request_id}`}>
                        <td className="usage-log-model-cell">
                          <span aria-hidden="true" className="usage-log-cell-label">{t('模型')}</span>
                          <strong>{log.model || '—'}</strong>
                        </td>
                        <td className="usage-log-charge-cell">
                          <span aria-hidden="true" className="usage-log-cell-label">{locale === 'en' ? 'Settled charge' : '已结算费用'}</span>
                          <strong>{log.status === 'success' ? `${log.billed_amount.toFixed(6)} U` : '—'}</strong>
                          <small>{log.status === 'success' ? t('本次实际扣费') : locale === 'en' ? 'No settlement recorded' : '未记录结算费用'}</small>
                        </td>
                        <td>
                          <span aria-hidden="true" className="usage-log-cell-label">{t('Token 用量')}</span>
                          <div className="usage-log-tokens">
                            <span>{t('输入 {{count}}', { count: log.prompt_tokens })}</span>
                            <span>{t('输出 {{count}}', { count: log.completion_tokens })}</span>
                            <strong>{t('总计 {{count}}', { count: log.total_tokens })}</strong>
                          </div>
                        </td>
                        <td>
                          <span aria-hidden="true" className="usage-log-cell-label">{t('状态与耗时')}</span>
                          <div className="usage-log-status">
                          <span
                            className={`console-status console-status--${log.status}`}
                          >
                            {log.status === 'success'
                              ? locale === 'en' ? 'Settled' : '已结算'
                              : log.status === 'error'
                                ? t('失败')
                                : t('记录')}
                          </span>
                            <span>{t('{{count}} 秒', { count: log.latency })}</span>
                          </div>
                        </td>
                        <td>
                          <span aria-hidden="true" className="usage-log-cell-label">{t('调用时间')}</span>
                          {formatTimestamp(log.timestamp, locale)}
                        </td>
                        <td className="usage-log-request-cell">
                          <span aria-hidden="true" className="usage-log-cell-label">{t('请求 ID')}</span>
                          <code title={log.request_id}>{log.request_id || '—'}</code>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}
