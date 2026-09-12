// Copyright (C) 2025 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  Eye,
  FilePlus2,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import './reconciliation.css';

const PAGE_SIZE = 20;
const statuses = {
  reserved: '已预留，待结算',
  pending: '待对账',
  approved: '核验已通过，待入账',
  verified_billed: '已核验收费',
  verified_nocharge: '已核验未收费',
  all: '全部状态',
  settled: '已结算',
  released: '预留已释放',
  applied: '已入账，待同步',
  completed: '处理已完成',
};
const reasons = {
  upstream_attempt_billing_unconfirmed: '本次上游计费尚未核实',
  awaiting_proof: '待补供应商凭证',
  billing_dimensions_pending: '计费用量或原价记录待核实',
  billing_application_pending: '核验已通过，等待入账',
  missing_distinct_usage_evidence: '缺少独立用量或上游账单证明',
  unsupported_parent_scope: '原请求状态与本次凭证不匹配',
  existing_finalization_intent: '原请求已有结算任务',
  awaiting_approval: '待财务核验',
  evidence_conflict: '供应商凭证冲突',
  approval_conflict: '核验记录冲突',
  original_request_missing: '找不到原请求',
  original_charge_unsettled: '原请求尚未结算',
  original_owner_missing: '原客户或密钥记录缺失',
  original_owner_deleted: '原客户或密钥已删除',
  billed_attempt_missing: '原计费尝试记录缺失',
  evidence_invalid: '退款凭证无效',
  original_lineage_mismatch: '退款凭证与原请求不匹配',
  original_charge_mismatch: '原扣费记录不一致',
  charge_dimensions_invalid: '原计费明细无效',
  reversal_dimensions_ambiguous: '退款用量无法确定',
  cumulative_refund_cap: '累计退款超出原扣费范围',
  token_charge_mismatch: '密钥扣费记录不一致',
};
const dimensions = {
  input_tokens: '输入文本量',
  output_tokens: '输出文本量',
  cached_tokens: '缓存用量',
  settlement_retry_required: '结算待重试',
  usage_missing: '用量记录缺失',
  cache_read: '缓存读取量',
  cache_write: '缓存写入量',
  cache_write_5m: '五分钟缓存写入量',
  cache_write_1h: '一小时缓存写入量',
};
const display = (value) =>
  (typeof value === 'string' || typeof value === 'number') && value !== ''
    ? String(value)
    : '-';
const statusLabel = (value) => statuses[value] || '状态待核实';
const reasonLabel = (value) =>
  value ? reasons[value] || '待人工核对（原因未识别）' : '-';
function missingLabel(raw) {
  try {
    const values = JSON.parse(raw);
    if (!Array.isArray(values) || values.some((v) => typeof v !== 'string'))
      return '计费缺项待核实';
    return values.length
      ? values.map((v) => dimensions[v] || v).join('、')
      : '无已记录缺项';
  } catch {
    return '计费缺项待核实';
  }
}
function timeLabel(value) {
  const date = new Date(value);
  return value && Number.isFinite(date.getTime())
    ? date.toLocaleString('zh-CN', { hour12: false })
    : '-';
}
function Fields({ entries }) {
  return (
    <dl className='ztapi-reconciliation-fields'>
      {entries.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{display(value)}</dd>
        </div>
      ))}
    </dl>
  );
}

