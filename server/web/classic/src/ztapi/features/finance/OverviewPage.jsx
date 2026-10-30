// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
import React, { useEffect, useRef, useState } from 'react';
import {
  Download,
  RefreshCw,
  Pencil,
  X,
  Check,
  ChevronLeft,
  ChevronRight,
} from 'lucide-react';
import { adminRequest, adminDownload } from '../../auth/admin-session.js';
import './overview.css';
import DirectCostDialog from './DirectCostDialog.jsx';

const API = '/api/admin/finance-reporting/';
const monthNow = () =>
  new Intl.DateTimeFormat('sv-SE', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
  }).format(new Date());
const timeLabel = (v) =>
  v
    ? new Date(typeof v === 'number' ? v * 1000 : v).toLocaleString('zh-CN', {
        timeZone: 'Asia/Shanghai',
        hour12: false,
      })
    : '-';
const kinds = {
  cash: '充值实收',
  gift: '赠送额度',
  internal: '内部测试额度',
  correction: '非消费额度冲销',
};
const reasons = {
  unclassified_funding: '历史额度性质待标注',
  unknown_revenue_basis: '消费对应的实收资金待核对',
  ledger_gap: '账本连续性待核对',
  settlement_basis_mismatch: '结算资金基础待核对',
  payment_lineage_missing: '充值到账凭证待关联',
  bonus_lineage_missing: '赠送来源待核对',
  invalid_reservation: '预扣记录待核对',
  settlement_lineage_missing: '收费记录待关联',
  release_lineage_missing: '预扣退回待核对',
  refund_lineage_missing: '退款原收费待关联',
  finalization_missing: '缺少稳定结算时间',
  supplier_cost_missing: '上游整月实际成本待补齐',
  supplier_cost_partial: '上游账单尚未覆盖整月',
  orphan_reservation: '预扣未关联结算记录',
  retry_posting_missing: '重试补扣缺少出账凭证',
  cost_lineage_missing: '实际出站渠道待核对',
  invalid_cost: '成本币种或金额待核对',
  test_cost_coverage_missing: '测试费用与整月账单覆盖关系待核对',
  test_cost_coverage_conflict: '已包含测试费用超过渠道账单金额',
  test_cost_exchange_missing: 'Xcloud 额度的现金折算方式未确认',
  test_cost_unconfirmed: '额外测试费用尚未确认',
  test_cost_invalid: '测试费用凭证或金额待核对',
  test_cost_lineage_missing: '测试请求尝试来源待核对',
  direct_cost_invalid: '直接验收费用凭证待核对',
  direct_cost_exchange_missing: '直接验收费用折算待核对',
  direct_cost_coverage_missing: '直接验收费用账单覆盖待核对',
  direct_cost_lineage_missing: '直接验收来源待核对',
};
const coverageLabels = {
  included: '账单内',
  extra: '账单外额外费用',
  unknown: '覆盖未知',
};
function TestCostDialog({ row, onClose, onSaved }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [currency, setCurrency] = useState(row?.currency || 'USDT');
  const [coverage, setCoverage] = useState(row?.coverage || 'unknown');
  const [status, setStatus] = useState(row?.status || 'estimate');
  const key = useRef(crypto.randomUUID());
  const lock = useRef(false);
  const dialog = useRef(null);
  useEffect(() => {
    const previous = document.activeElement;
    dialog.current?.focus();
    return () => previous?.focus?.();
  }, []);
  function onKey(e) {
    if (e.key === 'Escape' && !busy) onClose();
    if (e.key !== 'Tab') return;
    const controls = dialog.current?.querySelectorAll(
      'button:not(:disabled),input:not(:disabled),select:not(:disabled)',
    );
    if (!controls?.length) return;
    const first = controls[0],
      last = controls[controls.length - 1];
    if (
      e.shiftKey &&
      (document.activeElement === first ||
        document.activeElement === dialog.current)
    ) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
  async function submit(e) {
    e.preventDefault();
    if (lock.current) return;
    const f = new FormData(e.currentTarget);
    const requestId = String(f.get('request_id')).trim();
    const attempt = Number(f.get('attempt'));
    const channel = Number(f.get('channel_id'));
    const value = {
      request_id: requestId,
      attempt,
      channel_id: channel,
      input_tokens: f.get('input_tokens') === '' ? null : Number(f.get('input_tokens')),
      output_tokens: f.get('output_tokens') === '' ? null : Number(f.get('output_tokens')),
      status,
      evidence_type: String(f.get('evidence_type')),
      coverage,
      amount: String(f.get('amount')),
      refund: String(f.get('refund') || '0'),
      refund_evidence: String(f.get('refund_evidence') || '').trim(),
      currency,
      rate: String(f.get('rate') || ''),
      rate_evidence: String(f.get('rate_evidence') || '').trim(),
      evidence: String(f.get('evidence')).trim(),
      coverage_evidence: String(f.get('coverage_evidence') || '').trim(),
      supplier_bill_id: String(f.get('supplier_bill_id') || '').trim(),
    };
    lock.current = true;
    setBusy(true);
    try {
      await adminRequest({
        url: API + 'facts',
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          kind: 'test_cost',
          ref: `test:${requestId}:${attempt}:${channel}`,
          test_cost: value,
          previous_id: row?.fact_id || 0,
          idempotency_key: key.current,
        }),
      });
      onSaved();
    } catch (err) {
      setError(
        err?.status === 409
          ? '记录已变更，请刷新后重新核对。'
          : '保存失败，请核对请求、尝试、金额及凭证。',
      );
      lock.current = false;
      setBusy(false);
    }
  }
  return (
    <div className='finance-overlay'>
      <div
        className='finance-dialog'
        ref={dialog}
        tabIndex={-1}
        onKeyDown={onKey}
        role='dialog'
        aria-modal='true'
        aria-label='测试费用凭证'
      >
        <header>
          <h2>测试费用凭证</h2>
          <button
            title='关闭'
            aria-label='关闭测试费用凭证'
            disabled={busy}
            onClick={onClose}
          >
            <X size={18} />
          </button>
        </header>
        <form onSubmit={submit}>
          <fieldset disabled={busy}>
            <div className='finance-form-grid'>
              <label className='finance-wide'>
                请求 ID
                <input
                  name='request_id'
                  required
                  maxLength={128}
                  readOnly={!!row}
                  defaultValue={row?.request_id || ''}
                />
              </label>
              <label>
                尝试序号
                <input
                  name='attempt'
                  type='number'
                  min='1'
                  step='1'
                  required
                  readOnly={!!row}
                  defaultValue={row?.attempt || ''}
                />
              </label>
              <label>
                渠道 ID
                <input
                  name='channel_id'
                  type='number'
                  min='1'
                  step='1'
                  required
                  readOnly={!!row}
                  defaultValue={row?.channel_id || ''}
                />
              </label>
              <label>
                上游输入 Token
                <input name='input_tokens' type='number' min='0' step='1' defaultValue={row?.input_tokens ?? ''} />
              </label>
              <label>
                上游输出 Token
                <input name='output_tokens' type='number' min='0' step='1' defaultValue={row?.output_tokens ?? ''} />
              </label>
              <label className='finance-wide'>
                供应商账单 ID（可选）
                <input name='supplier_bill_id' maxLength={128} defaultValue={row?.supplier_bill_id || ''} />
              </label>
              <label>
                金额状态
                <select
                  name='status'
                  value={status}
                  disabled={currency === 'XCLOUD_CREDIT'}
                  onChange={(e) => setStatus(e.target.value)}
                >
                  <option value='estimate'>估算</option>
                  <option value='confirmed'>已确认</option>
                </select>
              </label>
              <label>
                证据类别
                <select
                  name='evidence_type'
                  key={status}
                  required
                  defaultValue={
                    row?.status === status ? row?.evidence_type || '' : ''
                  }
                >
                  <option value=''>请选择</option>
                  {status === 'confirmed' ? (
                    <option value='supplier_statement'>上游实际扣费凭证</option>
                  ) : (
                    <>
                      <option value='quote'>报价</option>
                      <option value='other'>其他估算依据</option>
                    </>
                  )}
                </select>
              </label>
              <label>
                覆盖关系
                <select
                  name='coverage'
                  value={coverage}
                  onChange={(e) => setCoverage(e.target.value)}
                >
                  <option value='unknown'>未知</option>
                  <option value='included'>已包含在渠道月账单</option>
                  <option value='extra'>账单外额外费用</option>
                </select>
              </label>
              <label>
                原币费用
                <input
                  name='amount'
                  type='number'
                  min='0'
                  step='0.00000001'
                  required
                  defaultValue={row?.amount || ''}
                />
              </label>
              <label>
                上游已确认返还
                <input
                  name='refund'
                  type='number'
                  min='0'
                  step='0.00000001'
                  required
                  defaultValue={row?.refund || '0'}
                />
              </label>
              <label>
                原币币种
                <select
                  name='currency'
                  value={currency}
                  onChange={(e) => {
                    setCurrency(e.target.value);
                    if (e.target.value === 'XCLOUD_CREDIT') {
                      setStatus('confirmed');
                      setCoverage('unknown');
                    }
                  }}
                >
                  <option>USDT</option>
                  <option>CNY</option>
                  <option>USD</option>
                  <option value='XCLOUD_CREDIT'>Xcloud 额度</option>
                </select>
              </label>
              <label>
                每单位原币折合 USDT
                <input
                  key={currency}
                  name='rate'
                  type='number'
                  min='0.000000000001'
                  step='any'
                  required={currency !== 'XCLOUD_CREDIT'}
                  readOnly={currency === 'USDT'}
                  defaultValue={currency === 'USDT' ? '1' : row?.rate || ''}
                />
              </label>
              <label className='finance-wide'>
                测试费用折算依据
                <input name='rate_evidence' maxLength={500} defaultValue={row?.rate_evidence || ''} />
              </label>
              <label className='finance-wide'>
                费用凭证或报价依据
                <input
                  name='evidence'
                  minLength={3}
                  maxLength={500}
                  required
                  defaultValue={row?.evidence || ''}
                />
              </label>
              <label className='finance-wide'>
                覆盖关系凭证
                <input
                  name='coverage_evidence'
                  maxLength={500}
                  minLength={coverage === 'unknown' ? undefined : 3}
                  required={coverage !== 'unknown'}
                  defaultValue={row?.coverage_evidence || ''}
                />
                <small>
                  {coverage === 'included'
                    ? '填写对应渠道月账单的凭证编号'
                    : coverage === 'extra'
                      ? '填写费用在月账单之外的证明，不能使用月账单编号'
                      : '覆盖关系未知时毛利待核对'}
                </small>
              </label>
              <label className='finance-wide'>
                返还凭证
                <input
                  name='refund_evidence'
                  maxLength={500}
                  defaultValue={row?.refund_evidence || ''}
                />
              </label>
            </div>
          </fieldset>
          {error && (
            <p role='alert' className='finance-error'>
              {error}
            </p>
          )}
          <footer>
            <button type='button' disabled={busy} onClick={onClose}>
              取消
            </button>
            <button className='finance-primary' type='submit' disabled={busy}>
              <Check size={16} />
              {busy ? '保存中' : '保存凭证'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  );
}
function Pager({ page, total, onPage }) {
  return (
    <div className='finance-pager'>
      <span>{total} 条</span>
      <button
        title='上一页'
        aria-label='上一页'
        disabled={page === 0}
        onClick={() => onPage(page - 1)}
      >
        <ChevronLeft size={16} />
      </button>
      <span>
        {page + 1} / {Math.max(1, Math.ceil(total / 20))}
      </span>
      <button
        title='下一页'
        aria-label='下一页'
        disabled={(page + 1) * 20 >= total}
        onClick={() => onPage(page + 1)}
      >
        <ChevronRight size={16} />
      </button>
    </div>
  );
}
function EvidenceDialog({ target, month, closed, onClose, onSaved }) {
  const isCost = target.kind === 'cost',
    old = isCost ? target.row : target.row.classification;
  const [kind, setKind] = useState(
    old?.kind || (target.row.quota < 0 ? 'correction' : 'cash'),
  );
  const [currency, setCurrency] = useState(old?.currency || 'USDT');
  const [rate, setRate] = useState(old?.rate || '1');
  const [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  const lock = useRef(false),
    key = useRef(crypto.randomUUID()),
    dialog = useRef(null);
  useEffect(() => {
    const previous = document.activeElement;
    dialog.current?.focus();
    return () => previous?.focus?.();
  }, []);
  function onKey(e) {
    if (e.key === 'Escape' && !busy) onClose();
    if (e.key === 'Tab') {
      const controls = dialog.current?.querySelectorAll(
        'button:not(:disabled),input:not(:disabled),select:not(:disabled)',
      );
      if (!controls?.length) return;
      const first = controls[0],
        last = controls[controls.length - 1];
      if (
        e.shiftKey &&
        (document.activeElement === first ||
          document.activeElement === dialog.current)
      ) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }
  async function submit(e) {
    e.preventDefault();
    if (lock.current) return;
    const f = new FormData(e.currentTarget);
    lock.current = true;
    setBusy(true);
    setError('');
    const evidence = String(f.get('evidence') || '').trim();
    let input;
    if (isCost) {
      input = {
        kind: 'cost',
        ref: `${month}:${target.row.channel_id}`,
        cost: {
          month,
          channel_id: target.row.channel_id,
          supplier: String(f.get('supplier')).trim(),
          gross: String(f.get('gross')),
          refund: String(f.get('refund') || '0'),
          refund_evidence: String(f.get('refund_evidence') || '').trim(),
          currency,
          rate,
          complete: f.get('complete') === 'on',
          evidence,
          excluded_direct_bill_refs: String(f.get('excluded_direct_bill_refs') || '').split(/\s+/).filter(Boolean),
        },
        previous_id: target.row.fact_id || 0,
      };
    } else {
      const received = String(f.get('received') || '');
      input = {
        kind: 'funding',
        ref: target.row.ref,
        funding: {
          kind,
          amount: kind === 'cash' ? String(f.get('amount')) : '0',
          currency: kind === 'cash' ? currency : 'USDT',
          rate: kind === 'cash' ? rate : '1',
          received_at:
            kind === 'cash'
              ? Math.floor(new Date(`${received}+08:00`).getTime() / 1000)
              : 0,
          evidence,
        },
        previous_id: target.row.fact_id || 0,
      };
    }
    try {
      await adminRequest({
        url: API + 'facts',
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...input, idempotency_key: key.current }),
      });
      onSaved();
    } catch (err) {
      setError(
        err?.status === 409
          ? '凭证版本已变更，请关闭后刷新再核对。'
          : '保存失败，请核对金额、时间、币种及凭证；不会修改客户余额。',
      );
      lock.current = false;
      setBusy(false);
    }
  }
  const cash = isCost || kind === 'cash';
  const rateFields = (
    <>
      <label>
        原币币种
        <select
          value={currency}
          onChange={(e) => {
            setCurrency(e.target.value);
            setRate(e.target.value === 'USDT' ? '1' : '');
          }}
        >
          <option>USDT</option>
          <option>CNY</option>
          <option>USD</option>
        </select>
      </label>
      <label>
        每单位原币折合 USDT
        <input
          type='number'
          min='0.000000000001'
          step='any'
          required
          value={rate}
          readOnly={currency === 'USDT'}
          onChange={(e) => setRate(e.target.value)}
        />
      </label>
    </>
  );
  return (
    <div className='finance-overlay'>
      <div
        className='finance-dialog'
        ref={dialog}
        tabIndex={-1}
        role='dialog'
        aria-modal='true'
        aria-label={isCost ? '上游实际成本' : '历史额度标注'}
        onKeyDown={onKey}
      >
        <header>
          <h2>{isCost ? '上游实际成本' : '历史额度标注'}</h2>
          <button
            aria-label='关闭财务凭证'
            title='关闭'
            disabled={busy}
            onClick={onClose}
          >
            <X size={18} />
          </button>
        </header>
        <p className='finance-muted'>
          {isCost
            ? `${month} · 渠道 #${target.row.channel_id} · ${target.row.name || target.row.supplier || ''}`
            : `${target.row.username || target.row.user_id} · ${target.row.ref}`}
        </p>
        <form onSubmit={submit}>
          <fieldset disabled={busy}>
            <div className='finance-form-grid'>
              {isCost ? (
                <>
                  <label className='finance-wide'>
                    上游名称
                    <input
                      name='supplier'
                      maxLength={128}
                      minLength={2}
                      required
                      defaultValue={old?.supplier || ''}
                    />
                  </label>
                  <label>
                    原币实际消耗
                    <input
                      name='gross'
                      type='number'
                      min='0'
                      step='0.00000001'
                      required
                      defaultValue={old?.gross || ''}
                    />
                  </label>
                  <label>
                    已确认上游返还
                    <input
                      name='refund'
                      type='number'
                      min='0'
                      step='0.00000001'
                      required
                      defaultValue={old?.refund || '0'}
                    />
                  </label>
                </>
              ) : (
                <label className='finance-wide'>
                  额度性质
                  <select
                    value={kind}
                    onChange={(e) => setKind(e.target.value)}
                  >
                    {Object.entries(kinds)
                      .filter(([k]) =>
                        target.row.quota < 0
                          ? k === 'correction'
                          : k !== 'correction',
                      )
                      .map(([k, v]) => (
                        <option key={k} value={k}>
                          {v}
                        </option>
                      ))}
                  </select>
                </label>
              )}
              {!isCost && cash && (
                <>
                  <label>
                    实际收到金额
                    <input
                      name='amount'
                      type='number'
                      min='0.00000001'
                      step='0.00000001'
                      required
                      defaultValue={old?.amount || ''}
                    />
                  </label>
                  <label>
                    实际到账时间（北京时间）
                    <input
                      name='received'
                      type='datetime-local'
                      required
                      defaultValue={
                        old?.received_at
                          ? new Date(old.received_at * 1000 + 8 * 3600000)
                              .toISOString()
                              .slice(0, 16)
                          : ''
                      }
                    />
                  </label>
                </>
              )}
              {cash && rateFields}
              <label className='finance-wide'>
                {isCost ? '账单凭证' : '收款凭证或标注依据'}
                <input
                  name='evidence'
                  minLength={3}
                  maxLength={500}
                  required
                  defaultValue={old?.evidence || ''}
                />
              </label>
              {isCost && (
                <label className='finance-wide'>
                  返还凭证及原账单关联
                  <input
                    name='refund_evidence'
                    maxLength={500}
                    defaultValue={old?.refund_evidence || ''}
                  />
                </label>
              )}
              {isCost && (
                <label className='finance-wide'>
                  月账排除的直接费用身份
                  <input name='excluded_direct_bill_refs' defaultValue={(old?.excluded_direct_bill_refs || []).join(' ')} />
                </label>
              )}
              {isCost && (
                <label className='finance-wide finance-check'>
                  <input
                    type='checkbox'
                    name='complete'
                    disabled={!closed}
                    defaultChecked={Boolean(old?.complete)}
                  />
                  <span>确认已覆盖整月本平台全部凭据、失败重试和测试消耗</span>
                </label>
              )}
            </div>
          </fieldset>
          {error && (
            <p role='alert' className='finance-error'>
              {error}
            </p>
          )}
          <footer>
            <button type='button' disabled={busy} onClick={onClose}>
              取消
            </button>
            <button className='finance-primary' type='submit' disabled={busy}>
              <Check size={16} />
              {busy ? '保存中' : '保存财务凭证'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  );
}
export default function OverviewPage({ canWrite = false }) {
  const [month, setMonth] = useState(monthNow),
    [revision, setRevision] = useState(0),
    [report, setReport] = useState(null),
    [state, setState] = useState('loading');
  const [tab, setTab] = useState('cost'),
    [page, setPage] = useState(0),
    [onlyPending, setOnlyPending] = useState(true),
    [target, setTarget] = useState(null),
    [notice, setNotice] = useState('');
  useEffect(() => {
    let active = true;
    setState('loading');
    setReport(null);
    setTarget(null);
    setPage(0);
    adminRequest({ url: API + `report?month=${encodeURIComponent(month)}` })
      .then((r) => {
        if (
          !r ||
          !Array.isArray(r.receipts) ||
          !Array.isArray(r.channels) ||
          !Array.isArray(r.manual_funding) ||
          !Array.isArray(r.issues)
        )
          throw new Error('invalid_report');
        if (active) {
          setReport(r);
          setState('ready');
        }
      })
      .catch(() => {
        if (active) setState('error');
      });
    return () => {
      active = false;
    };
  }, [month, revision]);
  useEffect(() => {
    if (!canWrite) setTarget(null);
  }, [canWrite]);
  const refresh = () => setRevision((n) => n + 1);
  async function download() {
    try {
      await adminDownload(
        { url: API + `export?month=${encodeURIComponent(month)}` },
        `ztapi-finance-${month}.csv`,
      );
    } catch {
      setNotice('导出失败，请稍后重试。');
    }
  }
  const manual =
    report?.manual_funding.filter((r) => !onlyPending || !r.classification) ||
    [];
  const costs =
    report?.channels.map((c) => ({
      ...c,
      ...report.costs.find((row) => row.channel_id === c.channel_id),
    })) || [];
  for (const c of report?.costs || []) {
    if (!costs.some((row) => row.channel_id === c.channel_id)) costs.push(c);
  }
  const issues = report
    ? [
        ...new Set(
          report.issues.map((i) => reasons[i.code] || '财务记录待核对'),
        ),
      ]
    : [];
  return (
    <section
      className='ztapi-admin-page finance-overview'
      aria-label='财务总览'
    >
      <header className='finance-toolbar'>
        <div>
          <h1>财务总览</h1>
          <span className='finance-muted'>北京时间 · USDT</span>
        </div>
        <div className='finance-actions'>
          <label>
            统计月份
            <input
              aria-label='统计月份'
              type='month'
              value={month}
              max={monthNow()}
              onChange={(e) => {
                if (e.target.value) setMonth(e.target.value);
              }}
            />
          </label>
          <button
            title='刷新财务数据'
            aria-label='刷新财务数据'
            onClick={refresh}
          >
            <RefreshCw size={17} />
          </button>
          <button
            title='导出本月财务报表'
            aria-label='导出本月财务报表'
            disabled={!report}
            onClick={download}
          >
            <Download size={17} />
          </button>
        </div>
      </header>
      {state === 'loading' && <p role='status'>正在读取财务记录...</p>}
      {state === 'error' && (
        <p className='finance-error' role='alert'>
          财务数据加载失败。<button onClick={refresh}>重试</button>
        </p>
      )}
      {notice && <p role='alert'>{notice}</p>}
      {report && (
        <>
          <div
            className={`finance-status ${report.confirmed ? 'is-confirmed' : ''}`}
          >
            <strong>
              {report.confirmed ? '本月毛利已核对' : '毛利待核对'}
            </strong>
            <span>
              {report.confirmed
                ? `毛利率 ${report.margin ?? '-'}%`
                : !report.period_closed
                  ? '本月尚未结账'
                  : issues.join('；') || '财务凭证待补齐'}
            </span>
          </div>
          <dl className='finance-metrics'>
            {[
              ['消费对应实收收入', report.revenue],
              ['上游实际消耗', report.actual_cost],
              ['毛利', report.gross_profit],
            ].map(([title, value]) => (
              <div key={title}>
                <dt>{title}</dt>
                <dd>{value ?? '—'}</dd>
                <small>{value === null ? '待核对' : 'USDT'}</small>
              </div>
            ))}
          </dl>
          <section aria-label='测试费用'>
            <header className='finance-toolbar'>
              <div>
                <h2>测试费用</h2>
                <span className='finance-muted'>
                  已确认 {report.test_confirmed_total || '待核对'} USDT · 估算{' '}
                  {report.test_estimate_total || '待核对'} USDT
                  {' · '}Xcloud 额度 {report.test_xcloud_credit_total || '待核对'}
                </span>
              </div>
              {canWrite && (
                <button
                  onClick={() => setTarget({ kind: 'test_cost', row: null })}
                >
                  录入测试费用
                </button>
              )}
            </header>
            <div className='finance-table-wrap'>
              <table>
                <thead>
                  <tr>
                    <th>请求 ID</th>
                    <th>供应商账单 ID</th>
                    <th>模型</th>
                    <th>尝试 / 渠道</th>
                    <th>上游输入 / 输出 Token</th>
                    <th>时间</th>
                    <th>状态</th>
                    <th>覆盖关系</th>
                    <th>净费用</th>
                    {canWrite && <th>凭证</th>}
                  </tr>
                </thead>
                <tbody>
                  {(report.test_costs || []).map((row) => (
                    <tr key={row.fact_id}>
                      <td>{row.request_id}</td>
                      <td>{row.supplier_bill_id || '待核对'}</td>
                      <td>{row.model || '—'}</td>
                      <td>
                        {row.attempt} / #{row.channel_id}
                      </td>
                      <td>{row.input_tokens ?? '—'} / {row.output_tokens ?? '—'}</td>
                      <td>{timeLabel(row.attempt_at)}</td>
                      <td>{row.status === 'confirmed' ? '已确认' : '估算'}</td>
                      <td>{coverageLabels[row.coverage] || '覆盖未知'}</td>
                      <td>
                        {row.currency === 'XCLOUD_CREDIT' ? (
                          <>
                            <span>{row.net_credit ? `${row.net_credit} Xcloud 额度` : '原币待核对'}</span>
                            <small>{row.net_usdt ? `${row.net_usdt} USDT` : '折合待核对'}</small>
                          </>
                        ) : (
                          <>
                            <span>{row.net_usdt ? `${row.net_usdt} USDT` : '折合待核对'}</span>
                            {row.currency && row.currency !== 'USDT' && row.amount !== undefined && row.amount !== '' && <small>{row.amount} {row.currency}</small>}
                          </>
                        )}
                      </td>
                      {canWrite && (
                        <td>
                          <button
                            title='修订测试费用凭证'
                            aria-label={`修订${row.request_id}测试费用`}
                            onClick={() =>
                              setTarget({ kind: 'test_cost', row })
                            }
                          >
                            <Pencil size={15} />
                          </button>
                        </td>
                      )}
                    </tr>
                  ))}
                  {!report.test_costs?.length && (
                    <tr>
                      <td colSpan={canWrite ? 10 : 9}>本月暂无测试费用记录。</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
            <p className='finance-footnote'>
              账单内费用仅拆分展示；报价为估算，平台扣费不代表上游实际成本。
            </p>
          </section>
          <section aria-label='直接上游验收费用'>
            <header className='finance-toolbar'><div><h2>直接上游验收费用</h2><span className='finance-muted'>已折算 {report.direct_confirmed_total || '待核对'} USDT · Xcloud 额度 {report.direct_xcloud_credit_total || '待核对'}</span></div>
              {canWrite && <button onClick={() => setTarget({kind:'direct_cost', row:null})}>录入直接上游验收费用</button>}
            </header>
            <div className='finance-table-wrap'><table><thead><tr><th>账单 ID</th><th>供应商 / 账户</th><th>验收 / 渠道</th><th>模型</th><th>输入 / 输出 token</th><th>输入口径</th><th>缓存读取 / 写入 5m / 1h</th><th>发生时间</th><th>原币</th><th>折合</th><th>覆盖</th><th>身份</th>{canWrite && <th>凭证</th>}</tr></thead><tbody>
              {(report.direct_costs || []).map(row => <tr key={row.fact_id}><td>{row.bill_id}</td><td>{row.supplier} / {row.account}</td><td>{row.verification_id} / #{row.channel_id}</td><td>{row.model || '待核对'}</td><td>{row.input_tokens ?? '待核对'} / {row.output_tokens ?? '待核对'}</td><td>{row.input_token_scope || 'unknown'}</td><td>{row.cache_read ?? '待核对'} / {row.cache_write_5m ?? '待核对'} / {row.cache_write_1h ?? '待核对'}</td><td>{timeLabel(row.occurred_at)}</td><td>{row.amount} {row.currency}</td><td>{row.net_usdt ? `${row.net_usdt} USDT` : '待核对'}</td><td>{coverageLabels[row.coverage] || '覆盖未知'}</td><td>{row.ref}</td>{canWrite && <td><button title='修订直接费用' aria-label={`修订${row.bill_id}直接费用`} onClick={() => setTarget({kind:'direct_cost', row})}><Pencil size={15}/></button></td>}</tr>)}
              {!report.direct_costs?.length && <tr><td colSpan={canWrite ? 13 : 12}>本月暂无直接上游验收费用。</td></tr>}
            </tbody></table></div>
          </section>
          <dl className='finance-secondary'>
            <div>
              <dt>本月充值实收</dt>
              <dd>{report.receipts_total}</dd>
            </div>
            <div>
              <dt>已结算净消费额度</dt>
              <dd>
                {(report.charged_quota / report.quota_per_unit).toFixed(6)} U
              </dd>
            </div>
            <div>
              <dt>待结算请求</dt>
              <dd>{report.pending_requests} 笔</dd>
            </div>
            <div>
              <dt>待核对记录</dt>
              <dd>{report.issue_count} 项</dd>
            </div>
          </dl>
          {Array.isArray(report.estimates) && report.estimates.length > 0 && (
            <section className='finance-estimate' aria-label='Xcloud报价估算'>
              <header>
                <div>
                  <h2>Xcloud 渠道报价估算</h2>
                  <p className='finance-muted'>
                    按已结算请求的独立 token 维度重算，使用 Xcloud 公开报价 ×
                    约定 5 折；公开报价不是上游实际扣费。
                  </p>
                </div>
                <span
                  className={report.estimate_complete ? 'is-ok' : 'is-pending'}
                >
                  {report.estimate_complete ? '估算完整' : '估算待核对'}
                </span>
              </header>
              <dl className='finance-estimate-metrics'>
                <div>
                  <dt>估算上游消耗</dt>
                  <dd>{report.estimated_cost ?? '—'}</dd>
                  <small>USDT · 非实扣</small>
                </div>
                <div>
                  <dt>估算毛利</dt>
                  <dd>{report.estimated_gross_profit ?? '—'}</dd>
                  <small>USDT · 非确认毛利</small>
                </div>
              </dl>
              <div className='finance-table-wrap'>
                <table>
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>请求数</th>
                      <th>用户收入</th>
                      <th>Xcloud 报价成本</th>
                      <th>估算毛利</th>
                      <th>状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.estimates.map((row) => (
                      <tr key={`${row.channel_id}-${row.source_model}`}>
                        <td>
                          {row.source_model}
                          <small>
                            {row.supplier || `渠道 #${row.channel_id}`}
                          </small>
                        </td>
                        <td>{row.requests}</td>
                        <td>{row.revenue_usdt} USDT</td>
                        <td>
                          {row.complete ? `${row.cost_usdt} USDT` : '待补齐'}
                        </td>
                        <td>
                          {row.complete ? `${row.gross_profit_usdt} USDT` : '—'}
                        </td>
                        <td>
                          {row.complete ? '公开报价可计算' : '用量或报价缺失'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className='finance-footnote'>
                真实上游成本仍需录入 Xcloud
                账单凭证；录入后才会进入“上游实际消耗”和“已确认毛利”。
              </p>
            </section>
          )}
          <nav className='finance-tabs' role='tablist' aria-label='财务明细'>
            {[
              ['cost', '上游成本'],
              ['receipts', '充值实收'],
              ['funding', '历史额度核对'],
              ['issues', '待核对项'],
            ].map(([key, title]) => (
              <button
                role='tab'
                aria-selected={tab === key}
                aria-controls={`finance-${key}`}
                id={`finance-tab-${key}`}
                key={key}
                onClick={() => {
                  setTab(key);
                  setPage(0);
                }}
              >
                {title}
              </button>
            ))}
          </nav>
          <div
            role='tabpanel'
            id={`finance-${tab}`}
            aria-labelledby={`finance-tab-${tab}`}
            className='finance-panel'
          >
            {tab === 'cost' && (
              <>
                <div className='finance-table-wrap'>
                  <table>
                    <thead>
                      <tr>
                        <th>历史渠道</th>
                        <th>请求尝试</th>
                        <th>上游</th>
                        <th>原币消耗 / 返还</th>
                        <th>实际净成本</th>
                        <th>账单状态</th>
                        {canWrite && <th>凭证</th>}
                      </tr>
                    </thead>
                    <tbody>
                      {costs.length ? (
                        costs.map((c) => (
                          <tr key={c.channel_id}>
                            <td>
                              {c.name || `渠道 #${c.channel_id}`}
                              <small>#{c.channel_id}</small>
                            </td>
                            <td>{c.attempts || 0}</td>
                            <td>{c.supplier || '—'}</td>
                            <td>
                              {c.fact_id
                                ? `${c.gross} / ${c.refund} ${c.currency}`
                                : '—'}
                            </td>
                            <td>
                              {c.net_usdt ? `${c.net_usdt} USDT` : '待补齐'}
                            </td>
                            <td>
                              {c.complete
                                ? '整月已核对'
                                : c.fact_id
                                  ? '部分账单'
                                  : '未录入'}
                              {c.fact_id && <small>凭证版本 #{c.fact_id}</small>}
                            </td>
                            {canWrite && (
                              <td>
                                <button
                                  title={c.evidence || '录入实际账单'}
                                  aria-label={`录入${c.name || `渠道${c.channel_id}`}成本`}
                                  onClick={() =>
                                    setTarget({ kind: 'cost', row: c })
                                  }
                                >
                                  <Pencil size={15} />
                                </button>
                              </td>
                            )}
                          </tr>
                        ))
                      ) : (
                        <tr>
                          <td colSpan={canWrite ? 7 : 6}>
                            本月没有渠道请求记录。
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <p className='finance-footnote'>
                  实际成本只计已消耗金额，减去已确认返还；上游充值余额不计成本。
                </p>
              </>
            )}
            {tab === 'receipts' && (
              <>
                <div className='finance-table-wrap'>
                  <table>
                    <thead>
                      <tr>
                        <th>到账时间</th>
                        <th>用户</th>
                        <th>到账原币</th>
                        <th>折合 USDT</th>
                        <th>凭证</th>
                      </tr>
                    </thead>
                    <tbody>
                      {report.receipts
                        .slice(page * 20, page * 20 + 20)
                        .map((p) => (
                          <tr key={p.ref}>
                            <td>{timeLabel(p.received_at)}</td>
                            <td>{p.username || p.user_id}</td>
                            <td>
                              {p.amount} {p.currency}
                            </td>
                            <td>{p.amount_usdt}</td>
                            <td className='finance-evidence' title={p.evidence}>
                              {p.evidence}
                            </td>
                          </tr>
                        ))}
                      {!report.receipts.length && (
                        <tr>
                          <td colSpan={5}>本月暂无已确认实收记录。</td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <Pager
                  page={page}
                  total={report.receipts.length}
                  onPage={setPage}
                />
              </>
            )}
            {tab === 'funding' && (
              <>
                <label className='finance-check finance-filter'>
                  <input
                    type='checkbox'
                    checked={onlyPending}
                    onChange={(e) => {
                      setOnlyPending(e.target.checked);
                      setPage(0);
                    }}
                  />
                  仅待标注
                </label>
                <div className='finance-table-wrap'>
                  <table>
                    <thead>
                      <tr>
                        <th>记录 / 时间</th>
                        <th>用户</th>
                        <th>额度变化</th>
                        <th>性质</th>
                        {canWrite && <th>核对</th>}
                      </tr>
                    </thead>
                    <tbody>
                      {manual.slice(page * 20, page * 20 + 20).map((row) => (
                        <tr key={row.ref}>
                          <td>
                            {row.ref}
                            <small>{timeLabel(row.created_at)}</small>
                          </td>
                          <td>{row.username || row.user_id}</td>
                          <td>
                            {(row.quota / report.quota_per_unit).toFixed(6)} U
                          </td>
                          <td>{kinds[row.classification?.kind] || '待标注'}</td>
                          {canWrite && (
                            <td>
                              <button
                                aria-label={`标注${row.ref}`}
                                title='标注额度性质'
                                onClick={() =>
                                  setTarget({ kind: 'funding', row })
                                }
                              >
                                <Pencil size={15} />
                              </button>
                            </td>
                          )}
                        </tr>
                      ))}
                      {!manual.length && (
                        <tr>
                          <td colSpan={canWrite ? 5 : 4}>
                            没有符合条件的记录。
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <Pager page={page} total={manual.length} onPage={setPage} />
              </>
            )}
            {tab === 'issues' && (
              <>
                <div className='finance-table-wrap'>
                  <table>
                    <thead>
                      <tr>
                        <th>待核对原因</th>
                        <th>关联记录</th>
                        <th>用户编号</th>
                      </tr>
                    </thead>
                    <tbody>
                      {report.issues
                        .slice(page * 20, page * 20 + 20)
                        .map((item, i) => (
                          <tr key={`${item.ref}-${i}`}>
                            <td>{reasons[item.code] || '财务记录待核对'}</td>
                            <td>{item.ref}</td>
                            <td>{item.user_id || '—'}</td>
                          </tr>
                        ))}
                      {!report.issues.length && (
                        <tr>
                          <td colSpan={3}>暂无待核对记录。</td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
                <Pager
                  page={page}
                  total={report.issues.length}
                  onPage={setPage}
                />
              </>
            )}
          </div>
          <p className='finance-footnote'>
            已知部分收入 {report.known_revenue} USDT · 已录入净成本{' '}
            {report.recorded_cost || '0.000000'} USDT ·{' '}
            {timeLabel(report.as_of)}
            {report.issue_count > report.issues.length && (
              <> · 当前展示前 {report.issues.length} 项待核对记录</>
            )}
            {/^[a-f0-9]{40}$/.test(report.source_commit || '') && (
              <>
                {' '}
                ·{' '}
                <a
                  href={`https://github.com/ffff582/ztapi-source/tree/${report.source_commit}`}
                  target='_blank'
                  rel='noreferrer'
                >
                  财务模块源码
                </a>
              </>
            )}
          </p>
        </>
      )}
      {target?.kind === 'test_cost' && canWrite && (
        <TestCostDialog
          row={target.row}
          onClose={() => setTarget(null)}
          onSaved={() => {
            setTarget(null);
            refresh();
          }}
        />
      )}
      {target?.kind === 'direct_cost' && canWrite && <DirectCostDialog row={target.row} onClose={() => setTarget(null)} onSaved={() => { setTarget(null); refresh(); }} />}
      {target && target.kind !== 'test_cost' && target.kind !== 'direct_cost' && canWrite && (
        <EvidenceDialog
          key={`${target.kind}-${target.row.ref || target.row.channel_id}`}
          target={target}
          month={month}
          closed={report.period_closed}
          onClose={() => setTarget(null)}
          onSaved={() => {
            setTarget(null);
            refresh();
          }}
        />
      )}
    </section>
  );
}
