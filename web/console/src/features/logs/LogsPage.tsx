import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiClient } from '../../api/client';
import {
  parseUserLogPage,
  type PageEnvelope,
  type UserLogItem,
} from '../../api/contracts';

function formatTimestamp(timestamp: number) {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(timestamp * 1000));
}

function publicStatus(status: UserLogItem['status']) {
  if (status === 'success') {
    return '成功';
  }
  if (status === 'error') {
    return '失败';
  }
  return '记录';
}

export function LogsPage() {
  const [pageNumber, setPageNumber] = useState(1);
  const [page, setPage] = useState<PageEnvelope<UserLogItem> | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');

  useEffect(() => {
    let active = true;
    setStatus('loading');
    void apiClient
      .get<unknown>(`/log/self?p=${pageNumber}&page_size=50`)
      .then(parseUserLogPage)
      .then((value) => {
        if (active) {
          setPage(value);
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
  }, [pageNumber]);

  const pageCount =
    page === null ? 1 : Math.max(1, Math.ceil(page.total / page.page_size));

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">公开请求记录</p>
          <h1>使用日志</h1>
        </div>
        <p>仅展示 ZTAPI 请求标识、公开模型、用量与计费结果。</p>
      </header>

      {status === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          正在加载使用日志...
        </div>
      )}
      {status === 'error' && (
        <div className="console-state console-state--error" role="alert">
          使用日志加载失败，请稍后重试。
        </div>
      )}
      {status === 'ready' && page !== null && page.items.length === 0 && (
        <div className="console-state">当前账户还没有请求日志。</div>
      )}
      {status === 'ready' && page !== null && page.items.length > 0 && (
        <section className="console-section" aria-label="使用日志列表">
          <div className="console-section__heading">
            <div>
              <p className="console-eyebrow">第 {page.page} 页</p>
              <h2>请求明细</h2>
            </div>
            <span>{page.total} 条</span>
          </div>
          <div className="console-table-wrap">
            <table className="console-table logs-table">
              <thead>
                <tr>
                  <th scope="col">时间</th>
                  <th scope="col">ZTAPI 请求 ID</th>
                  <th scope="col">公开模型</th>
                  <th scope="col">状态</th>
                  <th scope="col">延迟</th>
                  <th scope="col">提示 Tokens</th>
                  <th scope="col">补全 Tokens</th>
                  <th scope="col">总 Tokens</th>
                  <th scope="col">计费金额</th>
                </tr>
              </thead>
              <tbody>
                {page.items.map((log) => (
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
                        {publicStatus(log.status)}
                      </span>
                    </td>
                    <td>{log.latency} 秒</td>
                    <td>{log.prompt_tokens}</td>
                    <td>{log.completion_tokens}</td>
                    <td>{log.total_tokens}</td>
                    <td>${log.billed_amount.toFixed(6)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <nav className="console-pagination" aria-label="日志分页">
            <button
              aria-label="上一页"
              className="console-icon-action"
              disabled={pageNumber <= 1}
              title="上一页"
              type="button"
              onClick={() => setPageNumber((current) => current - 1)}
            >
              <ChevronLeft aria-hidden="true" size={17} />
            </button>
            <span>第 {pageNumber} / {pageCount} 页</span>
            <button
              aria-label="下一页"
              className="console-icon-action"
              disabled={pageNumber >= pageCount}
              title="下一页"
              type="button"
              onClick={() => setPageNumber((current) => current + 1)}
            >
              <ChevronRight aria-hidden="true" size={17} />
            </button>
          </nav>
        </section>
      )}
    </div>
  );
}
