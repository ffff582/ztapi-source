import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiClient } from '../../api/client';
import {
  parseUserLogPage,
  type PageEnvelope,
  type UserLogItem,
} from '../../api/contracts';
import { localeTag, useLocale } from '../../i18n/locale';

function formatTimestamp(timestamp: number, locale: 'zh-CN' | 'en') {
  return new Intl.DateTimeFormat(localeTag(locale), {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(timestamp * 1000));
}

function publicStatus(status: UserLogItem['status'], t: (key: string) => string) {
  if (status === 'success') {
    return t('成功');
  }
  if (status === 'error') {
    return t('失败');
  }
  return t('记录');
}

export function LogsPage() {
  const { locale, t } = useLocale();
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
          <p className="console-eyebrow">{t('公开请求记录')}</p>
          <h1>{t('使用日志')}</h1>
        </div>
        <p>{t('仅展示 ZTAPI 请求标识、公开模型、用量与计费结果。')}</p>
      </header>

      {status === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          {t('正在加载使用日志...')}
        </div>
      )}
      {status === 'error' && (
        <div className="console-state console-state--error" role="alert">
          {t('使用日志加载失败，请稍后重试。')}
        </div>
      )}
      {status === 'ready' && page !== null && page.items.length === 0 && (
        <div className="console-state">{t('当前账户还没有请求日志。')}</div>
      )}
      {status === 'ready' && page !== null && page.items.length > 0 && (
        <section className="console-section" aria-label={t('使用日志列表')}>
          <div className="console-section__heading">
            <div>
              <p className="console-eyebrow">{t('第 {{page}} 页', { page: page.page })}</p>
              <h2>{t('请求明细')}</h2>
            </div>
            <span>{t('{{count}} 条', { count: page.total })}</span>
          </div>
          <div className="console-table-wrap usage-log-table-wrap">
            <table className="console-table logs-table usage-log-table">
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
                {page.items.map((log) => (
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
                          {publicStatus(log.status, t)}
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
          <nav className="console-pagination" aria-label={t('日志分页')}>
            <button
              aria-label={t('上一页')}
              className="console-icon-action"
              disabled={pageNumber <= 1}
              title={t('上一页')}
              type="button"
              onClick={() => setPageNumber((current) => current - 1)}
            >
              <ChevronLeft aria-hidden="true" size={17} />
            </button>
            <span>{t('第 {{page}} / {{pages}} 页', { page: pageNumber, pages: pageCount })}</span>
            <button
              aria-label={t('下一页')}
              className="console-icon-action"
              disabled={pageNumber >= pageCount}
              title={t('下一页')}
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
