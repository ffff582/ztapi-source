import { Check, Circle } from 'lucide-react';
import type { SpendingAlert } from '../../api/spending';
import { localeTag, useLocale } from '../../i18n/locale';
import { useSpendingInbox } from './useSpendingInbox';
import { SpendingReauthenticate } from './SpendingReauthenticate';

const titles: Record<SpendingAlert['kind'], string> = { daily_spend: '今日消费达到阈值', hourly_spend: '近 1 小时消费达到阈值', low_balance: '可用余额低于阈值', large_input: '单次普通输入 Token 达到阈值' };
export function SpendingInbox({ showSessionNotice = true }: { showSessionNotice?: boolean }) {
  const { t, locale } = useLocale();
  const inbox = useSpendingInbox();
  return <div className="spending-inbox" aria-live="polite" aria-busy={inbox.status === 'loading'}>
    {inbox.status === 'loading' && <p>{t('正在读取通知...')}</p>}
    {inbox.status === 'idle' && <p>{t('请登录后查看消费通知。')}</p>}
    {inbox.status === 'error' && <p role="status">{t('通知暂不可用，请稍后重试。')}</p>}
    {inbox.status === 'session-expired' && showSessionNotice && <SpendingReauthenticate />}
    {inbox.status === 'ready' && <p className="spending-muted">{t('未读 {{count}} 条', { count: inbox.data?.unread_count ?? 0 })}</p>}
    {inbox.data?.items.length === 0 && <p>{t('暂无消费通知')}</p>}
    {inbox.readError && <p role="alert" className="spending-error">{t('未能标记已读，请重试。')}</p>}
    {inbox.data && <ol className="spending-records">
      {inbox.data.items.slice(0, 20).map((item) => <li key={item.id}>
        <button type="button" className="spending-record" data-unread={String(!item.read_at)} onClick={() => void inbox.read(item.id)} disabled={inbox.reading.includes(item.id)}>
          <span className="spending-record__icon" aria-label={t(item.read_at ? '已读' : '未读')}>{item.read_at ? <Check size={16} aria-hidden="true" /> : <Circle size={12} aria-hidden="true" />}</span>
          <span className="spending-record__body">
            <strong>{t(titles[item.kind])}</strong>
            <span>{t('记录值')} {item.measured_value} {item.unit} <span aria-hidden="true">/</span> {t('阈值')} {item.threshold} {item.unit}</span>
            <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString(localeTag(locale), { timeZone: 'Asia/Shanghai', hour12: false })}</time>
            {(item.request_id || item.model) && <span className="spending-record__metadata">{item.model} {item.request_id}</span>}
            <span className="spending-muted">{t(deliveryLabel(item.delivery_status))}</span>
          </span>
        </button>
      </li>)}
    </ol>}
  </div>;
}
function deliveryLabel(status: string) {
  if (status === 'sent' || status === 'delivered') return 'Telegram 已发送';
  if (status === 'pending') return 'Telegram 等待发送';
  if (status === 'failed' || status === 'blocked') return 'Telegram 发送失败';
  if (status === 'unknown') return 'Telegram 发送结果未知';
  return '站内通知';
}
