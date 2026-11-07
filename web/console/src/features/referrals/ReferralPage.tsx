import { useCallback, useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react';
import { Check, ChevronLeft, ChevronRight, Copy, RefreshCw, ShieldCheck } from 'lucide-react';
import { getAuthSessionLifecycle, subscribeAuthSession } from '../../api/client';
import type { ReferralCommission, ReferralOverview, ReferralPageData, ReferralWithdrawalInput } from '../../api/contracts';
import { ReferralError, referralsApi } from '../../api/referrals';
import { localeTag, useLocale } from '../../i18n/locale';
import { WithdrawalForm } from './WithdrawalForm';
import { WithdrawalRecords } from './WithdrawalRecords';
import './referrals.css';

type Resource<T> = { status: 'loading'; data: null } | { status: 'error'; data: null } | { status: 'ready'; data: T };
type Read<T> = (signal: AbortSignal) => Promise<T>;

function useResource<T>(load: Read<T>, revision: number): Resource<T> {
  const [snapshot, setSnapshot] = useState<{ load: Read<T>; revision: number; state: Resource<T> } | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal).then(
      (data) => { if (!controller.signal.aborted) setSnapshot({ load, revision, state: { status: 'ready', data } }); },
      () => { if (!controller.signal.aborted) setSnapshot({ load, revision, state: { status: 'error', data: null } }); },
    );
    return () => controller.abort();
  }, [load, revision]);
  return snapshot?.load === load && snapshot.revision === revision ? snapshot.state : { status: 'loading', data: null };
}

export type MutationOutcome = 'success' | 'conflict' | 'rejected' | 'unknown';

export function ReferralPage() {
  const identity = useSyncExternalStore(subscribeAuthSession, getAuthSessionLifecycle);
  return <ReferralWorkspace key={identity} />;
}

