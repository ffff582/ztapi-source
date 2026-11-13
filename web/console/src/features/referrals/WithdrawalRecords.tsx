import { useState } from 'react';
import { Check, X } from 'lucide-react';
import { referralMoneyMicros, type ReferralWithdrawal, type ReferralWithdrawalStatus } from '../../api/contracts';
import { localeTag, useLocale } from '../../i18n/locale';

const statusLabels: Record<ReferralWithdrawalStatus, string> = {
  requested: '待手续费报价', fee_quoted: '待确认手续费', confirmed: '已确认报价，待人工打款', paying: '人工打款处理中',
  submitted_unknown: '打款结果待核验，资金继续占用', paid: '已付款', rejected: '已拒绝', cancelled: '已取消', manual_review: '待人工核验',
};

export function WithdrawalRecords({ items, serverTime, busy, onConfirm, onCancel }: { items: ReferralWithdrawal[]; serverTime: string | null; busy: boolean; onConfirm: (item: ReferralWithdrawal) => Promise<unknown>; onCancel: (item: ReferralWithdrawal) => Promise<unknown> }) {
  return <ul className="referral-withdrawals">{items.map((item) => <WithdrawalRecord key={`${item.id}:${item.version}`} item={item} serverTime={serverTime} busy={busy} onConfirm={onConfirm} onCancel={onCancel} />)}</ul>;
}

function WithdrawalRecord({ item, serverTime, busy, onConfirm, onCancel }: { item: ReferralWithdrawal; serverTime: string | null; busy: boolean; onConfirm: (item: ReferralWithdrawal) => Promise<unknown>; onCancel: (item: ReferralWithdrawal) => Promise<unknown> }) {
  const { t, locale } = useLocale();
  const [acknowledged, setAcknowledged] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const quoted = item.status === 'fee_quoted';
  const hasQuote = !['requested', 'rejected', 'cancelled'].includes(item.status) && item.fee !== null && item.net_amount !== null && referralMoneyMicros(item.net_amount) > 0n;
  const expired = !serverTime || !item.fee_expires_at || Date.parse(item.fee_expires_at) <= Date.parse(serverTime);
  const canCancel = ['requested', 'fee_quoted', 'confirmed', 'manual_review'].includes(item.status);
  return <li>
    <div className="referral-record-heading"><strong>#{item.id}</strong><span className={`referral-status referral-status--${item.status}`}>{t(statusLabels[item.status])}</span><time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString(localeTag(locale), { hour12: false })}</time></div>
    <dl className="referral-record-amounts">
      <div><dt>{t('申请金额 (USDT)')}</dt><dd>{item.amount}</dd></div>
      <div><dt>{t('手续费 (USDT)')}</dt><dd>{hasQuote ? item.fee : t('待报价')}</dd></div>
      <div><dt>{t('净到账 (USDT)')}</dt><dd>{hasQuote ? item.net_amount : t('待报价')}</dd></div>
    </dl>
    <dl className="referral-record-details"><div><dt>{t('TRC20 收款地址')}</dt><dd><code>{item.address}</code></dd></div>{item.tx_id && <div><dt>{t('链上交易')}</dt><dd><code>{item.tx_id}</code></dd></div>}{item.note && <div><dt>{t('备注')}</dt><dd>{item.note}</dd></div>}</dl>
    {item.status === 'manual_review' && <p className="referral-warning" role="note">{locale === 'en'
      ? 'This withdrawal is affected by a refund. Reconcile the refund and remaining valid commission before cancelling. The server determines the amount released.'
      : '该提现受退款影响，请先人工核对退款及剩余有效佣金，再取消提现。可释放金额由服务器核算。'}</p>}
    {quoted && <>
      <p className="referral-muted">{t('报价有效期至')} <time dateTime={item.fee_expires_at ?? ''}>{item.fee_expires_at ? new Date(item.fee_expires_at).toLocaleString(localeTag(locale), { hour12: false }) : t('待核验')}</time></p>
      {expired && <p className="referral-warning">{t('手续费报价已过期，请刷新等待重新报价。')}</p>}
      <label className="referral-acknowledgement"><input type="checkbox" disabled={busy || expired} checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />{t('确认手续费 {{fee}} USDT，净到账 {{net}} USDT，由管理员人工打款。', { fee: item.fee ?? '', net: item.net_amount ?? '' })}</label>
    </>}
    <div className="referral-actions">
      {quoted && <button type="button" className="referral-command" disabled={busy || expired || !acknowledged} onClick={() => void onConfirm(item)}><Check size={16} aria-hidden="true" />{t('确认手续费报价')}</button>}
      {canCancel && !cancelling && <button type="button" className="referral-command referral-command--secondary" disabled={busy} onClick={() => setCancelling(true)}><X size={16} aria-hidden="true" />{t('取消提现')}</button>}
      {canCancel && cancelling && <><button type="button" className="referral-command referral-command--secondary" disabled={busy} onClick={() => void onCancel(item)}><Check size={16} aria-hidden="true" />{t('确认取消提现')}</button><button type="button" className="referral-icon" title={t('保留提现')} aria-label={t('保留提现')} disabled={busy} onClick={() => setCancelling(false)}><X size={16} aria-hidden="true" /></button></>}
    </div>
  </li>;
}
