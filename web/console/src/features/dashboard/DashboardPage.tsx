import { Activity, Check, Circle, Gauge, ListChecks } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiClient } from '../../api/client';
import { AccountBalance } from '../wallet/AccountBalance';
import {
  parseAuthUser,
  parseUserLogPage,
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
}

type DashboardRange = 'today' | 'yesterday' | '7d' | '30d';

const dashboardRanges: Array<{ value: DashboardRange; label: string }> = [
  { value: 'today', label: '今天' },
  { value: 'yesterday', label: '昨天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
];

function dashboardRangeTimestamps(range: DashboardRange) {
  const now = new Date();
  const endOfRange = Math.floor(now.getTime() / 1000);
  const startOfToday = new Date(now);
  startOfToday.setHours(0, 0, 0, 0);
  const todayStart = Math.floor(startOfToday.getTime() / 1000);
  if (range === 'today') {
    return { start: todayStart, end: endOfRange };
  }
  if (range === 'yesterday') {
    const yesterdayStart = new Date(startOfToday);
    yesterdayStart.setDate(yesterdayStart.getDate() - 1);
    return { start: Math.floor(yesterdayStart.getTime() / 1000), end: todayStart };
  }
  const days = range === '7d' ? 6 : 29;
  const rangeStart = new Date(startOfToday);
  rangeStart.setDate(rangeStart.getDate() - days);
  return { start: Math.floor(rangeStart.getTime() / 1000), end: endOfRange };
}

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

  useEffect(() => {
    let active = true;
    setStatus('loading');
    const timestamps = dashboardRangeTimestamps(range);
    const rangeQuery = new URLSearchParams({
      start_timestamp: String(timestamps.start),
      end_timestamp: String(timestamps.end),
    }).toString();
    void Promise.all([
      apiClient.get<unknown>('/auth/session'),
      apiClient.get<unknown>(`/log/self/stat?${rangeQuery}`),
      apiClient.get<unknown>(`/log/self?p=1&page_size=5&${rangeQuery}`),
      apiClient.get<unknown>('/token/?p=1&page_size=1'),
    ])
      .then(([userValue, statValue, logValue, tokenValue]) => {
        const page = parseUserLogPage(logValue);
        const tokenPage = parseUserTokenPage(tokenValue);
        if (active) {
          setData({
            user: parseAuthUser(userValue),
            stat: parseUserLogStat(statValue),
            logs: page.items,
            total: page.total,
            tokenCount: tokenPage.total,
          });
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
  }, [range]);

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
          {t('使用数据加载失败，请稍后重试。')}
        </div>
      )}
      {status === 'ready' && data !== null && (
        <>
          {(() => {
            const recentSpend = data.logs.reduce((total, log) => total + log.billed_amount, 0);
            const recentTokens = data.logs.reduce((total, log) => total + log.total_tokens, 0);
            const modelCounts = data.logs.reduce<Record<string, number>>((counts, log) => {
              counts[log.model || t('未知模型')] = (counts[log.model || t('未知模型')] || 0) + 1;
              return counts;
            }, {});
            const topModels = Object.entries(modelCounts).sort((left, right) => right[1] - left[1]).slice(0, 4);
            return (
              <>
                <section className="dashboard-metrics" aria-label={t('关键统计')}>
                  <div className="dashboard-metric-card dashboard-metric-card--spend">
                    <span>{t('最近记录消费')}</span>
                    <strong>{recentSpend.toFixed(6)} U</strong>
                    <small>{t('按当前展示记录统计')}</small>
                  </div>
                  <div className="dashboard-metric-card dashboard-metric-card--requests">
                    <span>{t('请求次数')}</span>
                    <strong>{data.total}</strong>
                    <small>{t('最近一分钟 {{count}} 次', { count: data.stat.rpm })}</small>
                  </div>
                  <div className="dashboard-metric-card dashboard-metric-card--tokens">
                    <span>{t('Token 用量')}</span>
                    <strong>{recentTokens.toLocaleString()}</strong>
                    <small>{t('最近展示记录合计')}</small>
                  </div>
                </section>

                <section className="dashboard-analysis" aria-label={t('调用分析')}>
                  <div className="dashboard-analysis__panel">
                    <div className="dashboard-analysis__heading">
                      <div><p className="console-eyebrow">{t('最近请求')}</p><h2>{t('消费趋势')}</h2></div>
                      <span>{t('按当前展示记录')}</span>
                    </div>
                    <div className="dashboard-bars" aria-label={t('最近记录消费趋势')}>
                      {data.logs.length === 0 ? <span>{t('暂无足够数据')}</span> : data.logs.map((log) => (
                        <div key={`${log.timestamp}-${log.request_id}`} className="dashboard-bars__item">
                          <i style={{ height: `${Math.max(12, Math.min(100, log.billed_amount * 10000))}%` }} />
                          <small>{log.model || t('未知')}</small>
                        </div>
                      ))}
                    </div>
                  </div>
                  <div className="dashboard-analysis__panel">
                    <div className="dashboard-analysis__heading">
                      <div><p className="console-eyebrow">{t('调用分布')}</p><h2>{t('模型排行')}</h2></div>
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
              <span>{t('最近一分钟请求')}</span>
              <strong>{data.stat.rpm}</strong>
            </div>
            <div className="summary-metric">
              <ListChecks aria-hidden="true" size={20} />
              <span>{t('最近一分钟 Tokens')}</span>
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
                      <th scope="col">{t('实际费用')}</th>
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
                          <span aria-hidden="true" className="usage-log-cell-label">{t('实际费用')}</span>
                          <strong>{log.billed_amount.toFixed(6)} U</strong>
                          <small>{t('本次实际扣费')}</small>
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
                              ? t('成功')
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