function ReferralWorkspace() {
  const { t, locale } = useLocale();
  const [revision, setRevision] = useState(0);
  const [commissionPage, setCommissionPage] = useState(1);
  const [withdrawalPage, setWithdrawalPage] = useState(1);
  const [busy, setBusy] = useState(false);
  const mutationLock = useRef(false);
  const alive = useRef(true);
  const [notice, setNotice] = useState<{ key: string; error?: boolean } | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  const readSelf = useCallback((signal: AbortSignal) => referralsApi.self(signal), []);
  const readCommissions = useCallback((signal: AbortSignal) => referralsApi.commissions(commissionPage, signal), [commissionPage]);
  const readWithdrawals = useCallback((signal: AbortSignal) => referralsApi.withdrawals(withdrawalPage, signal), [withdrawalPage]);
  const overview = useResource(readSelf, revision);
  const commissions = useResource(readCommissions, revision);
  const withdrawals = useResource(readWithdrawals, revision);
  const data = overview.data;
  const open = !!data?.campaign.enabled && data.campaign.blocked_reasons.length === 0;

  function refresh() { setRevision((value) => value + 1); }

  async function mutate(action: () => Promise<unknown>, successKey: string): Promise<MutationOutcome> {
    if (mutationLock.current) return 'rejected';
    mutationLock.current = true;
    setBusy(true);
    setNotice(null);
    let outcome: MutationOutcome = 'unknown';
    try {
      await action();
      outcome = 'success';
      if (alive.current) setNotice({ key: successKey });
    } catch (error) {
      if (error instanceof ReferralError && error.status === 409) {
        outcome = 'conflict';
        if (alive.current) setNotice({ key: '状态或报价已更新，已重新读取服务器数据，请核对后再操作。', error: true });
      } else if (error instanceof ReferralError && error.status >= 400 && error.status < 500) {
        outcome = 'rejected';
        const key = error.status === 401 ? '登录验证已失效，请重新登录后再操作。'
          : ['reauthentication_required', 'password_required', 'referral_reauthentication_required'].includes(error.code) ? '提现需要重新验证，请先在账号安全设置密码。'
          : ['totp_required', 'invalid_totp', 'referral_totp_required'].includes(error.code) ? '请填写有效的二次验证码后重新验证。'
          : error.code === 'invalid_password' ? '当前账号密码验证失败，请重新验证。'
          : '提现操作未通过，请核对金额、地址及身份验证后重试。';
        if (alive.current) setNotice({ key, error: true });
      } else if (alive.current) {
        setNotice({ key: '请求结果尚未确认，请先刷新记录核对，不要重复发起新的提现。', error: true });
      }
    } finally {
      if (alive.current) {
        setBusy(false);
        refresh();
      }
      mutationLock.current = false;
    }
    return outcome;
  }

  const apply = (input: ReferralWithdrawalInput) => mutate(() => referralsApi.request(input), '提现申请已记录，等待人工手续费报价。');

  return (
    <div className="console-page referral-page">
      <header className="referral-heading">
        <div><h1>{t('推广返利')}</h1><p className="referral-muted">USDT / TRC20</p></div>
        <button className="referral-icon" type="button" title={t('刷新推广数据')} aria-label={t('刷新推广数据')} onClick={refresh} disabled={busy || overview.status === 'loading'}><RefreshCw size={18} aria-hidden="true" /></button>
      </header>
      {notice && <p className={notice.error ? 'referral-notice referral-error' : 'referral-notice'} role={notice.error ? 'alert' : 'status'}>{t(notice.key)}{notice.key.includes('登录') && <> <a href="/login?redirect=%2Fconsole%2Freferrals">{t('重新登录')}</a></>}</p>}
      {overview.status === 'loading' && <p role="status" aria-busy="true">{t('正在读取推广数据...')}</p>}
      {overview.status === 'error' && <p className="referral-error" role="alert">{t('推广数据加载失败，请重试。')}</p>}
      {data && <>
        <section className="referral-section" aria-labelledby="referral-campaign-heading">
          <h2 id="referral-campaign-heading">{t('邀请推广')}</h2>
          <p className={`referral-campaign-status${!open ? ' referral-warning' : ''}`} role="status">
            {!open ? t('推广返利暂未开放') : data.campaign.accepting_invites ? t('接受新的邀请绑定') : t('已停止新的邀请绑定')}
          </p>
          {!open && <p className="referral-muted">{t('当前不接受新的参与申请；已有佣金和提现记录仍以服务器账本为准。')}</p>}
          {data.campaign.blocked_reasons.length > 0 && <ul className="referral-blocks">{data.campaign.blocked_reasons.map((reason, index) => <li key={index}>{t(reason)}</li>)}</ul>}
          <p>{t('规则：真实到账充值本金的 5%，绑定后 12 个月内有效，每笔冻结 7 天。')}</p>
          <p className="referral-muted">{t('20 USDT 起提，仅支持 TRC20；网络费由提现人承担，先报价、确认后人工打款。')}</p>
          <p className="referral-muted">{t('调用额度和赠送额度不计佣，也不能兑换现金。未开放期间不承诺新邀请开始计佣。')}</p>
          <Invitation data={data} enabled={open && data.campaign.accepting_invites} />
        </section>
        <section className="referral-section" aria-labelledby="referral-summary-heading">
          <h2 id="referral-summary-heading">{t('佣金概览')}</h2>
          <dl className="referral-metrics">
            {([
              ['佣金总额', data.summary.total_commission], ['冻结佣金', data.summary.frozen_commission],
              ['可提现佣金', data.summary.available_commission], ['提现占用', data.summary.withdrawal_reserved],
              ['累计已付', data.summary.paid_commission],
            ] as const).map(([label, amount]) => <div key={label}><dt>{t(label)} <small>USDT</small></dt><dd>{amount}</dd></div>)}
          </dl>
          <p className="referral-muted">{t('已邀请 {{count}} 人', { count: data.summary.invited_count })}</p>
          <p className="referral-muted">{t('服务器时间')} <time dateTime={data.server_time}>{new Date(data.server_time).toLocaleString(localeTag(locale), { hour12: false })}</time></p>
        </section>
      </>}
      <section className="referral-section" aria-labelledby="referral-commissions-heading">
        <h2 id="referral-commissions-heading">{t('充值佣金明细')}</h2>
        <ResourceState resource={commissions} empty="暂无充值佣金明细。" error="佣金明细加载失败，请重试。" retry={refresh}>
          {(value) => <CommissionTable data={value} />}
        </ResourceState>
        <Pagination page={commissionPage} total={commissions.data?.total} loading={commissions.status !== 'ready'} onChange={setCommissionPage} kind="佣金明细" />
      </section>
      <section className="referral-section" aria-labelledby="referral-request-heading">
        <h2 id="referral-request-heading"><ShieldCheck size={17} aria-hidden="true" />{t('申请提现')}</h2>
        <WithdrawalForm data={data} enabled={data !== null} busy={busy} submit={apply} />
      </section>
      <section className="referral-section" aria-labelledby="referral-withdrawals-heading">
        <h2 id="referral-withdrawals-heading">{t('提现记录')}</h2>
        <ResourceState resource={withdrawals} empty="暂无提现记录。" error="提现记录加载失败，请重试。" retry={refresh}>
          {(value) => <WithdrawalRecords items={value.items} serverTime={data?.server_time ?? null} busy={busy || !data} onConfirm={(item) => mutate(() => referralsApi.confirm(item.id, item.version), '手续费报价已确认，等待人工打款。')} onCancel={(item) => mutate(() => referralsApi.cancel(item.id, item.version), '提现已取消，资金状态以服务器记录为准。')} />}
        </ResourceState>
        <Pagination page={withdrawalPage} total={withdrawals.data?.total} loading={withdrawals.status !== 'ready' || busy} onChange={setWithdrawalPage} kind="提现记录" />
      </section>
    </div>
  );
}