function SettlementDetail({ id, canWrite, onBusy }) {
  const [reload, setReload] = useState(0);
  const [detail, setDetail] = useState(null);
  const [state, setState] = useState('loading');
  const [noChargeDialog, setNoChargeDialog] = useState(false);
  const [notice, setNotice] = useState('');
  useEffect(() => {
    if (!canWrite) setNoChargeDialog(false);
  }, [canWrite]);
  useEffect(() => {
    let active = true;
    setState('loading');
    adminRequest({ url: `/api/admin/request-settlements/${id}` })
      .then((result) => {
        if (!active) return;
        if (!result || result.id !== id) {
          setState('error');
          return;
        }
        setDetail(result);
        setState('ready');
      })
      .catch(() => {
        if (active) setState('error');
      });
    return () => {
      active = false;
    };
  }, [id, reload]);
  if (state === 'loading') return <p role='status'>正在加载结算详情...</p>;
  if (state === 'error')
    return (
      <>
        <p role='alert'>结算详情加载失败。</p>
        <button type='button' onClick={() => setReload((n) => n + 1)}>
          <RefreshCw size={16} />
          重新加载详情
        </button>
      </>
    );
  const totalCharged = detail.total_charged_quota ?? detail.charged_quota;
  const netCharged =
    detail.net_charged_quota ??
    (Number.isSafeInteger(totalCharged) &&
    Number.isSafeInteger(detail.refunded_quota)
      ? totalCharged - detail.refunded_quota
      : undefined);
  return (
    <>
      <Fields
        entries={[
          ['原客户 ID', detail.user_id],
          ['原请求 ID', detail.request_id],
          ['公开模型', detail.model],
          ['结算状态', statusLabel(detail.status)],
          ['预留额度（原始单位）', detail.reserved_quota],
          ['待补计费项', missingLabel(detail.missing_dimensions)],
          ['原密钥 ID', detail.token_id],
          ['结算操作 ID', detail.operation_id],
          ['原始扣费（原始单位）', detail.charged_quota],
          ['追加扣费（原始单位）', detail.additional_charged_quota ?? 0],
          ['累计扣费（原始单位）', totalCharged],
          ['累计退款（原始单位）', detail.refunded_quota],
          ['净扣费（原始单位）', netCharged],
          ['最近账本流水 ID', detail.last_ledger_id || '-'],
          ['上游派发', detail.dispatched ? '已派发' : '未派发'],
          ['创建时间', timeLabel(detail.created_at)],
        ]}
      />
      {Array.isArray(detail.charge_anchors) &&
        detail.charge_anchors.length > 0 && (
          <>
            <h3>尝试扣费记录</h3>
            <div
              className='ztapi-ops-table-wrap'
              role='region'
              aria-label='尝试扣费记录表格'
              tabIndex={0}
            >
              <table className='ztapi-ops-table'>
                <thead>
                  <tr>
                    {[
                      '扣费记录 ID',
                      '尝试序号',
                      '渠道 ID',
                      '扣费（原始单位）',
                      '退款（原始单位）',
                      '计费凭证 ID',
                      '原始账本流水 ID',
                    ].map((label) => (
                      <th scope='col' key={label}>
                        {label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {detail.charge_anchors.map((charge) => (
                    <tr key={charge.id}>
                      {[
                        charge.id,
                        charge.attempt,
                        charge.channel_id,
                        charge.charged_quota,
                        charge.refunded_quota,
                        charge.billing_proof_id || '-',
                        charge.original_ledger_id || '-',
                      ].map((value, index) => (
                        <td key={index}>{display(value)}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      {notice && (
        <p role='status' className='ztapi-ops-notice'>
          {notice}
        </p>
      )}
      {canWrite && detail.status === 'pending' && (
        <div className='ztapi-ops-actions'>
          <button
            type='button'
            disabled={!detail.attempts?.length || detail.attempts.length > 2}
            onClick={() => setNoChargeDialog(true)}
          >
            <FilePlus2 size={16} />
            核验整单未收费
          </button>
        </div>
      )}
      {noChargeDialog && canWrite && detail.status === 'pending' && (
        <EvidenceDialog
          type='nocharge'
          identity={detail}
          canWrite={canWrite}
          onBusy={onBusy}
          onClose={() => setNoChargeDialog(false)}
          onSuccess={(result) => {
            setNoChargeDialog(false);
            setDetail((current) => ({
              ...current,
              status: result.status,
              last_ledger_id: result.ledger_id ?? current.last_ledger_id,
            }));
            setNotice(
              result.status === 'released'
                ? result.cache_sync_pending
                  ? '整单未收费核验已完成，预留已释放；账务同步仍在进行。'
                  : '整单未收费核验已完成，预留已释放。'
                : '核验结果尚未确认释放预留，请刷新核对，勿重复提交。',
            );
          }}
        />
      )}
      <h3>请求尝试记录</h3>
      <div
        className='ztapi-ops-table-wrap'
        role='region'
        aria-label='请求尝试记录表格'
        tabIndex={0}
      >
        <table className='ztapi-ops-table'>
          <thead>
            <tr>
              {[
                '尝试记录 ID',
                '尝试序号',
                '渠道 ID',
                '凭据版本',
                '协议',
                '上游请求 ID',
                '响应状态码',
              ].map((label) => (
                <th key={label} scope='col'>
                  {label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(detail.attempts || []).map((attempt) => (
              <tr key={attempt.id}>
                {[
                  attempt.id,
                  attempt.attempt,
                  attempt.channel_id,
                  attempt.credential_version,
                  attempt.protocol,
                  attempt.upstream_request_id,
                  attempt.http_status || '-',
                ].map((value, index) => (
                  <td key={index}>{display(value)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {!detail.attempts?.length && (
          <p className='ztapi-ops-empty'>暂无请求尝试记录。</p>
        )}
      </div>
    </>
  );
}

function RefundReview({ row, canWrite, onResult, onBusy }) {
  const [reference, setReference] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [state, setState] = useState('ready');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const inFlight = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const input = row.submission || {};
  const eligible =
    row.status === 'pending' && row.pending_reason === 'awaiting_approval';
  const referenceTooLong =
    new TextEncoder().encode(reference.trim()).length > 512;
  const validReference = reference.trim().length > 0 && !referenceTooLong;
  const submit = async (event) => {
    event.preventDefault();
    if (
      !canWrite ||
      !eligible ||
      !validReference ||
      !confirmed ||
      state !== 'ready' ||
      inFlight.current
    )
      return;
    inFlight.current = true;
    setState('submitting');
    onBusy(true);
    try {
      const result = await adminRequest({
        method: 'POST',
        url: `/api/admin/supplier-refunds/${row.id}/approve`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ verification_reference: reference.trim() }),
      });
      if (!mounted.current) return;
      if (
        !result ||
        result.id !== row.id ||
        result.request_id !== row.request_id
      )
        throw new Error('Unexpected refund identity');
      onResult(result);
      setState('submitted');
      setNotice(
        {
          pending: '核验审批已受理，退款仍待对账，尚未入账。',
          applied: '退款已入账，缓存同步仍在进行。',
          completed: '退款处理已完成。',
        }[result.status] || '审批结果状态未知，请刷新核对，勿重复提交。',
      );
    } catch (failure) {
      if (!mounted.current) return;
      setState('error');
      setError(
        failure.status === 403
          ? '当前账号无退款审批权限，请刷新核对。'
          : failure.status === 409
            ? '核验记录存在冲突，请刷新核对，勿重复提交。'
            : '审批结果未确认，请刷新核对，勿重复提交。',
      );
    } finally {
      inFlight.current = false;
      if (mounted.current) onBusy(false);
    }
  };
  return (
    <>
      <Fields
        entries={[
          ['退款记录 ID', row.id],
          ['凭证来源', row.source],
          ['供应商凭证编号', row.proof_id],
          ['申报客户 ID', input.user_id],
          ['原请求 ID', row.request_id],
          ['处理状态', statusLabel(row.status)],
          ['待处理原因', reasonLabel(row.pending_reason)],
          ['尝试序号', input.attempt],
          ['渠道 ID', input.channel_id],
          ['凭据版本', input.credential_version],
          ['上游请求 ID', input.upstream_request_id],
          ['上游任务 ID', input.upstream_task_id],
          ['上游账单 ID', input.upstream_bill_id],
          [
            '退款范围',
            { full: '全量退款', partial: '部分用量退款' }[input.mode] ||
              '退款范围待核实',
          ],
          ['原始凭证引用', input.evidence_reference],
          ['原扣费记录 ID', row.charge_id || '-'],
          ['退款账本流水 ID', row.ledger_id || '-'],
          ['已退额度（原始单位）', row.refunded_quota],
          ['密钥已退额度（原始单位）', row.token_refunded_quota],
          ['更新时间', timeLabel(row.updated_at)],
        ]}
      />
      {input.units?.length > 0 && (
        <>
          <h3>申报退款用量</h3>
          <div
            className='ztapi-ops-table-wrap'
            role='region'
            aria-label='申报退款用量表格'
            tabIndex={0}
          >
            <table className='ztapi-ops-table'>
              <thead>
                <tr>
                  <th scope='col'>计费项</th>
                  <th scope='col'>申报用量</th>
                </tr>
              </thead>
              <tbody>
                {input.units.map((unit, index) => (
                  <tr key={index}>
                    <td>
                      {dimensions[unit.dimension] || display(unit.dimension)}
                    </td>
                    <td>{display(unit.units)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {notice && (
        <p role='status' className='ztapi-ops-notice'>
          {notice}
        </p>
      )}
      {error && (
        <p role='alert' className='ztapi-ops-error'>
          {error}
        </p>
      )}
      {!canWrite && <p>只读权限</p>}
      {canWrite && eligible && state !== 'submitted' && (
        <form className='ztapi-reconciliation-review' onSubmit={submit}>
          <label>
            核验记录编号
            <textarea
              value={reference}
              maxLength={512}
              aria-invalid={referenceTooLong}
              aria-describedby={
                referenceTooLong ? 'reconciliation-reference-error' : undefined
              }
              disabled={state !== 'ready'}
              onChange={(event) => setReference(event.target.value)}
              required
            />
          </label>
          {referenceTooLong && (
            <p
              id='reconciliation-reference-error'
              role='alert'
              className='ztapi-ops-error'
            >
              核验记录编号过长，请缩短后提交。
            </p>
          )}
          <label className='ztapi-reconciliation-confirm'>
            <input
              type='checkbox'
              checked={confirmed}
              disabled={state !== 'ready'}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            我已核验供应商凭证及原请求归属，确认批准；实际退款以账本结果为准。
          </label>
          <button
            type='submit'
            className='ztapi-ops-primary'
            disabled={!validReference || !confirmed || state !== 'ready'}
          >
            {state === 'submitting' ? '正在提交...' : '确认核验并批准'}
          </button>
        </form>
      )}
    </>
  );
}

const attemptKinds = { billed: '收费账单', nocharge: '未收费证明' };
const identityKeys = [
  'request_id',
  'user_id',
  'attempt',
  'channel_id',
  'credential_version',
  'upstream_request_id',
];
const commonEvidenceKeys = [
  'source',
  'proof_id',
  ...identityKeys,
  'upstream_bill_id',
  'evidence_reference',
];
const tokenDimensions = [
  'input_tokens',
  'output_tokens',
  'cache_read',
  'cache_write',
  'cache_write_5m',
  'cache_write_1h',
];
const utf8Length = (value) => new TextEncoder().encode(value).length;

function parseEvidence(raw, type, identity) {
  try {
    const value = JSON.parse(raw);
    if (type === 'nocharge') {
      const textValid = (text, limit) =>
        typeof text === 'string' && text.trim() && utf8Length(text) <= limit;
      if (
        !value ||
        typeof value !== 'object' ||
        Array.isArray(value) ||
        Object.keys(value).some(
          (key) =>
            ![
              'source',
              'proof_id',
              'verification_reference',
              'attempts',
            ].includes(key),
        ) ||
        !textValid(value.source, 128) ||
        !textValid(value.proof_id, 256) ||
        !textValid(value.verification_reference, 512)
      )
        throw new Error(
          '整单未收费凭证须包含来源、编号和核验引用，不能包含金额或价格。',
        );
      const expected = [...(identity?.attempts || [])].sort(
        (a, b) => a.attempt - b.attempt,
      );
      if (
        identity?.status !== 'pending' ||
        !Array.isArray(value.attempts) ||
        expected.length < 1 ||
        expected.length > 2 ||
        value.attempts.length !== expected.length
      )
        throw new Error('须逐一提供当前待结算请求的全部尝试凭证。');
      value.attempts.forEach((attempt, index) => {
        if (
          !attempt ||
          typeof attempt !== 'object' ||
          Array.isArray(attempt) ||
          Object.keys(attempt).some(
            (key) =>
              ![
                'attempt',
                'channel_id',
                'credential_version',
                'upstream_request_id',
                'verification_reference',
              ].includes(key),
          ) ||
          ![
            'attempt',
            'channel_id',
            'credential_version',
            'upstream_request_id',
          ].every((key) => attempt[key] === expected[index][key]) ||
          !textValid(attempt.credential_version, 64) ||
          !textValid(attempt.upstream_request_id, 200) ||
          !textValid(attempt.verification_reference, 512)
        )
          throw new Error(
            '每次尝试身份须与原记录一致，且必须有独立的未收费核验引用。',
          );
      });
      return { value, error: '' };
    }
    const isAttempt = type === 'attempt';
    const allowed = [
      ...commonEvidenceKeys,
      ...(isAttempt
        ? ['kind', 'usage_semantic', 'usage', 'distinct_usage_reference']
        : ['upstream_task_id', 'mode', 'units']),
    ];
    if (
      !value ||
      typeof value !== 'object' ||
      Array.isArray(value) ||
      Object.keys(value).some((key) => !allowed.includes(key))
    )
      throw new Error('凭证包含不支持的字段，不能提交金额、价格或审批身份。');
    for (const key of [
      'source',
      'proof_id',
      'request_id',
      'credential_version',
      'evidence_reference',
    ]) {
      if (typeof value[key] !== 'string' || !value[key].trim())
        throw new Error('凭证缺少来源、编号、原请求身份或证据引用。');
    }
    for (const key of ['user_id', 'attempt', 'channel_id']) {
      if (
        !Number.isSafeInteger(value[key]) ||
        value[key] <= 0 ||
        (key === 'attempt' && value[key] > 2)
      )
        throw new Error('客户、渠道和尝试身份无效。');
    }
    if (utf8Length(JSON.stringify(value)) > 65536)
      throw new Error('凭证内容过长。');
    const limits = {
      source: 128,
      proof_id: 256,
      request_id: 128,
      credential_version: 128,
      upstream_request_id: isAttempt ? 128 : 256,
      upstream_bill_id: 256,
      upstream_task_id: 256,
      evidence_reference: 512,
      distinct_usage_reference: 512,
      usage_semantic: 64,
    };
    for (const [key, limit] of Object.entries(limits)) {
      if (
        value[key] !== undefined &&
        (typeof value[key] !== 'string' || utf8Length(value[key]) > limit)
      )
        throw new Error('凭证文字字段类型错误或长度超限。');
    }
    if (identity && identityKeys.some((key) => value[key] !== identity[key]))
      throw new Error('凭证身份与当前请求尝试不一致。');
    const lines = isAttempt ? value.usage : value.units;
    if (!Array.isArray(lines) || lines.length > (isAttempt ? 6 : 128))
      throw new Error('用量必须是有效的明细数组。');
    if (isAttempt && !Object.hasOwn(attemptKinds, value.kind))
      throw new Error('凭证类型必须为 billed 或 nocharge。');
    if (!isAttempt && !['full', 'partial'].includes(value.mode))
      throw new Error('退款范围必须为 full 或 partial。');
    const seen = new Set();
    let total = 0;
    for (const line of lines) {
      const numberKey = isAttempt ? 'quantity' : 'units';
      if (
        !line ||
        typeof line !== 'object' ||
        Array.isArray(line) ||
        Object.keys(line).some(
          (key) => !['dimension', numberKey].includes(key),
        ) ||
        typeof line.dimension !== 'string' ||
        !line.dimension ||
        seen.has(line.dimension)
      )
        throw new Error('用量字段无效或计费项重复。');
      seen.add(line.dimension);
      if (isAttempt) {
        if (
          !tokenDimensions.includes(line.dimension) ||
          !Number.isSafeInteger(line.quantity) ||
          line.quantity <= 0 ||
          line.quantity > 2147483647
        )
          throw new Error('尝试用量必须为支持计费项的正整数。');
        total += line.quantity;
      } else if (
        typeof line.units !== 'string' ||
        !/^[0-9]{1,30}(\.[0-9]{1,18})?$/.test(line.units) ||
        !/[1-9]/.test(line.units)
      )
        throw new Error(
          '退款用量须为正数十进制字符串，整数最多 30 位、小数最多 18 位。',
        );
    }
    if (isAttempt && value.kind === 'billed') {
      if (
        !['openai', 'anthropic'].includes(value.usage_semantic) ||
        total <= 0 ||
        total > 2147483647
      )
        throw new Error(
          '收费账单须提供 openai 或 anthropic 用量语义及有效用量。',
        );
      if (
        seen.has('cache_write') &&
        (seen.has('cache_write_5m') || seen.has('cache_write_1h'))
      )
        throw new Error('缓存写入总量不能与分时缓存写入量重复申报。');
    }
    if (isAttempt && value.kind === 'nocharge' && lines.length)
      throw new Error('未收费证明不能包含收费计费用量。');
    if (
      isAttempt &&
      value.usage_semantic &&
      !['openai', 'anthropic'].includes(value.usage_semantic)
    )
      throw new Error('用量语义无效。');
    if (
      !isAttempt &&
      ((value.mode === 'partial' && !lines.length) ||
        (value.mode === 'full' && lines.length))
    )
      throw new Error('部分退款须列明用量，全量退款不接受自填用量。');
    return { value, error: '' };
  } catch (error) {
    return {
      value: null,
      error:
        error instanceof SyntaxError ? '凭证 JSON 格式无效。' : error.message,
    };
  }
}

function initialEvidence(type, identity) {
  const common = { source: '', proof_id: '' };
  if (type === 'nocharge')
    return {
      ...common,
      verification_reference: '',
      attempts: [...identity.attempts]
        .sort((a, b) => a.attempt - b.attempt)
        .map((attempt) => ({
          attempt: attempt.attempt,
          channel_id: attempt.channel_id,
          credential_version: attempt.credential_version,
          upstream_request_id: attempt.upstream_request_id,
          verification_reference: '',
        })),
    };
  const owner = Object.fromEntries(
    identityKeys.map((key) => [key, identity?.[key] ?? '']),
  );
  return type === 'attempt'
    ? {
        ...common,
        ...owner,
        upstream_bill_id: '',
        kind: '',
        usage_semantic: '',
        usage: [],
        evidence_reference: '',
        distinct_usage_reference: '',
      }
    : {
        ...common,
        ...owner,
        upstream_bill_id: '',
        upstream_task_id: '',
        mode: '',
        units: [],
        evidence_reference: '',
      };
}

function evidencePayload(value, type) {
  if (type === 'nocharge') return value;
  if (type === 'attempt')
    return {
      ...value,
      usage: value.usage.map((line) => ({
        ...line,
        quantity: Number(line.quantity),
      })),
    };
  return {
    ...value,
    user_id: Number(value.user_id),
    attempt: Number(value.attempt),
    channel_id: Number(value.channel_id),
  };
}

function EvidenceFields({ type, value, disabled, onChange }) {
  const change = (key, next) => onChange({ ...value, [key]: next });
  const textField = (label, key, maxLength = 512, required = true) => (
    <label key={key}>
      {label}
      <input
        type='text'
        value={value[key] ?? ''}
        maxLength={maxLength}
        required={required}
        disabled={disabled}
        onChange={(event) => change(key, event.target.value)}
      />
    </label>
  );
  const isAttempt = type === 'attempt';
  const lines = isAttempt ? value.usage : value.units;
  const showLines = isAttempt
    ? value.kind === 'billed'
    : type === 'refund' && value.mode === 'partial';
  const updateLine = (index, next) =>
    change(
      isAttempt ? 'usage' : 'units',
      lines.map((line, i) => (i === index ? { ...line, ...next } : line)),
    );
  return (
    <div className='ztapi-reconciliation-form-fields'>
      {textField('凭证来源', 'source', 128)}
      {textField('凭证编号', 'proof_id', 256)}
      {type === 'nocharge' ? (
        <>
          {textField('整单核验引用', 'verification_reference')}
          {value.attempts.map((attempt, index) => (
            <label key={attempt.attempt}>
              尝试 {attempt.attempt} 核验引用
              <input
                value={attempt.verification_reference}
                maxLength={512}
                required
                disabled={disabled}
                onChange={(event) =>
                  change(
                    'attempts',
                    value.attempts.map((item, i) =>
                      i === index
                        ? {
                            ...item,
                            verification_reference: event.target.value,
                          }
                        : item,
                    ),
                  )
                }
              />
            </label>
          ))}
        </>
      ) : (
        <>
          {type === 'refund' && (
            <>
              {textField('原请求 ID', 'request_id', 128)}
              {['user_id', 'channel_id'].map((key) => (
                <label key={key}>
                  {key === 'user_id' ? '客户 ID' : '渠道 ID'}
                  <input
                    type='number'
                    min={1}
                    step={1}
                    value={value[key]}
                    required
                    disabled={disabled}
                    onChange={(event) => change(key, event.target.value)}
                  />
                </label>
              ))}
              <label>
                尝试序号
                <select
                  value={value.attempt}
                  required
                  disabled={disabled}
                  onChange={(event) => change('attempt', event.target.value)}
                >
                  <option value=''>请选择</option>
                  <option value='1'>第 1 次</option>
                  <option value='2'>第 2 次</option>
                </select>
              </label>
              {textField('凭据版本', 'credential_version', 128)}
              {textField('上游请求 ID', 'upstream_request_id', 256)}
              {textField('上游任务 ID', 'upstream_task_id', 256, false)}
              {textField('上游账单行编号', 'upstream_bill_id', 256, false)}
            </>
          )}
          {textField('证据引用', 'evidence_reference')}
          {isAttempt ? (
            <label>
              凭证类型
              <select
                value={value.kind}
                required
                disabled={disabled}
                onChange={(event) =>
                  onChange({
                    ...value,
                    kind: event.target.value,
                    usage: [],
                    upstream_bill_id: '',
                    usage_semantic: '',
                    distinct_usage_reference: '',
                  })
                }
              >
                <option value=''>请选择</option>
                <option value='billed'>收费账单</option>
                <option value='nocharge'>未收费证明</option>
              </select>
            </label>
          ) : (
            <label>
              退款范围
              <select
                value={value.mode}
                required
                disabled={disabled}
                onChange={(event) =>
                  onChange({ ...value, mode: event.target.value, units: [] })
                }
              >
                <option value=''>请选择</option>
                <option value='full'>全量退款</option>
                <option value='partial'>部分用量退款</option>
              </select>
            </label>
          )}
          {isAttempt && value.kind === 'billed' && (
            <>
              <label>
                用量口径
                <select
                  value={value.usage_semantic}
                  required
                  disabled={disabled}
                  onChange={(event) =>
                    change('usage_semantic', event.target.value)
                  }
                >
                  <option value=''>请选择</option>
                  <option value='openai'>OpenAI 口径</option>
                  <option value='anthropic'>Anthropic 口径</option>
                </select>
              </label>
              {textField('上游账单行编号', 'upstream_bill_id', 256)}
              {textField('独立用量核验引用', 'distinct_usage_reference')}
            </>
          )}
          {showLines && (
            <div className='ztapi-reconciliation-usage-editor'>
              {lines.map((line, index) => (
                <div className='ztapi-reconciliation-usage-row' key={index}>
                  <label>
                    计费项 {index + 1}
                    <select
                      value={line.dimension}
                      required
                      disabled={disabled}
                      onChange={(event) =>
                        updateLine(index, { dimension: event.target.value })
                      }
                    >
                      <option value=''>请选择</option>
                      {tokenDimensions.map((dimension) => (
                        <option
                          key={dimension}
                          value={dimension}
                          disabled={lines.some(
                            (other, i) =>
                              i !== index && other.dimension === dimension,
                          )}
                        >
                          {dimensions[dimension]}
                          {dimension === 'input_tokens' ? '（不含缓存）' : ''}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    {isAttempt ? '用量' : '退款用量'} {index + 1}
                    <input
                      type={isAttempt ? 'number' : 'text'}
                      inputMode={isAttempt ? 'numeric' : 'decimal'}
                      min={isAttempt ? 1 : undefined}
                      max={isAttempt ? 2147483647 : undefined}
                      step={isAttempt ? 1 : undefined}
                      value={isAttempt ? line.quantity : line.units}
                      required
                      disabled={disabled}
                      onChange={(event) =>
                        updateLine(index, {
                          [isAttempt ? 'quantity' : 'units']:
                            event.target.value,
                        })
                      }
                    />
                  </label>
                  <button
                    type='button'
                    title={`删除第 ${index + 1} 项`}
                    aria-label={`删除第 ${index + 1} 项`}
                    disabled={disabled}
                    onClick={() =>
                      change(
                        isAttempt ? 'usage' : 'units',
                        lines.filter((_, i) => i !== index),
                      )
                    }
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
              ))}
              <button
                type='button'
                disabled={disabled || lines.length >= tokenDimensions.length}
                onClick={() =>
                  change(isAttempt ? 'usage' : 'units', [
                    ...lines,
                    isAttempt
                      ? { dimension: '', quantity: '' }
                      : { dimension: '', units: '' },
                  ])
                }
              >
                <Plus size={16} />
                添加用量
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function EvidenceDialog({
  type,
  identity,
  canWrite,
  onClose,
  onSuccess,
  onBusy,
}) {
  const title =
    type === 'nocharge'
      ? '核验整单未收费'
      : type === 'attempt'
        ? '提交尝试凭证'
        : '登记退款凭证';
  const [raw, setRaw] = useState('');
  const [advanced, setAdvanced] = useState(false);
  const [fields, setFields] = useState(() => initialEvidence(type, identity));
  const [confirmed, setConfirmed] = useState(false);
  const [state, setState] = useState('ready');
  const [error, setError] = useState('');
  const dialog = useRef(null);
  const lock = useRef(false);
  const mounted = useRef(true);
  const parsed = advanced
    ? raw.trim()
      ? parseEvidence(raw, type, identity)
      : { value: null, error: '' }
    : parseEvidence(
        JSON.stringify(evidencePayload(fields, type)),
        type,
        identity,
      );
  const changeFields = (next) => {
    setFields(next);
    setConfirmed(false);
  };
  const validationError = advanced || confirmed ? parsed.error : '';
  useEffect(() => {
    mounted.current = true;
    const opener = document.activeElement;
    dialog.current?.focus();
    return () => {
      mounted.current = false;
      if (opener?.isConnected) opener.focus();
    };
  }, []);
  const close = () => {
    if (!lock.current) onClose();
  };
  const submit = async (event) => {
    event.preventDefault();
    if (
      !canWrite ||
      !parsed.value ||
      !confirmed ||
      state !== 'ready' ||
      lock.current
    )
      return;
    lock.current = true;
    setState('submitting');
    onBusy(true);
    try {
      const result = await adminRequest({
        method: 'POST',
        url:
          type === 'nocharge'
            ? `/api/admin/request-settlements/${identity.id}/confirm-no-charge`
            : type === 'attempt'
              ? '/api/admin/attempt-billing/proofs'
              : '/api/admin/supplier-refunds',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(parsed.value),
      });
      if (!mounted.current) return;
      if (
        !result?.id ||
        result.request_id !==
          (type === 'nocharge'
            ? identity.request_id
            : parsed.value.request_id) ||
        (type === 'nocharge'
          ? result.id !== identity.id
          : result.proof_id !== parsed.value.proof_id) ||
        (type === 'attempt' && result.attempt !== parsed.value.attempt)
      )
        throw new Error('Unexpected proof identity');
      onBusy(false);
      onSuccess(result);
    } catch (failure) {
      if (!mounted.current) return;
      setState('error');
      setError(
        failure.status === 403
          ? '当前账号无凭证登记权限。'
          : failure.status === 409
            ? '凭证存在冲突，请关闭并刷新核对，勿重复提交。'
            : '凭证提交结果未确认，请关闭并刷新核对，勿重复提交。',
      );
    } finally {
      lock.current = false;
      if (mounted.current) onBusy(false);
    }
  };
  return (
    <div className='ztapi-ops-backdrop'>
      <section
        ref={dialog}
        tabIndex={-1}
        role='dialog'
        aria-modal='true'
        aria-label={title}
        className='ztapi-ops-dialog ztapi-reconciliation-evidence'
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault();
            close();
          }
          if (event.key === 'Tab') {
            const controls = [
              ...dialog.current.querySelectorAll(
                'button:not(:disabled),textarea:not(:disabled),input:not(:disabled),select:not(:disabled)',
              ),
            ].filter((node) => !node.closest('[hidden]'));
            if (!controls.length) {
              event.preventDefault();
              return;
            }
            if (
              event.shiftKey &&
              (document.activeElement === controls[0] ||
                document.activeElement === dialog.current)
            ) {
              event.preventDefault();
              controls.at(-1).focus();
            } else if (
              !event.shiftKey &&
              document.activeElement === controls.at(-1)
            ) {
              event.preventDefault();
              controls[0].focus();
            }
          }
        }}
      >
        <h2>{title}</h2>
        {type === 'nocharge' && (
          <Fields
            entries={[
              ['原请求 ID', identity.request_id],
              ['客户 ID', identity.user_id],
              ['公开模型', identity.model],
              ['待核验尝试数', identity.attempts.length],
              ...identity.attempts.map((attempt) => [
                `尝试 ${attempt.attempt}`,
                `渠道 ${attempt.channel_id} / ${attempt.credential_version} / ${attempt.upstream_request_id}`,
              ]),
            ]}
          />
        )}
        {identity && type !== 'nocharge' && (
          <Fields
            entries={identityKeys.map((key, i) => [
              [
                '原请求 ID',
                '客户 ID',
                '尝试序号',
                '渠道 ID',
                '凭据版本',
                '上游请求 ID',
              ][i],
              identity[key],
            ])}
          />
        )}
        <form onSubmit={submit}>
          {!advanced && (
            <EvidenceFields
              type={type}
              value={fields}
              disabled={state !== 'ready'}
              onChange={changeFields}
            />
          )}
          <button
            type='button'
            className='ztapi-reconciliation-import-toggle'
            aria-expanded={advanced}
            aria-controls='reconciliation-import'
            disabled={state !== 'ready'}
            onClick={() => {
              setAdvanced((current) => !current);
              setRaw('');
              setConfirmed(false);
            }}
          >
            <ChevronDown size={16} />
            高级导入
          </button>
          <div id='reconciliation-import' hidden={!advanced}>
            <label>
              供应商凭证 JSON
              <textarea
                spellCheck={false}
                value={raw}
                maxLength={65536}
                disabled={state !== 'ready'}
                onChange={(event) => {
                  setRaw(event.target.value);
                  setAdvanced(true);
                  setConfirmed(false);
                }}
              />
            </label>
          </div>
          {validationError && (
            <p role='alert' className='ztapi-ops-error'>
              {validationError}
            </p>
          )}
          {advanced && parsed.value && type === 'nocharge' && (
            <Fields
              entries={[
                ['凭证来源', parsed.value.source],
                ['凭证编号', parsed.value.proof_id],
                ['整单核验引用', parsed.value.verification_reference],
                ...parsed.value.attempts.map((attempt) => [
                  `尝试 ${attempt.attempt} 核验引用`,
                  attempt.verification_reference,
                ]),
              ]}
            />
          )}
          {advanced && parsed.value && type !== 'nocharge' && (
            <Fields
              entries={[
                ['凭证来源', parsed.value.source],
                ['凭证编号', parsed.value.proof_id],
                ['申报客户 ID', parsed.value.user_id],
                ['原请求 ID', parsed.value.request_id],
                ['尝试序号', parsed.value.attempt],
                [
                  '申报类型',
                  type === 'attempt'
                    ? attemptKinds[parsed.value.kind]
                    : parsed.value.mode === 'full'
                      ? '全量退款'
                      : '部分用量退款',
                ],
                ['证据引用', parsed.value.evidence_reference],
              ]}
            />
          )}
          {error && (
            <p role='alert' className='ztapi-ops-error'>
              {error}
            </p>
          )}
          <label className='ztapi-reconciliation-confirm'>
            <input
              type='checkbox'
              checked={confirmed}
              disabled={state !== 'ready'}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            {type === 'nocharge'
              ? '我已逐一核验供应商凭证，确认该请求全部尝试均未收费，并确认申请释放原预留。'
              : type === 'attempt'
                ? '我已核对本次尝试身份和凭证；收费各项用量互不重复，输入用量不含缓存。'
                : '我已核对供应商退款凭证及原请求归属，确认提交审核。'}
          </label>
          <footer>
            <button
              type='button'
              disabled={state === 'submitting'}
              onClick={close}
            >
              取消
            </button>
            <button
              type='submit'
              className='ztapi-ops-primary'
              disabled={
                !canWrite || !parsed.value || !confirmed || state !== 'ready'
              }
            >
              {state === 'submitting'
                ? '正在提交...'
                : type === 'nocharge'
                  ? '确认未收费并提交核验'
                  : '提交待审凭证'}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}

function AttemptProofReview({ proof, canWrite, onBusy, onResult }) {
  const [reference, setReference] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [state, setState] = useState('ready');
  const [message, setMessage] = useState('');
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const input = proof.submission || {};
  const validReference =
    reference.trim() && utf8Length(reference.trim()) <= 512;
  const approve = async (event) => {
    event.preventDefault();
    if (
      !canWrite ||
      proof.status !== 'pending' ||
      !validReference ||
      !confirmed ||
      state !== 'ready' ||
      lock.current
    )
      return;
    lock.current = true;
    setState('submitting');
    onBusy(true);
    try {
      const result = await adminRequest({
        method: 'POST',
        url: `/api/admin/attempt-billing/proofs/${proof.id}/approve`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ verification_reference: reference.trim() }),
      });
      if (!mounted.current) return;
      if (
        result?.id !== proof.id ||
        result.request_id !== proof.request_id ||
        result.attempt !== proof.attempt
      )
        throw new Error('Unexpected proof identity');
      onResult(result);
      setState('submitted');
      setMessage(
        result.status === 'completed'
          ? '本次尝试凭证已处理完成；整单状态以请求账本为准。'
          : result.status === 'applied'
            ? '本次尝试已入账，账务同步仍在进行。'
            : ['pending', 'approved'].includes(result.status)
              ? '凭证审批已受理，仍待对账，未确认账务处理完成。'
              : '审批结果状态未知，请刷新核对，勿重复提交。',
      );
    } catch (failure) {
      if (!mounted.current) return;
      setState('error');
      setMessage(
        failure.status === 403
          ? '当前账号无凭证审批权限，请刷新核对。'
          : failure.status === 409
            ? '凭证存在冲突，请刷新核对，勿重复提交。'
            : '审批结果未确认，请刷新核对，勿重复提交。',
      );
    } finally {
      lock.current = false;
      if (mounted.current) onBusy(false);
    }
  };
  return (
    <section className='ztapi-reconciliation-detail' aria-label='尝试凭证审核'>
      <h2>尝试凭证审核</h2>
      <Fields
        entries={[
          ['凭证编号', proof.proof_id],
          ['来源', proof.source],
          ['处理状态', statusLabel(proof.status)],
          ['待处理原因', reasonLabel(proof.pending_reason)],
          ['原请求 ID', proof.request_id],
          ['申报客户 ID', input.user_id],
          ['尝试序号', proof.attempt],
          ['渠道 ID', input.channel_id],
          ['凭据版本', input.credential_version],
          ['上游请求 ID', input.upstream_request_id],
          ['上游账单 ID', input.upstream_bill_id],
          ['凭证类型', attemptKinds[input.kind] || '类型待核实'],
          ['用量语义', input.usage_semantic],
          ['证据引用', input.evidence_reference],
          ['独立用量证明', input.distinct_usage_reference],
          ['扣费记录 ID', proof.charge_id || '-'],
        ]}
      />
      {input.usage?.length > 0 && (
        <div
          className='ztapi-ops-table-wrap'
          role='region'
          tabIndex={0}
          aria-label='申报尝试用量表格'
        >
          <table className='ztapi-ops-table'>
            <thead>
              <tr>
                <th scope='col'>计费项</th>
                <th scope='col'>申报用量</th>
              </tr>
            </thead>
            <tbody>
              {input.usage.map((line, index) => (
                <tr key={index}>
                  <td>
                    {dimensions[line.dimension] || display(line.dimension)}
                  </td>
                  <td>{display(line.quantity)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {message && (
        <p
          role={state === 'error' ? 'alert' : 'status'}
          className={state === 'error' ? 'ztapi-ops-error' : 'ztapi-ops-notice'}
        >
          {message}
        </p>
      )}
      {!canWrite && <p>只读权限</p>}
      {canWrite && proof.status === 'pending' && state !== 'submitted' && (
        <form className='ztapi-reconciliation-review' onSubmit={approve}>
          <label>
            核验记录编号
            <textarea
              value={reference}
              maxLength={512}
              disabled={state !== 'ready'}
              onChange={(event) => {
                setReference(event.target.value);
                setConfirmed(false);
              }}
            />
          </label>
          {utf8Length(reference.trim()) > 512 && (
            <p role='alert' className='ztapi-ops-error'>
              核验记录编号过长，请缩短后提交。
            </p>
          )}
          <label className='ztapi-reconciliation-confirm'>
            <input
              type='checkbox'
              checked={confirmed}
              disabled={state !== 'ready'}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            {input.kind === 'nocharge'
              ? '我已核验本次尝试的供应商凭证及原请求归属，确认本次尝试确未收费并批准。'
              : '我已核验本次尝试的供应商凭证、用量和原请求归属，确认批准。'}
          </label>
          <button
            type='submit'
            className='ztapi-ops-primary'
            disabled={!validReference || !confirmed || state !== 'ready'}
          >
            {state === 'submitting' ? '正在提交...' : '确认批准凭证'}
          </button>
        </form>
      )}
    </section>
  );
}

function AttemptReconciliation({ canWrite, onBusy }) {
  const [view, setView] = useState('reviews');
  const [statusFilter, setStatusFilter] = useState('pending');
  const [cursors, setCursors] = useState([0]);
  const [reload, setReload] = useState(0);
  const [data, setData] = useState({ items: [], next_after_id: 0 });
  const [state, setState] = useState('loading');
  const [selected, setSelected] = useState(null);
  const [evidence, setEvidence] = useState(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const after = cursors.at(-1);
  const setWriting = useCallback(
    (value) => {
      setBusy(value);
      onBusy(value);
    },
    [onBusy],
  );
  useEffect(() => {
    if (!canWrite) {
      setEvidence(null);
      setWriting(false);
    }
  }, [canWrite, setWriting]);
  useEffect(() => {
    let active = true;
    setState('loading');
    adminRequest({
      url: `/api/admin/attempt-billing/${view}?status=${statusFilter}&after_id=${after}&limit=100`,
    })
      .then((result) => {
        if (!active) return;
        if (!Array.isArray(result?.items) || result.items.length > 100) {
          setState('error');
          return;
        }
        setData(result);
        setState('ready');
      })
      .catch(() => {
        if (active) setState('error');
      });
    return () => {
      active = false;
    };
  }, [view, statusFilter, after, reload]);
  const switchView = (next) => {
    if (busy || next === view) return;
    setSelected(null);
    setCursors([0]);
    setNotice('');
    setStatusFilter('pending');
    setView(next);
  };
  const refresh = () => {
    setSelected(null);
    setReload((n) => n + 1);
  };
  const hasNext =
    data.items.length === 100 &&
    Number.isInteger(data.next_after_id) &&
    data.next_after_id > after &&
    data.next_after_id <= 4294967295;
  return (
    <div className='ztapi-reconciliation-attempts'>
      <div className='ztapi-reconciliation-attempt-toolbar'>
        <div
          role='tablist'
          aria-label='尝试对账视图'
          className='ztapi-reconciliation-tabs'
        >
          {[
            ['reviews', '待对账尝试'],
            ['proofs', '待审凭证'],
          ].map(([key, label]) => (
            <button
              type='button'
              role='tab'
              key={key}
              id={`attempt-tab-${key}`}
              aria-controls='attempt-panel'
              tabIndex={view === key ? 0 : -1}
              aria-selected={view === key}
              disabled={busy}
              onClick={() => switchView(key)}
              onKeyDown={(event) => {
                if (
                  !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(
                    event.key,
                  )
                )
                  return;
                event.preventDefault();
                const next =
                  event.key === 'Home'
                    ? 'reviews'
                    : event.key === 'End'
                      ? 'proofs'
                      : view === 'reviews'
                        ? 'proofs'
                        : 'reviews';
                switchView(next);
                document.getElementById(`attempt-tab-${next}`)?.focus();
              }}
            >
              {label}
            </button>
          ))}
        </div>
        <label className='ztapi-reconciliation-status-filter'>
          对账状态
          <select
            value={statusFilter}
            disabled={busy}
            onChange={(event) => {
              setStatusFilter(event.target.value);
              setCursors([0]);
              setSelected(null);
              setNotice('');
            }}
          >
            {(view === 'reviews'
              ? ['pending', 'verified_billed', 'verified_nocharge', 'all']
              : ['pending', 'approved', 'applied', 'completed', 'all']
            ).map((status) => (
              <option key={status} value={status}>
                {statusLabel(status)}
              </option>
            ))}
          </select>
        </label>
        <button
          type='button'
          aria-label='刷新尝试对账'
          disabled={busy || state === 'loading'}
          onClick={refresh}
        >
          <RefreshCw size={16} />
          刷新
        </button>
      </div>
      <div
        id='attempt-panel'
        role='tabpanel'
        aria-labelledby={`attempt-tab-${view}`}
        aria-busy={state === 'loading'}
      >
        {notice && (
          <p role='status' className='ztapi-ops-notice'>
            {notice}
          </p>
        )}
        {state === 'loading' && <p role='status'>正在加载尝试对账...</p>}
        {state === 'error' && (
          <p role='alert'>尝试对账加载失败，请刷新重试。</p>
        )}
        {state === 'ready' && (
          <>
            <div
              className='ztapi-ops-table-wrap'
              role='region'
              aria-label={
                view === 'reviews' ? '待对账尝试表格' : '待审凭证表格'
              }
              tabIndex={0}
            >
              <table className='ztapi-ops-table'>
                <thead>
                  <tr>
                    {(view === 'reviews'
                      ? [
                          '原请求 ID',
                          '客户 ID',
                          '模型',
                          '尝试序号',
                          '渠道 ID',
                          '凭据版本',
                          '上游请求 ID',
                          '状态',
                          '待处理原因',
                          '操作',
                        ]
                      : [
                          '凭证编号',
                          '来源',
                          '原请求 ID',
                          '尝试序号',
                          '凭证类型',
                          '状态',
                          '待处理原因',
                          '详情',
                        ]
                    ).map((label) => (
                      <th key={label} scope='col'>
                        {label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((row) => (
                    <tr key={row.id}>
                      {(view === 'reviews'
                        ? [
                            row.request_id,
                            row.user_id,
                            row.model,
                            row.attempt,
                            row.channel_id,
                            row.credential_version,
                            row.upstream_request_id,
                            statusLabel(row.status),
                            reasonLabel(row.pending_reason),
                          ]
                        : [
                            row.proof_id,
                            row.source,
                            row.request_id,
                            row.attempt,
                            attemptKinds[row.submission?.kind] || '类型待核实',
                            statusLabel(row.status),
                            reasonLabel(row.pending_reason),
                          ]
                      ).map((value, index) => (
                        <td key={index}>{display(value)}</td>
                      ))}
                      <td>
                        <div className='ztapi-ops-row-actions'>
                          {view === 'reviews' ? (
                            canWrite && row.status === 'pending' ? (
                              <button
                                type='button'
                                disabled={busy}
                                aria-label={`提交凭证 ${row.request_id} 第${row.attempt}次`}
                                onClick={() => setEvidence(row)}
                              >
                                <FilePlus2 size={16} />
                                提交凭证
                              </button>
                            ) : (
                              '只读'
                            )
                          ) : (
                            <button
                              type='button'
                              disabled={busy}
                              aria-label={`查看凭证 ${row.proof_id}`}
                              onClick={() => setSelected(row)}
                            >
                              <Eye size={16} />
                              详情
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.items.length && (
                <p className='ztapi-ops-empty'>
                  {view === 'reviews'
                    ? '暂无待对账尝试。'
                    : '暂无待审尝试凭证。'}
                </p>
              )}
            </div>
            <nav className='ztapi-ops-pagination' aria-label='尝试对账分页'>
              <button
                type='button'
                aria-label='上一页'
                title='上一页'
                disabled={busy || cursors.length <= 1}
                onClick={() => {
                  setSelected(null);
                  setCursors((items) => items.slice(0, -1));
                }}
              >
                <ChevronLeft size={16} />
              </button>
              <span>
                第 {cursors.length} 页 · 本页 {data.items.length} 条
              </span>
              <button
                type='button'
                aria-label='下一页'
                title='下一页'
                disabled={busy || !hasNext}
                onClick={() => {
                  setSelected(null);
                  setCursors((items) => [...items, data.next_after_id]);
                }}
              >
                <ChevronRight size={16} />
              </button>
            </nav>
          </>
        )}
        {selected && (
          <AttemptProofReview
            key={selected.id}
            proof={selected}
            canWrite={canWrite}
            onBusy={setWriting}
            onResult={(result) => {
              setSelected(result);
              setData((current) => ({
                ...current,
                items: current.items.map((row) =>
                  row.id === result.id ? result : row,
                ),
              }));
            }}
          />
        )}
      </div>
      {evidence && canWrite && (
        <EvidenceDialog
          type='attempt'
          identity={evidence}
          canWrite={canWrite}
          onClose={() => setEvidence(null)}
          onBusy={setWriting}
          onSuccess={(result) => {
            setEvidence(null);
            setNotice(
              result.status === 'pending'
                ? '凭证已登记，等待财务审核；未执行扣费或释放预留。'
                : `凭证记录已返回：${statusLabel(result.status)}。请按记录核对账本。`,
            );
          }}
        />
      )}
    </div>
  );
}

export default function ReconciliationPage({ canWrite = false }) {
  const [tab, setTab] = useState('settlements');
  const [statusFilter, setStatusFilter] = useState('pending');
  const [cursors, setCursors] = useState([0]);
  const [reload, setReload] = useState(0);
  const [data, setData] = useState({ items: [], next_after_id: 0 });
  const [state, setState] = useState('loading');
  const [selected, setSelected] = useState(null);
  const [busy, setBusy] = useState(false);
  const [refundDialog, setRefundDialog] = useState(false);
  const [submissionNotice, setSubmissionNotice] = useState('');
  const heading = useRef(null);
  const opener = useRef(null);
  const after = cursors.at(-1);
  useEffect(() => {
    if (!canWrite) {
      setRefundDialog(false);
      setBusy(false);
    }
  }, [canWrite]);
  useEffect(() => {
    if (tab === 'attempts') {
      setState('ready');
      return;
    }
    let active = true;
    setState('loading');
    const endpoint =
      tab === 'settlements'
        ? 'request-settlements?'
        : `supplier-refunds?status=${statusFilter}&`;
    adminRequest({
      url: `/api/admin/${endpoint}after_id=${after}&limit=${PAGE_SIZE}`,
    })
      .then((result) => {
        if (!active) return;
        if (!Array.isArray(result?.items) || result.items.length > PAGE_SIZE) {
          setState('error');
          return;
        }
        setData(result);
        setState('ready');
      })
      .catch(() => {
        if (active) setState('error');
      });
    return () => {
      active = false;
    };
  }, [tab, statusFilter, after, reload]);
  useEffect(() => {
    if (selected) heading.current?.focus();
  }, [selected?.id]);
  const changeTab = (next) => {
    if (busy || next === tab) return;
    setSelected(null);
    setSubmissionNotice('');
    setStatusFilter('pending');
    setState('loading');
    setCursors([0]);
    setTab(next);
  };
  const refresh = () => {
    if (busy) return;
    setSelected(null);
    setReload((value) => value + 1);
  };
  const open = (row, event) => {
    if (busy) return;
    opener.current = event.currentTarget;
    setSelected(row);
  };
  const updateRefund = useCallback((result) => {
    setSelected((row) => ({ ...row, ...result }));
    setData((current) => ({
      ...current,
      items: current.items.map((row) =>
        row.id === result.id ? { ...row, ...result } : row,
      ),
    }));
  }, []);
  const hasNext =
    data.items.length === PAGE_SIZE &&
    Number.isInteger(data.next_after_id) &&
    data.next_after_id > after &&
    data.next_after_id <= 4294967295;
  const refundTab = tab === 'refunds';
  return (
    <section className='ztapi-ops-page ztapi-reconciliation'>
      <header className='ztapi-ops-heading'>
        <h1>财务对账</h1>
        <div className='ztapi-ops-actions'>
          {tab === 'refunds' && (
            <label className='ztapi-reconciliation-status-filter'>
              退款状态
              <select
                value={statusFilter}
                disabled={busy}
                onChange={(event) => {
                  setStatusFilter(event.target.value);
                  setCursors([0]);
                  setSelected(null);
                  setSubmissionNotice('');
                }}
              >
                {['pending', 'applied', 'completed', 'all'].map((status) => (
                  <option key={status} value={status}>
                    {statusLabel(status)}
                  </option>
                ))}
              </select>
            </label>
          )}
          {tab === 'refunds' && canWrite && (
            <button
              type='button'
              disabled={busy}
              onClick={() => setRefundDialog(true)}
            >
              <FilePlus2 size={16} />
              登记退款凭证
            </button>
          )}
          {tab !== 'attempts' && (
            <button
              type='button'
              aria-label='刷新对账记录'
              disabled={busy || state === 'loading'}
              onClick={refresh}
            >
              <RefreshCw size={16} />
              刷新
            </button>
          )}
        </div>
      </header>
      <div
        className='ztapi-reconciliation-tabs'
        role='tablist'
        aria-label='对账分类'
      >
        {[
          ['settlements', '待结算请求'],
          ['refunds', '供应商退款'],
          ['attempts', '尝试对账'],
        ].map(([key, label]) => (
          <button
            type='button'
            role='tab'
            id={`reconciliation-tab-${key}`}
            aria-controls='reconciliation-panel'
            aria-selected={tab === key}
            tabIndex={tab === key ? 0 : -1}
            disabled={busy}
            key={key}
            onClick={() => changeTab(key)}
            onKeyDown={(event) => {
              if (
                !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)
              )
                return;
              event.preventDefault();
              const keys = ['settlements', 'refunds', 'attempts'];
              const next =
                event.key === 'Home'
                  ? 'settlements'
                  : event.key === 'End'
                    ? 'attempts'
                    : keys[
                        (keys.indexOf(tab) +
                          (event.key === 'ArrowLeft' ? 2 : 1)) %
                          keys.length
                      ];
              changeTab(next);
              document.getElementById(`reconciliation-tab-${next}`)?.focus();
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <div
        id='reconciliation-panel'
        role='tabpanel'
        aria-labelledby={`reconciliation-tab-${tab}`}
        aria-busy={state === 'loading'}
      >
        {submissionNotice && (
          <p role='status' className='ztapi-ops-notice'>
            {submissionNotice}
          </p>
        )}
        {tab === 'attempts' && (
          <AttemptReconciliation canWrite={canWrite} onBusy={setBusy} />
        )}
        {tab !== 'attempts' && state === 'loading' && (
          <p role='status'>正在加载对账记录...</p>
        )}
        {tab !== 'attempts' && state === 'error' && (
          <p role='alert'>对账记录加载失败，请刷新重试。</p>
        )}
        {tab !== 'attempts' && state === 'ready' && (
          <>
            <div
              className='ztapi-ops-table-wrap'
              role='region'
              aria-label={refundTab ? '供应商退款表格' : '待结算请求表格'}
              tabIndex={0}
            >
              <table className='ztapi-ops-table'>
                <thead>
                  <tr>
                    {(refundTab
                      ? [
                          '凭证编号',
                          '来源',
                          '原请求 ID',
                          '状态',
                          '待处理原因',
                          '更新时间',
                          '详情',
                        ]
                      : [
                          '原请求 ID',
                          '客户 ID',
                          '模型',
                          '预留额度（原始单位）',
                          '状态',
                          '待补计费项',
                          '创建时间',
                          '详情',
                        ]
                    ).map((label) => (
                      <th scope='col' key={label}>
                        {label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((row) => (
                    <tr key={row.id}>
                      {(refundTab
                        ? [
                            row.proof_id,
                            row.source,
                            row.request_id,
                            statusLabel(row.status),
                            reasonLabel(row.pending_reason),
                            timeLabel(row.updated_at),
                          ]
                        : [
                            row.request_id,
                            row.user_id,
                            row.model,
                            row.reserved_quota,
                            statusLabel(row.status),
                            missingLabel(row.missing_dimensions),
                            timeLabel(row.created_at),
                          ]
                      ).map((value, index) => (
                        <td key={index}>{display(value)}</td>
                      ))}
                      <td>
                        <div className='ztapi-ops-row-actions'>
                          <button
                            type='button'
                            disabled={busy}
                            aria-label={
                              refundTab
                                ? `查看退款 ${row.proof_id || row.id}`
                                : `查看结算 ${row.request_id}`
                            }
                            onClick={(event) => open(row, event)}
                          >
                            <Eye size={16} />
                            详情
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.items.length && (
                <p className='ztapi-ops-empty'>
                  {refundTab ? '暂无待处理的供应商退款。' : '暂无待结算请求。'}
                </p>
              )}
            </div>
            <nav className='ztapi-ops-pagination' aria-label='对账记录分页'>
              <button
                type='button'
                aria-label='上一页'
                title='上一页'
                disabled={busy || cursors.length <= 1}
                onClick={() => {
                  setSelected(null);
                  setCursors((current) => current.slice(0, -1));
                }}
              >
                <ChevronLeft size={16} />
              </button>
              <span>
                第 {cursors.length} 页 · 本页 {data.items.length} 条
              </span>
              <button
                type='button'
                aria-label='下一页'
                title='下一页'
                disabled={busy || !hasNext}
                onClick={() => {
                  setSelected(null);
                  setCursors((current) => [...current, data.next_after_id]);
                }}
              >
                <ChevronRight size={16} />
              </button>
            </nav>
          </>
        )}
      </div>
      {selected && (
        <section
          className='ztapi-reconciliation-detail'
          aria-labelledby='reconciliation-detail-title'
        >
          <header>
            <h2 id='reconciliation-detail-title' ref={heading} tabIndex={-1}>
              {refundTab ? '退款审核详情' : '待结算详情'}
            </h2>
            <button
              type='button'
              aria-label='关闭详情'
              title='关闭详情'
              disabled={busy}
              onClick={() => {
                setSelected(null);
                opener.current?.focus();
              }}
            >
              <X size={16} />
            </button>
          </header>
          {refundTab ? (
            <RefundReview
              key={selected.id}
              row={selected}
              canWrite={canWrite}
              onResult={updateRefund}
              onBusy={setBusy}
            />
          ) : (
            <SettlementDetail
              key={selected.id}
              id={selected.id}
              canWrite={canWrite}
              onBusy={setBusy}
            />
          )}
        </section>
      )}
      {refundDialog && canWrite && (
        <EvidenceDialog
          type='refund'
          canWrite={canWrite}
          onClose={() => setRefundDialog(false)}
          onBusy={setBusy}
          onSuccess={(result) => {
            setRefundDialog(false);
            setSubmissionNotice(
              result.status === 'pending'
                ? '退款凭证已登记，等待财务审核；未执行退款。'
                : `退款凭证记录已返回：${statusLabel(result.status)}。请按记录核对账本。`,
            );
            setSelected(null);
            setReload((n) => n + 1);
          }}
        />
      )}
    </section>
  );
}
