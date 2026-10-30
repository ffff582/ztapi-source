import React, { useEffect, useRef, useState } from 'react';
import { Check, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

export default function DirectCostDialog({ row, onClose, onSaved }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [coverage, setCoverage] = useState(row?.coverage || 'unknown');
  const [currency, setCurrency] = useState(row?.currency || 'XCLOUD_CREDIT');
  const key = useRef(crypto.randomUUID());
  const lock = useRef(false);
  const dialog = useRef(null);
  useEffect(() => { const previous = document.activeElement; dialog.current?.focus(); return () => previous?.focus?.(); }, []);
  function onKey(e) {
    if (e.key === 'Escape' && !busy) onClose();
    if (e.key !== 'Tab') return;
    const controls = dialog.current?.querySelectorAll('button:not(:disabled),input:not(:disabled),select:not(:disabled)');
    if (!controls?.length) return;
    const first = controls[0], last = controls[controls.length - 1];
    if (e.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  }
  async function submit(e) {
    e.preventDefault();
    if (lock.current) return;
    const f = new FormData(e.currentTarget);
    const get = (name) => String(f.get(name) || '').trim();
    const count = (name) => get(name) === '' ? null : Number(get(name));
    const localTime = get('occurred_at');
    const value = {
      supplier: get('supplier'), account: get('account'), bill_id: get('bill_id'),
      verification_id: Number(get('verification_id')), channel_id: Number(get('channel_id')),
      model: get('model'), input_tokens: get('input_tokens') === '' ? null : Number(get('input_tokens')),
      output_tokens: get('output_tokens') === '' ? null : Number(get('output_tokens')),
      input_token_scope: get('input_token_scope'), cache_read: count('cache_read'),
      cache_write_5m: count('cache_write_5m'), cache_write_1h: count('cache_write_1h'),
      occurred_at: `${localTime.length === 16 ? localTime + ':00' : localTime}+08:00`,
      amount: get('amount'), currency, rate: get('rate'), rate_evidence: get('rate_evidence'),
      evidence: get('evidence'), link_evidence: get('link_evidence'), coverage,
      coverage_evidence: coverage === 'unknown' ? '' : get('coverage_evidence'),
      month_cost_fact_id: coverage === 'unknown' ? 0 : Number(get('month_cost_fact_id')),
    };
    lock.current = true; setBusy(true); setError('');
    try {
      await adminRequest({ url: '/api/admin/finance-reporting/facts', method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({kind:'direct_cost', direct_cost:value, previous_id:row?.fact_id || 0, idempotency_key:key.current}) });
      onSaved();
    } catch (err) { setError(err?.status === 409 ? '账单身份或版本冲突，请刷新核对。' : '保存失败，请核对验收记录、渠道、时间及凭证。'); lock.current = false; setBusy(false); }
  }
  const time = row?.occurred_at ? new Date(new Date(row.occurred_at).getTime() + 8 * 3600000).toISOString().slice(0,19) : '';
  return <div className='finance-overlay'><div className='finance-dialog' role='dialog' aria-modal='true' aria-label='直接上游验收费用' ref={dialog} tabIndex={-1} onKeyDown={onKey}>
    <header><h2>直接上游验收费用</h2><button type='button' aria-label='关闭直接费用' title='关闭' disabled={busy} onClick={onClose}><X size={18}/></button></header>
    <form onSubmit={submit}><fieldset disabled={busy}><div className='finance-form-grid'>
      <label>供应商<input name='supplier' required maxLength={128} readOnly={!!row} defaultValue={row?.supplier || ''}/></label>
      <label>上游账户标识<input aria-label='上游账户标识' name='account' required maxLength={128} readOnly={!!row} defaultValue={row?.account || ''}/><small>仅填写稳定的非敏感标识，不填密钥</small></label>
      <label className='finance-wide'>上游账单 ID<input name='bill_id' required maxLength={128} readOnly={!!row} defaultValue={row?.bill_id || ''}/></label>
      <label>验收记录 ID<input name='verification_id' required type='number' min='1' step='1' defaultValue={row?.verification_id || ''}/></label>
      <label>验收渠道 ID<input name='channel_id' required type='number' min='1' step='1' defaultValue={row?.channel_id || ''}/></label>
      <label>上游模型<input name='model' maxLength={128} defaultValue={row?.model || ''}/></label>
      <label>输入 token<input name='input_tokens' type='number' min='0' max='9007199254740991' step='1' defaultValue={row?.input_tokens ?? ''}/></label>
      <label>输出 token<input name='output_tokens' type='number' min='0' max='9007199254740991' step='1' defaultValue={row?.output_tokens ?? ''}/></label>
      <label>输入 token 口径<select name='input_token_scope' defaultValue={row?.input_token_scope || 'unknown'}><option value='unknown'>unknown</option><option value='inclusive'>inclusive</option><option value='exclusive'>exclusive</option></select></label>
      <label>缓存读取 token<input name='cache_read' type='number' min='0' max='9007199254740991' step='1' defaultValue={row?.cache_read ?? ''}/></label>
      <label>缓存写入 5 分钟 token<input name='cache_write_5m' type='number' min='0' max='9007199254740991' step='1' defaultValue={row?.cache_write_5m ?? ''}/></label>
      <label>缓存写入 1 小时 token<input name='cache_write_1h' type='number' min='0' max='9007199254740991' step='1' defaultValue={row?.cache_write_1h ?? ''}/></label>
      <label>账单发生时间（北京时间）<input name='occurred_at' type='datetime-local' step='1' required defaultValue={time}/></label>
      <label>直接费用原币金额<input name='amount' type='number' min='0' step='0.00000001' required defaultValue={row?.amount || ''}/></label>
      <label>直接费用币种<select value={currency} onChange={e => setCurrency(e.target.value)}><option value='XCLOUD_CREDIT'>Xcloud 额度</option><option>USDT</option><option>USD</option><option>CNY</option></select></label>
      <label>折合 USDT 汇率<input name='rate' type='number' min='0.000000000001' step='any' defaultValue={row?.rate || ''}/></label>
      <label className='finance-wide'>折算凭证<input name='rate_evidence' maxLength={500} defaultValue={row?.rate_evidence || ''}/></label>
      <label className='finance-wide'>上游账单凭证<input name='evidence' minLength={3} maxLength={500} required defaultValue={row?.evidence || ''}/></label>
      <label className='finance-wide'>验收关联凭证<input name='link_evidence' minLength={3} maxLength={500} required defaultValue={row?.link_evidence || ''}/></label>
      <label>直接费用覆盖关系<select value={coverage} onChange={e => setCoverage(e.target.value)}><option value='unknown'>未知</option><option value='included'>月账单内</option><option value='extra'>月账单外</option></select></label>
      {coverage !== 'unknown' && <><label>月账凭证版本 ID<input name='month_cost_fact_id' type='number' min='1' required defaultValue={row?.month_cost_fact_id || ''}/></label><label className='finance-wide'>直接费用覆盖凭证<input name='coverage_evidence' minLength={3} maxLength={500} required defaultValue={row?.coverage_evidence || ''}/></label></>}
    </div></fieldset>{error && <p role='alert' className='finance-error'>{error}</p>}<footer><button type='button' disabled={busy} onClick={onClose}>取消</button><button className='finance-primary' type='submit' disabled={busy}><Check size={16}/>保存直接费用</button></footer></form>
  </div></div>;
}