function Invitation({ data, enabled }: { data: ReferralOverview; enabled: boolean }) {
  const { t } = useLocale();
  const [result, setResult] = useState<'copied' | 'error' | null>(null);
  let url = '';
  try {
    const candidate = new URL(data.invite_url, window.location.origin);
    if ([window.location.origin, 'https://ztapi.vip'].includes(candidate.origin) && !candidate.username && !candidate.password && candidate.pathname === '/register' && candidate.searchParams.getAll('invite').length === 1 && candidate.searchParams.get('invite') === data.invite_code && data.invite_code !== '') url = candidate.href;
  } catch { /* Invalid server links cannot be shared. */ }
  async function copy() {
    try { await navigator.clipboard.writeText(url); setResult('copied'); }
    catch { setResult('error'); }
  }
  return <div className="referral-invitation">
    <label htmlFor="referral-invite-url">{t('邀请链接')}</label>
    <div className="referral-invite-row"><input id="referral-invite-url" readOnly value={url} aria-describedby="referral-invite-result" /><button type="button" className="referral-icon" aria-label={t('复制邀请链接')} title={t('复制邀请链接')} disabled={!enabled || !url} onClick={() => void copy()}>{result === 'copied' ? <Check size={17} aria-hidden="true" /> : <Copy size={17} aria-hidden="true" />}</button></div>
    <span id="referral-invite-result" className="referral-muted" role="status">{result === 'copied' ? t('邀请链接已复制') : result === 'error' ? t('复制失败，请手动选择并复制。') : !url ? t('邀请链接暂不可用') : ''}</span>
  </div>;
}

function ResourceState<T extends { items: unknown[] }>({ resource, empty, error, retry, children }: { resource: Resource<T>; empty: string; error: string; retry: () => void; children: (data: T) => ReactNode }) {
  const { t } = useLocale();
  if (resource.status === 'loading') return <p className="referral-muted" role="status" aria-busy="true">{t('正在读取记录...')}</p>;
  if (resource.status === 'error') return <div className="referral-retry"><p role="alert" className="referral-error">{t(error)}</p><button type="button" className="referral-icon" title={t('重试读取记录')} aria-label={t('重试读取记录')} onClick={retry}><RefreshCw size={17} aria-hidden="true" /></button></div>;
  if (resource.data.items.length === 0) return <p className="referral-muted">{t(empty)}</p>;
  return children(resource.data);
}

function CommissionTable({ data }: { data: ReferralPageData<ReferralCommission> }) {
  const { t, locale } = useLocale();
  const headings = ['受邀用户', '充值本金 (USDT)', '佣金 (USDT)', '已冲回 (USDT)', '解冻时间', '状态'];
  const states: Record<string, string> = { frozen: '冻结中', available: '可提现', reversed: '已冲回', partially_reversed: '部分冲回' };
  return <table className="referral-table" aria-label={t('充值佣金明细')}><thead><tr>{headings.map((heading) => <th key={heading} scope="col">{t(heading)}</th>)}</tr></thead><tbody>{data.items.map((item) => <tr key={item.id}>
    <td data-label={t(headings[0])}><span>{item.invitee_label}<small>{new Date(item.earned_at).toLocaleString(localeTag(locale), { hour12: false })}</small></span></td>
    <td data-label={t(headings[1])}><span>{item.principal}</span></td><td data-label={t(headings[2])}><span>{item.commission}</span></td><td data-label={t(headings[3])}><span>{item.reversed}</span></td>
    <td data-label={t(headings[4])}><time dateTime={item.available_at}>{new Date(item.available_at).toLocaleString(localeTag(locale), { hour12: false })}</time></td><td data-label={t(headings[5])}><span>{t(states[item.status] ?? '状态待核验')}</span></td>
  </tr>)}</tbody></table>;
}

function Pagination({ page, total, loading, onChange, kind }: { page: number; total?: number; loading: boolean; onChange: (page: number) => void; kind: string }) {
  const { t } = useLocale();
  return <nav className="referral-pagination" aria-label={t(`${kind}分页`)}>
    <span>{total === undefined ? t('第 {{page}} 页', { page }) : t('第 {{page}} 页 · 共 {{total}} 条', { page, total })}</span>
    <button className="referral-icon" type="button" title={t(`上一页${kind}`)} aria-label={t(`上一页${kind}`)} disabled={page <= 1 || loading} onClick={() => onChange(page - 1)}><ChevronLeft size={17} aria-hidden="true" /></button>
    <button className="referral-icon" type="button" title={t(`下一页${kind}`)} aria-label={t(`下一页${kind}`)} disabled={loading || total === undefined || page * 20 >= total} onClick={() => onChange(page + 1)}><ChevronRight size={17} aria-hidden="true" /></button>
  </nav>;
}
