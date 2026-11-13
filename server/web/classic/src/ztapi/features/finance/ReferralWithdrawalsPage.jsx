// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
import React, { useEffect, useRef, useState } from 'react';
import { Check, ChevronLeft, ChevronRight, ClipboardCheck, FileCheck2, Hand, Pencil, RefreshCw, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import { referralAmount } from './ReferralCommissions.jsx';
import { referralReason, referralReasons } from './referral-reasons.js';
import './overview.css';
import './referrals.css';

const API = '/api/admin/referrals';
const states = {
  requested: '待报价', fee_quoted: '等待用户确认', confirmed: '用户已确认', paying: '付款任务已领取',
  submitted_unknown: '已提交，结果未知', paid: '已付款', rejected: '已拒绝', cancelled: '已撤销', manual_review: '人工复核',
};
const manualReviewNotice = '提现受退款影响，请先人工核对退款及剩余有效佣金；核对后可拒绝释放，不可报价或领取付款。';
const actions = {
  quote: { label: '手续费报价', submit: '提交报价', icon: Pencil },
  reject: { label: '拒绝', submit: '确认拒绝', icon: X },
  claim: { label: '独占领取', submit: '确认领取', icon: Hand },
  submitted: { label: '登记结果未知', submit: '保持占用并登记', icon: ClipboardCheck },
  'confirm-paid': { label: '服务端核验付款', submit: '请求只读核验', icon: FileCheck2 },
};
const micro = (value) => {
  if (typeof value !== 'string' || !/^(0|[1-9]\d{0,13})(\.\d{1,6})?$/.test(value)) return null;
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'));
};
const timeLabel = (value) => {
  if (!value) return '待核对';
  const date = new Date(typeof value === 'number' ? value * 1000 : value);
  return Number.isNaN(date.getTime()) ? '待核对' : date.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
};
const reasonList = (value) => Array.isArray(value) ? value.filter((v) => typeof v === 'string') : [];
const validVersion = (row) => Number.isSafeInteger(row.id) && row.id > 0 && Number.isSafeInteger(row.version) && row.version > 0;
function rowActions(row) {
  if (!validVersion(row)) return [];
  if (row.status === 'requested' || row.status === 'fee_quoted') return ['quote', 'reject'];
  if (row.status === 'confirmed') return ['quote', 'claim', 'reject'];
  if (row.status === 'manual_review') return ['reject'];
  if (row.status === 'paying' || row.status === 'submitted_unknown') return ['submitted', 'confirm-paid'];
  return [];
}

function ActionDialog({ target, busy, onClose, onSubmit }) {
  const [error, setError] = useState('');
  const dialog = useRef(null);
  const { row, action } = target;
  const spec = actions[action];
  useEffect(() => {
    const old = document.activeElement;
    dialog.current?.focus();
    return () => old?.focus?.();
  }, []);
  function onKey(event) {
    if (event.key === 'Escape' && !busy) onClose();
    if (event.key !== 'Tab') return;
    const items = dialog.current?.querySelectorAll('button:not(:disabled),input:not(:disabled),textarea:not(:disabled)');
    if (!items?.length) { event.preventDefault(); return; }
    const first = items[0], last = items[items.length - 1];
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }
  function submit(event) {
    event.preventDefault();
    if (busy) return;
    const form = new FormData(event.currentTarget);
    const payload = { version: row.version };
    if (action === 'quote') {
      const fee = String(form.get('fee') || '').trim();
      const feeMicros = micro(fee), amount = micro(row.amount);
      if (feeMicros === null || amount === null || feeMicros >= amount) { setError('手续费须为最多六位小数，且小于申请金额。'); return; }
      payload.fee = `${feeMicros / 1000000n}.${String(feeMicros % 1000000n).padStart(6, '0')}`;
    }
    if (['quote', 'reject', 'submitted'].includes(action)) payload.note = String(form.get('note') || '').trim();
    if (action === 'quote' && payload.fee === '0.000000' && !payload.note) { setError('零手续费须填写审核依据。'); return; }
    if (action === 'reject' && !payload.note) { setError('请填写拒绝理由。'); return; }
    if (action === 'submitted' || action === 'confirm-paid') {
      payload.tx_id = String(form.get('tx_id') || '').trim();
      if (!/^[a-fA-F0-9]{64}$/.test(payload.tx_id)) { setError('请填写64位交易哈希；付款状态仍以服务端核验为准。'); return; }
    }
    onSubmit(payload);
  }
  return (
    <div className='finance-overlay'>
      <div className='finance-dialog referral-dialog' role='dialog' aria-modal='true' aria-label={`${spec.label} #${row.id}`} tabIndex={-1} ref={dialog} onKeyDown={onKey}>
        <header><h2>{spec.label} #{row.id}</h2><button title='关闭' aria-label='关闭操作' disabled={busy} onClick={onClose}><X size={18} /></button></header>
        <dl className='referral-amounts'>
          <div><dt>申请金额</dt><dd>{referralAmount(row.amount)}</dd></div>
          <div><dt>原手续费</dt><dd>{referralAmount(row.fee)}</dd></div>
          <div><dt>原净到账</dt><dd>{referralAmount(row.net_amount)}</dd></div>
          <div><dt>原收款钱包</dt><dd>{row.address || '待核对'}</dd></div>
        </dl>
        {action === 'quote' && <p className='finance-muted'>报价后等待用户确认；手续费变化须重新确认。</p>}
        {action === 'claim' && <p className='finance-status'>独占领取后只能由领取管理员登记付款；此操作不会发起转账。</p>}
        {row.status === 'manual_review' && action === 'reject' && <p className='finance-status' role='note'>{manualReviewNotice}</p>}
        {(action === 'submitted' || action === 'confirm-paid') && <p className='finance-status'>交易哈希不代表付款成功。服务端核验网络、合约、收款人、金额与具体转账身份；核验未通过时保持占用。</p>}
        <form onSubmit={submit}>
          <fieldset disabled={busy}>
            {action === 'quote' && <label>手续费（USDT）<input name='fee' inputMode='decimal' autoComplete='off' maxLength={24} required /></label>}
            {['submitted', 'confirm-paid'].includes(action) && <label>交易哈希<input name='tx_id' maxLength={64} required autoComplete='off' defaultValue={row.tx_id || ''} /></label>}
            {['quote', 'reject', 'submitted'].includes(action) && <label>{action === 'reject' ? '拒绝理由' : '审核备注'}<textarea name='note' maxLength={500} required={action === 'reject'} /></label>}
          </fieldset>
          {error && <p role='alert' className='finance-error'>{error}</p>}
          <footer><button type='button' disabled={busy} onClick={onClose}>取消</button><button type='submit' disabled={busy}><Check size={16} />{busy ? '处理中' : spec.submit}</button></footer>
        </form>
      </div>
    </div>
  );
}

export default function ReferralWithdrawalsPage({ canWrite = false, view = 'withdrawals' }) {
  const [data, setData] = useState(null), [readiness, setReadiness] = useState(null);
  const [status, setStatus] = useState(''), [page, setPage] = useState(1), [pageSize, setPageSize] = useState(20), [revision, setRevision] = useState(0);
  const [state, setState] = useState('loading'), [notice, setNotice] = useState(''), [target, setTarget] = useState(null), [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const isWithdrawals = view === 'withdrawals';
  useEffect(() => {
    let active = true;
    setData(null); setReadiness(null); setTarget(null); setState('loading');
    adminRequest({ url: `${API}?status=${encodeURIComponent(status)}&page=${page}&page_size=${pageSize}` })
      .then((result) => {
        const list = isWithdrawals ? result?.withdrawals : result?.commissions;
        if (!Array.isArray(list?.items) || !Number.isSafeInteger(list.total) || list.total < 0) throw new Error('invalid_referral_list');
        if (active) { setData(result); setState('ready'); }
      }).catch(() => { if (active) setState('error'); });
    adminRequest({ url: `${API}/readiness` }).then((result) => { if (active) setReadiness(result); }).catch(() => { if (active) setReadiness({ unavailable: true }); });
    return () => { active = false; };
  }, [status, page, pageSize, revision, isWithdrawals]);
  useEffect(() => { if (!canWrite) setTarget(null); }, [canWrite]);
  const refresh = () => setRevision((v) => v + 1);
  async function mutate(payload) {
    if (!canWrite || !target || lock.current) return;
    lock.current = true; setBusy(true); setNotice('');
    try {
      await adminRequest({ url: `${API}/withdrawals/${target.row.id}/${target.action}`, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      setNotice('操作已提交，请以刷新后的服务端状态为准。');
    } catch (error) {
      setNotice(error?.status === 409 ? '状态或版本冲突，已刷新；付款未确认时保持占用，请核对最新记录。' : '操作未能确认，已刷新；不要重复转账，请核对服务端记录。');
    } finally {
      setTarget(null); setData(null); setBusy(false); lock.current = false; refresh();
    }
  }
  const list = data ? (isWithdrawals ? data.withdrawals : data.commissions) : null;
  const campaign = data?.campaign;
  const checks = readiness?.readiness ?? readiness;
  const worker = typeof readiness?.worker === 'object' && readiness.worker !== null ? readiness.worker : null;
  const blocked = referralReasons([...reasonList(campaign?.blocked_reasons), ...reasonList(checks?.blocked_reasons), ...reasonList(checks?.price_gate?.blocked_reasons)]);
  const readinessValues = {
    ready: checks?.ready,
    schema: checks?.schema_ready ?? checks?.schema,
    worker: checks?.worker_ready ?? checks?.worker,
    finance: checks?.finance_ready ?? checks?.finance,
    pricing: checks?.price_gate?.allowed ?? checks?.pricing,
    fees: checks?.platform_fee_known ?? checks?.fees,
  };
  return (
    <section className='ztapi-admin-page finance-overview referral-page' aria-label={isWithdrawals ? '提现审核' : '推广返利'}>
      <header className='finance-toolbar'><h1>{isWithdrawals ? '提现审核' : '推广返利'}</h1><button title='刷新推广数据' aria-label='刷新推广数据' disabled={busy} onClick={refresh}><RefreshCw size={17} /></button></header>
      <section aria-label='活动与准入' className='referral-readiness'>
        <h2>活动与准入</h2>
        <div className='finance-status'>
          <strong>{campaign?.enabled === true ? '活动已启用' : campaign?.enabled === false ? '活动已关闭' : '活动状态未知'}</strong>
          <span>{campaign?.accepting_invites === true ? '接受新邀请' : campaign?.accepting_invites === false ? '不接受新邀请' : '邀请准入未知'}</span>
        </div>
        <ul className='referral-blocked'>{blocked.map(([zh, en]) => <li key={zh}><span>{zh}</span><small lang='en'>{en}</small></li>)}</ul>
        <dl className='finance-secondary'>
          {['ready', 'schema', 'worker', 'finance', 'pricing', 'fees'].map((key) => <div key={key}><dt>{({ready: '总体准入', schema: '账本结构', worker: '归集任务', finance: '财务能力', pricing: '报价准入', fees: '费用准入'})[key]}</dt><dd>{readinessValues[key] === true ? '通过' : readinessValues[key] === false ? '未通过' : '未知'}</dd></div>)}
        </dl>
        {worker && <dl className='finance-secondary'>
          <div><dt>任务开关</dt><dd>{worker.enabled === true ? '已启用' : worker.enabled === false ? '未启用' : '未知'}</dd></div>
          <div><dt>任务进程</dt><dd>{worker.running === true ? '运行中' : worker.running === false ? '未运行' : '未知'}</dd></div>
          <div><dt>最近心跳</dt><dd>{timeLabel(worker.last_heartbeat)}</dd></div>
          <div><dt>最近成功</dt><dd>{timeLabel(worker.last_success)}</dd></div>
          <div className='referral-worker-error'><dt>最近异常</dt><dd>{typeof worker.last_error === 'string' && worker.last_error ? <><span>{referralReason(worker.last_error)[0]}</span><small lang='en'>{referralReason(worker.last_error)[1]}</small></> : '未提供异常记录'}</dd></div>
        </dl>}
        {readiness?.unavailable && <p className='finance-muted'>准入数据读取失败，状态未知。</p>}
      </section>
      {notice && <p className='finance-status' role='alert'>{notice}</p>}
      {data && <section aria-label='推广汇总' className='referral-admin-summary'>
        <h2>推广汇总</h2>
        <dl className='finance-secondary'>
          {[
            ['累计佣金', 'total_commission'], ['累计冲回', 'reversed_commission'],
            ['冻结佣金', 'frozen_commission'], ['可提佣金', 'available_commission'],
            ['提现占用', 'withdrawal_reserved'], ['已付佣金', 'paid_commission'],
            ['退款未追回损失', 'manual_loss'],
          ].map(([label, key]) => <div key={key}><dt>{label}</dt><dd>{referralAmount(data.summary?.[key])}</dd></div>)}
        </dl>
      </section>}
      <div className='finance-toolbar referral-filters'>
        <label>{isWithdrawals ? '提现状态' : '佣金状态'}<select aria-label={isWithdrawals ? '提现状态' : '佣金状态'} value={status} disabled={busy} onChange={(e) => { setStatus(e.target.value); setPage(1); setNotice(''); }}><option value=''>全部</option>{Object.entries(isWithdrawals ? states : { frozen: '冻结中', available: '已解冻', reversed: '已冲回' }).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
        <label>每页<select value={pageSize} disabled={busy} onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1); }}><option value={20}>20</option><option value={50}>50</option></select></label>
      </div>
      {state === 'loading' && <p role='status'>正在读取推广记录...</p>}
      {state === 'error' && <p role='alert' className='finance-error'>推广数据加载失败。<button onClick={refresh}>重试</button></p>}
      {list && <>
        <div className='finance-table-wrap'><table aria-label={isWithdrawals ? '提现记录' : '佣金记录'}>
          <thead><tr>{(isWithdrawals ? ['编号 / 版本', '申请额', '预扣手续费', '净到账', '原收款钱包', '状态', '申请 / 报价到期', '交易哈希 / 备注', ...(canWrite ? ['审核'] : [])] : ['编号', '受邀用户', '实收本金', '佣金 / 冲回', '产生 / 解冻', '状态']).map((label) => <th key={label} scope='col'>{label}</th>)}</tr></thead>
          <tbody>{list.items.map((row) => isWithdrawals ? <tr key={row.id}>
            <td>#{row.id}<small>v{row.version ?? '未知'}</small></td><td>{referralAmount(row.amount)}</td><td>{referralAmount(row.fee)}</td><td>{referralAmount(row.net_amount)}</td><td className='referral-identity'>{row.address || '待核对'}</td><td>{states[row.status] || `未知状态：${row.status || '-'}`}{row.status === 'manual_review' && <small role='note'>{manualReviewNotice}</small>}</td><td>{timeLabel(row.created_at)}<small>{timeLabel(row.fee_expires_at)}</small></td><td className='referral-identity'>{row.tx_id || '—'}<small>{row.note || '—'}</small></td>
            {canWrite && <td><div className='referral-row-actions'>{rowActions(row).map((action) => { const Icon = actions[action].icon; return <button key={action} title={`${actions[action].label} #${row.id}`} aria-label={`${actions[action].label} #${row.id}`} disabled={busy} onClick={() => { setNotice(''); setTarget({ row, action }); }}><Icon size={17} /></button>; })}</div></td>}
          </tr> : <tr key={row.id}><td>#{row.id}</td><td>{row.invitee_label || '—'}</td><td>{referralAmount(row.principal)}</td><td>{referralAmount(row.commission)}<small>{referralAmount(row.reversed)}</small></td><td>{timeLabel(row.earned_at)}<small>{timeLabel(row.available_at)}</small></td><td>{row.status || '未知'}</td></tr>)}
          {!list.items.length && <tr><td colSpan={isWithdrawals ? (canWrite ? 9 : 8) : 6}>{isWithdrawals ? '暂无提现记录' : '暂无佣金记录'}</td></tr>}</tbody>
        </table></div>
        <div className='finance-pager'><span>{list.total} 条 · 第 {page} / {Math.max(1, Math.ceil(list.total / pageSize))} 页</span><button title='上一页' aria-label='上一页' disabled={busy || page <= 1} onClick={() => setPage((n) => n - 1)}><ChevronLeft size={17} /></button><button title='下一页' aria-label='下一页' disabled={busy || page * pageSize >= list.total} onClick={() => setPage((n) => n + 1)}><ChevronRight size={17} /></button></div>
      </>}
      {target && canWrite && <ActionDialog target={target} busy={busy} onClose={() => setTarget(null)} onSubmit={mutate} />}
    </section>
  );
}
