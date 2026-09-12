import { ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react';
import { useCallback, useState } from 'react';
import { apiClient } from '../../api/client';
import { parseUserTopUpPage } from '../../api/contracts';
import { formatAccountUSD } from './AccountBalance';
import { useAccountResource } from './useAccountResource';
import { localeTag, useLocale, type Locale } from '../../i18n/locale';

const PAGE_SIZE = 5;
const statusLabels: Record<string, string> = {
  success: '已到账', pending: '待确认', expired: '已过期', failed: '失败',
  rejected: '已拒绝', manual_review: '待人工核验', confirming: '确认中',
};

export function formatTopUpTime(timestamp: number, locale: Locale = 'zh-CN') {
  return timestamp > 0 ? new Date(timestamp * 1000).toLocaleString(localeTag(locale), { hour12: false }) : '--';
}

export function TopUpHistory() {
  const { locale, t } = useLocale();
  const [page, setPage] = useState(1);
  const load = useCallback(async (signal: AbortSignal) => parseUserTopUpPage(
    await apiClient.get<unknown>(`/user/topup/self?p=${page}&page_size=${PAGE_SIZE}`, { signal }),
  ), [page]);
  const history = useAccountResource(load);
  return (
    <section className="console-section wallet-history" aria-labelledby="topup-history-heading">
      <div className="console-section__heading">
        <div><h2 id="topup-history-heading">{t('充值记录')}</h2><span>{t('最近 30 天')}</span></div>
        <button type="button" className="console-icon-action" aria-label={t('刷新充值记录')}
          title={t('刷新充值记录')} onClick={history.refresh} disabled={history.status === 'loading'}>
          <RefreshCw aria-hidden="true" size={18} />
        </button>
      </div>
      {history.status === 'loading' && <div className="console-state" aria-busy="true">{t('正在读取充值记录...')}</div>}
      {history.status === 'error' && <div className="console-state console-state--error" role="status">{t('充值记录加载失败，请刷新重试。')}</div>}
      {history.status === 'ready' && <>
        {history.data.items.length === 0 ? <div className="console-state">{t('暂无充值记录。')}</div> : (
          <div className="console-table-wrap" tabIndex={0} aria-label={t('充值订单列表')}>
            <table className="console-table wallet-history__table">
              <thead><tr>
                <th scope="col">{t('订单号 / 创建时间')}</th><th scope="col">{t('订单金额')}</th>
                <th scope="col">{t('已入账余额 (USD)')}</th><th scope="col">{t('状态')}</th><th scope="col">{t('到账时间')}</th>
              </tr></thead>
              <tbody>{history.data.items.map((item) => (
                <tr key={item.id}>
                  <td><code>{item.trade_no}</code><small>{formatTopUpTime(item.create_time, locale)}</small></td>
                  <td data-label={t('订单金额')}>{item.payment_provider === 'usdt_trc20' ? `${item.money.toFixed(2)} USDT` : '--'}</td>
                  <td data-label={t('已入账余额 (USD)')}>{item.status === 'success' && item.payment_provider === 'usdt_trc20' ? formatAccountUSD(item.amount) : '--'}</td>
                  <td data-label={t('状态')}><span className={`console-status console-status--${item.status === 'success' ? 'success' : 'info'}`}>{t(statusLabels[item.status] ?? '待核验')}</span></td>
                  <td data-label={t('到账时间')}>{item.status === 'success' ? formatTopUpTime(item.complete_time, locale) : '--'}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
        <nav className="console-pagination" aria-label={t('充值记录分页')}>
          <span>{t('第 {{page}} 页 · 共 {{total}} 条', { page, total: history.data.total })}</span>
          <button className="console-icon-action" type="button" aria-label={t('上一页充值记录')} title={t('上一页')}
            disabled={page <= 1} onClick={() => setPage((value) => value - 1)}><ChevronLeft aria-hidden="true" size={18} /></button>
          <button className="console-icon-action" type="button" aria-label={t('下一页充值记录')} title={t('下一页')}
            disabled={page * PAGE_SIZE >= history.data.total} onClick={() => setPage((value) => value + 1)}><ChevronRight aria-hidden="true" size={18} /></button>
        </nav>
      </>}
    </section>
  );
}
