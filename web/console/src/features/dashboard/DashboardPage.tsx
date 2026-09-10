import { Activity, Gauge, ListChecks } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiClient } from '../../api/client';
import { AccountBalance } from '../wallet/AccountBalance';
import {
  parseAuthUser,
  parseUserLogPage,
  parseUserLogStat,
  type AuthUser,
  type UserLogItem,
  type UserLogStat,
} from '../../api/contracts';

interface DashboardData {
  user: AuthUser;
  stat: UserLogStat;
  logs: UserLogItem[];
  total: number;
}

function formatTimestamp(timestamp: number) {
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(timestamp * 1000));
}

export function DashboardPage() {
  const [data, setData] = useState<DashboardData | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');

  useEffect(() => {
    let active = true;
    void Promise.all([
      apiClient.get<unknown>('/auth/session'),
      apiClient.get<unknown>('/log/self/stat'),
      apiClient.get<unknown>('/log/self?p=1&page_size=5'),
    ])
      .then(([userValue, statValue, logValue]) => {
        const page = parseUserLogPage(logValue);
        if (active) {
          setData({
            user: parseAuthUser(userValue),
            stat: parseUserLogStat(statValue),
            logs: page.items,
            total: page.total,
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
          <p className="console-eyebrow">账户与使用</p>
          <h1>使用概览</h1>
        </div>
        <p>查看当前会话与最近请求的实际统计。</p>
      </header>

      <AccountBalance />

      {status === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          正在加载使用数据...
        </div>
      )}
      {status === 'error' && (
        <div className="console-state console-state--error" role="alert">
          使用数据加载失败，请稍后重试。
        </div>
      )}
      {status === 'ready' && data !== null && (
        <>
          <section className="dashboard-summary" aria-label="实时统计">
            <div className="summary-metric">
              <Activity aria-hidden="true" size={20} />
              <span>当前账户</span>
              <strong>{data.user.username}</strong>
            </div>
            <div className="summary-metric">
              <Gauge aria-hidden="true" size={20} />
              <span>最近一分钟请求</span>
              <strong>{data.stat.rpm}</strong>
            </div>
            <div className="summary-metric">
              <ListChecks aria-hidden="true" size={20} />
              <span>最近一分钟 Tokens</span>
              <strong>{data.stat.tpm}</strong>
            </div>
          </section>

          <section className="console-section" aria-labelledby="recent-log-heading">
            <div className="console-section__heading">
              <div>
                <p className="console-eyebrow">最近活动</p>
                <h2 id="recent-log-heading">最近请求</h2>
              </div>
              <span>{data.total} 条</span>
            </div>
            {data.logs.length === 0 ? (
              <div className="console-state">当前账户还没有请求记录。</div>
            ) : (
              <div className="console-table-wrap">
                <table className="console-table">
                  <thead>
                    <tr>
                      <th scope="col">时间</th>
                      <th scope="col">请求 ID</th>
                      <th scope="col">模型</th>
                      <th scope="col">状态</th>
                      <th scope="col">Tokens</th>
                      <th scope="col">计费金额</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.logs.map((log) => (
                      <tr key={`${log.timestamp}-${log.request_id}`}>
                        <td>{formatTimestamp(log.timestamp)}</td>
                        <td>
                          <code>{log.request_id || '—'}</code>
                        </td>
                        <td>{log.model || '—'}</td>
                        <td>
                          <span
                            className={`console-status console-status--${log.status}`}
                          >
                            {log.status === 'success'
                              ? '成功'
                              : log.status === 'error'
                                ? '失败'
                                : '记录'}
                          </span>
                        </td>
                        <td>{log.total_tokens}</td>
                        <td>${log.billed_amount.toFixed(6)}</td>
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
