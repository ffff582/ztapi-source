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

  useEffect(() => {
    let active = true;
    void Promise.all([
      apiClient.get<unknown>('/auth/session'),
      apiClient.get<unknown>('/log/self/stat'),
      apiClient.get<unknown>('/log/self?p=1&page_size=5'),
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
  }, []);

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{t('账户与使用')}</p>
          <h1>{t('使用概览')}</h1>
        </div>
        <p>{t('查看当前会话与最近请求的实际统计。')}</p>
      </header>

      <AccountBalance />

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
                          <strong>${log.billed_amount.toFixed(6)}</strong>
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
