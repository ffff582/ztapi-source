import { ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react';
import { useCallback, useState } from 'react';
import { apiClient } from '../../api/client';
import { DataContractError, parseRuntimeStatus } from '../../api/contracts';
import { formatAccountUSD } from './AccountBalance';
import { useAccountResource } from './useAccountResource';
import { localeTag, useLocale } from '../../i18n/locale';

const PAGE_SIZE = 20;
type Reservation = {
  id: number;
  request_id: string;
  model: string;
  status: 'reserved' | 'pending';
  reserved_quota: number;
  created_at: string;
};

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function parseReservations(value: unknown, after: number) {
  if (!record(value) || !Array.isArray(value.items) || value.items.length > PAGE_SIZE + 1) {
    throw new DataContractError();
  }
  let last = after;
  const items: Reservation[] = value.items.map((item: unknown) => {
    if (!record(item) || typeof item.id !== 'number' || !Number.isSafeInteger(item.id) || item.id <= last ||
      typeof item.request_id !== 'string' || item.request_id.trim() === '' || item.request_id.length > 128 ||
      typeof item.model !== 'string' || item.model.trim() === '' || item.model.length > 200 ||
      (item.status !== 'reserved' && item.status !== 'pending') ||
      typeof item.reserved_quota !== 'number' || !Number.isSafeInteger(item.reserved_quota) || item.reserved_quota < 0 ||
      typeof item.created_at !== 'string' || !Number.isFinite(Date.parse(item.created_at))) {
      throw new DataContractError();
    }
    last = item.id;
    return { id: item.id, request_id: item.request_id, model: item.model,
      status: item.status, reserved_quota: item.reserved_quota, created_at: item.created_at };
  });
  if (value.next_after_id !== last) throw new DataContractError();
  return { items: items.slice(0, PAGE_SIZE), hasMore: items.length > PAGE_SIZE };
}

export function PendingSettlements() {
  const { locale, t } = useLocale();
  const [cursors, setCursors] = useState([0]);
  const after = cursors[cursors.length - 1];
  const load = useCallback(async (signal: AbortSignal) => {
    const [response, runtime] = await Promise.all([
      apiClient.get<unknown>(`/user/self/pending-settlements?after_id=${after}&limit=${PAGE_SIZE + 1}`, { signal }),
      apiClient.get<unknown>('/status', { signal }),
    ]);
    const page = parseReservations(response, after);
    const divisor = parseRuntimeStatus(runtime).quota_per_unit;
    const items = page.items.map((item) => {
      const amount = item.reserved_quota / divisor;
      if (!Number.isFinite(amount)) throw new DataContractError();
      return { ...item, amount };
    });
    return { ...page, items };
  }, [after]);
  const reservations = useAccountResource(load);

  function refresh() {
    setCursors([0]);
    reservations.refresh();
  }

  function nextPage() {
    if (reservations.status !== 'ready' || !reservations.data.hasMore) return;
    // Advance from the last visible item, not the server's lookahead cursor.
    const last = reservations.data.items[PAGE_SIZE - 1].id;
    setCursors((previous) => [...previous, last]);
  }

  return (
    <section className="console-section wallet-history" aria-labelledby="pending-settlements-heading">
      <div className="console-section__heading">
        <div><h2 id="pending-settlements-heading">{t('待核账预留')}</h2><span>{t('尚未计费')}</span></div>
        <button className="console-icon-action" type="button" aria-label={t('刷新预留记录')} title={t('刷新预留记录')}
          disabled={reservations.status === 'loading'} onClick={refresh}>
          <RefreshCw aria-hidden="true" size={18} />
        </button>
      </div>
      <p className="console-field__help">{t('预留金额已从可用余额中暂时扣除，最终费用以结算结果为准。')}</p>
      {reservations.status === 'loading' && <div className="console-state" aria-busy="true">{t('正在读取预留记录...')}</div>}
      {reservations.status === 'error' && <div className="console-state console-state--error" role="status">{t('预留记录加载失败，请刷新重试。')}</div>}
      {reservations.status === 'ready' && (reservations.data.items.length === 0 ? (
        <div className="console-state">{t(after === 0 ? '暂无待核账预留。' : '本页暂无预留记录。')}</div>
      ) : (
        <div className="console-table-wrap" tabIndex={0} aria-label={t('请求预留列表')}>
          <table className="console-table wallet-history__table">
            <thead><tr><th scope="col">{t('请求 ID / 创建时间')}</th><th scope="col">{t('模型')}</th>
              <th scope="col">{t('预留金额 (USD)')}</th><th scope="col">{t('状态')}</th></tr></thead>
            <tbody>{reservations.data.items.map((item) => (
              <tr key={item.id}>
                <td style={{ overflowWrap: 'anywhere', whiteSpace: 'normal', maxWidth: 360 }}>
                  <code style={{ whiteSpace: 'normal' }}>{item.request_id}</code>
                  <small>{new Date(item.created_at).toLocaleString(localeTag(locale), { hour12: false })}</small>
                </td>
                <td data-label={t('模型')} style={{ overflowWrap: 'anywhere', whiteSpace: 'normal', maxWidth: 280 }}>{item.model}</td>
                <td data-label={t('预留金额 (USD)')}>{formatAccountUSD(item.amount)}</td>
                <td data-label={t('状态')}><span className="console-status console-status--info">
                  {t(item.status === 'reserved' ? '预留中' : '待核账')}
                </span></td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      ))}
      <nav className="console-pagination" aria-label={t('预留记录分页')}>
        <span>{t('第 {{page}} 页', { page: cursors.length })}</span>
        <button className="console-icon-action" type="button" aria-label={t('上一页预留记录')} title={t('上一页')}
          disabled={cursors.length === 1 || reservations.status === 'loading'}
          onClick={() => setCursors((previous) => previous.slice(0, -1))}>
          <ChevronLeft aria-hidden="true" size={18} />
        </button>
        <button className="console-icon-action" type="button" aria-label={t('下一页预留记录')} title={t('下一页')}
          disabled={reservations.status !== 'ready' || !reservations.data.hasMore} onClick={nextPage}>
          <ChevronRight aria-hidden="true" size={18} />
        </button>
      </nav>
    </section>
  );
}
