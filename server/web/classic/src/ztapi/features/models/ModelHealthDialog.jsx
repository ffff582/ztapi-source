/*
Copyright (C) 2025 QuantumNous
SPDX-License-Identifier: AGPL-3.0-or-later
*/

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Activity, RefreshCw, ShieldCheck, X } from 'lucide-react';
import {
  canRecoverModelHealth,
  healthErrorMessage,
  healthEvidenceBytes,
  loadHealthWorkerStatus,
  loadModelHealth,
  recoverModelHealth,
} from './model-health.js';

const statusLabels = {
  tripped: '已熔断',
  available: '未熔断',
  unknown: '未知 / 无有效样本',
  disabled: '采集未启用',
};
const resultLabels = {
  success: '成功',
  failure: '有效失败',
  excluded: '已排除',
  unknown: '未知',
};
const ruleLabels = {
  consecutive_2: '连续 2 次有效失败',
  rolling_24h_gt_2pct: '24 小时至少 3 次失败且失败率大于 2%',
};
const sourceLabels = { real: '真实请求', probe: '合成探针' };
const kindLabels = {
  unpublish: '目录下架',
  alert: '事故告警',
  coverage: '覆盖告警',
};
const workerLabels = {
  probe: '探针',
  alert: '告警',
  worker: '工作进程',
  unpublish: '下架任务',
  coverage: '覆盖记录',
  scheduler: '调度器',
};
const workerCodes = {
  alert_recipient_missing_or_invalid: '告警接收配置缺失或无效',
  probe_identity_missing: '探针身份未配置',
  probe_identity_invalid: '探针身份无效',
  probe_identity_source_mismatch: '探针身份配置不一致',
  probe_identity_check_error: '探针身份检查失败',
  probe_budget_exhausted: '探针预算已耗尽',
};
const tabs = [
  ['overview', '概览'],
  ['events', '请求事件'],
  ['incidents', '事故与通知'],
];

function date(value) {
  if (!value || !Number.isFinite(new Date(value * 1000).getTime()))
    return '未记录';
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'medium',
  }).format(new Date(value * 1000));
}

function Datum({ label, children }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children ?? '—'}</dd>
    </div>
  );
}

function WorkerStatus({ status }) {
  return (
    <section className='ztapi-health-section' aria-label='健康工作进程状态'>
      <h3>运行状态</h3>
      {!status ? (
        <p className='ztapi-health-muted'>工作进程状态暂不可用</p>
      ) : (
        <>
          <ul className='ztapi-health-status-list'>
            {status.workers.length === 0 ? (
              <li>暂无工作进程状态记录</li>
            ) : (
              status.workers.map((worker) => (
                <li key={worker.component}>
                  <strong>
                    {workerLabels[worker.component] || worker.component}
                  </strong>
                  <span>
                    {workerCodes[worker.code] || worker.code || '未知'}
                  </span>
                  <small>{date(worker.updatedAt)}</small>
                </li>
              ))
            )}
          </ul>
          <p className='ztapi-health-muted'>
            {status.budget
              ? `探针已计入预算 $${(status.budget.accounted / 1e9).toFixed(6)} / 已分配 $${(status.budget.allocated / 1e9).toFixed(6)}`
              : '探针预算未提供'}
          </p>
        </>
      )}
    </section>
  );
}

function Coverage({ rows }) {
  const completeRows = [false, true].flatMap((stream) => {
    const modeRows = rows.filter((row) => row.stream === stream);
    return modeRows.length
      ? modeRows
      : [
          {
            stream,
            source: '',
            validSamples: 0,
            unknownSamples: null,
            lastValidAt: null,
          },
        ];
  });
  return (
    <section className='ztapi-health-section'>
      <h3>近 60 分钟覆盖</h3>
      <div className='ztapi-health-table-wrap'>
        <table>
          <thead>
            <tr>
              <th>模式 / 来源</th>
              <th>有效样本</th>
              <th>未知</th>
              <th>最近有效完成</th>
            </tr>
          </thead>
          <tbody>
            {completeRows.map((row, i) => (
              <tr key={i}>
                <td>
                  {row.stream ? '流式' : '非流式'}
                  <small>{sourceLabels[row.source] || '无有效样本'}</small>
                </td>
                <td>{row.validSamples ?? '—'}</td>
                <td>{row.unknownSamples ?? '—'}</td>
                <td>{date(row.lastValidAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Events({ rows }) {
  return (
    <section className='ztapi-health-section'>
      <h3>
        最近请求事件 <small>最多 100 条</small>
      </h3>
      {!rows.length ? (
        <p className='ztapi-health-muted'>暂无请求完成事件。</p>
      ) : (
        rows.map((row, index) => (
          <details
            className='ztapi-health-event'
            key={row.id ?? row.sequence ?? index}
            open={index === 0}
          >
            <summary>
              <strong>
                #{row.sequence ?? '—'} · {resultLabels[row.result] || '未知'}
              </strong>
              <span>
                {sourceLabels[row.source] || '来源未记录'} /{' '}
                {row.stream ? '流式' : '非流式'}
              </span>
              <time>{date(row.completedAt)}</time>
            </summary>
            <dl className='ztapi-health-details'>
              <Datum label='故障归因 / 排除原因'>
                {row.reason || '未提供'}
              </Datum>
              <Datum label='HTTP 状态'>{row.httpStatus || '未提供'}</Datum>
              <Datum label='finish_reason 原值'>
                {row.finishReasons.length
                  ? row.finishReasons.join(', ')
                  : '未提供'}
              </Datum>
              <Datum label='传输终态'>{row.terminalStatus || '未提供'}</Datum>
              <Datum label='上游请求 ID'>
                {row.upstreamRequestID || '未提供'}
              </Datum>
              <Datum label='站内请求 ID'>{row.requestID || '未提供'}</Datum>
              <Datum label='上游错误码'>
                {row.providerErrorCode || '未提供'}
              </Datum>
              <Datum label='实际渠道 / 协议'>
                {row.channelID || '未提供'} / {row.protocol || '未提供'}
              </Datum>
              <Datum label='健康代次 / 配置版本'>
                {row.generation ?? '—'} / {row.configVersion ?? '—'}
              </Datum>
              <Datum label='样本计数'>
                {row.staleGeneration
                  ? '旧代次，不计入当前健康判定'
                  : row.counted
                    ? '计入有效样本'
                    : '不计入有效样本'}
              </Datum>
            </dl>
          </details>
        ))
      )}
    </section>
  );
}

function Incidents({ health }) {
  const deliveryStatus = (row) =>
    row.status === 'done'
      ? row.kind === 'unpublish'
        ? '下架任务已完成'
        : '投递任务已完成'
      : {
          pending: row.kind === 'unpublish' ? '等待下架' : '等待发送',
          leased: '正在处理',
          superseded: '旧代次任务已失效',
        }[row.status] ||
        row.status ||
        '未知';
  return (
    <>
      <section className='ztapi-health-section'>
        <h3>
          事故记录 <small>最近 20 条</small>
        </h3>
        {!health.incidents.length ? (
          <p className='ztapi-health-muted'>暂无熔断事故。</p>
        ) : (
          health.incidents.map((row) => (
            <article className='ztapi-health-record' key={row.id}>
              <h4>
                事故 #{row.id}{' '}
                <span>{row.recoveredAt ? '已人工解除' : '尚未恢复'}</span>
              </h4>
              <p>{ruleLabels[row.rule] || row.rule || '规则未记录'}</p>
              <dl className='ztapi-health-details'>
                <Datum label='触发时间'>{date(row.openedAt)}</Datum>
                <Datum label='健康代次 / 触发事件'>
                  {row.generation ?? '—'} / #{row.triggerEventID ?? '—'}
                </Datum>
                <Datum label='触发时失败 / 有效样本'>
                  {row.failures ?? '—'} / {row.validSamples ?? '—'}
                </Datum>
                <Datum label='目录下架'>
                  {row.unpublishedAt
                    ? date(row.unpublishedAt)
                    : '待处理 / 未记录'}
                </Datum>
                <Datum label='恢复时间'>{date(row.recoveredAt)}</Datum>
                <Datum label='恢复操作员'>
                  {row.recoveryOperatorID
                    ? `#${row.recoveryOperatorID}`
                    : '未记录'}
                </Datum>
                {row.recoveryEvidence ? (
                  <Datum label='恢复证据引用'>{row.recoveryEvidence}</Datum>
                ) : null}
              </dl>
            </article>
          ))
        )}
      </section>
      <section className='ztapi-health-section'>
        <h3>
          下架与通知投递 <small>最近 50 条</small>
        </h3>
        {!health.outbox.length ? (
          <p className='ztapi-health-muted'>暂无持久任务。</p>
        ) : (
          health.outbox.map((row) => (
            <article className='ztapi-health-record' key={row.id}>
              <h4>
                {kindLabels[row.kind] || row.kind} #{row.id}
                <span>{deliveryStatus(row)}</span>
              </h4>
              <dl className='ztapi-health-details'>
                <Datum label='关联事故 / 事件'>
                  #{row.incidentID || '—'} / #{row.eventID || '—'}
                </Datum>
                <Datum label='处理次数'>{row.attempts ?? '—'}</Datum>
                <Datum label='最近失败码'>{row.lastError || '未记录'}</Datum>
                <Datum
                  label={
                    row.status === 'done' ? '任务完成时间' : '下次可重试时间'
                  }
                >
                  {date(
                    row.status === 'done' ? row.deliveredAt : row.nextAttemptAt,
                  )}
                </Datum>
              </dl>
            </article>
          ))
        )}
      </section>
    </>
  );
}

export default function ModelHealthDialog({
  model,
  canWrite = false,
  onClose,
  onRecovered,
  returnFocusTo,
}) {
  const [health, setHealth] = useState(null);
  const [workerStatus, setWorkerStatus] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState('overview');
  const [evidence, setEvidence] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const epoch = useRef(0);
  const mounted = useRef(false);
  const pending = useRef(false);
  const dialogRef = useRef(null);
  const closeRef = useRef(null);

  const load = useCallback(async () => {
    if (pending.current) return;
    const version = ++epoch.current;
    setLoading(true);
    setConfirmed(false);
    setError('');
    setWorkerStatus(null);
    loadHealthWorkerStatus()
      .catch(() => null)
      .then((status) => {
        if (mounted.current && epoch.current === version)
          setWorkerStatus(status);
      });
    try {
      const next = await loadModelHealth(model.id);
      if (!mounted.current || epoch.current !== version) return;
      setHealth(next);
      setNeedsRefresh(false);
    } catch (failure) {
      if (!mounted.current || epoch.current !== version) return;
      setHealth(null);
      setNeedsRefresh(true);
      setError(healthErrorMessage(failure));
    } finally {
      if (mounted.current && epoch.current === version) setLoading(false);
    }
  }, [model.id]);

  useEffect(() => {
    mounted.current = true;
    load();
    closeRef.current?.focus();
    return () => {
      mounted.current = false;
      epoch.current++;
      returnFocusTo?.focus?.();
    };
  }, [load, returnFocusTo]);

  const recover = async (event) => {
    event.preventDefault();
    if (
      pending.current ||
      needsRefresh ||
      loading ||
      !canRecoverModelHealth(health, canWrite) ||
      !confirmed ||
      !evidence.trim() ||
      healthEvidenceBytes(evidence) > 4096
    )
      return;
    pending.current = true;
    setBusy(true);
    setError('');
    const version = ++epoch.current;
    try {
      await recoverModelHealth(model.id, health, {
        canWrite,
        evidence,
        confirmed,
      });
      if (!mounted.current || epoch.current !== version) return;
      setNotice('熔断已解除，模型保持下架。');
      setEvidence('');
      setConfirmed(false);
      onRecovered?.('熔断已解除，模型保持下架。');
      pending.current = false;
      await load();
    } catch (failure) {
      if (!mounted.current || epoch.current !== version) return;
      setError(healthErrorMessage(failure, 'recover'));
      setConfirmed(false);
      setNeedsRefresh(true);
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  const keyDown = (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      if (!pending.current) onClose();
      return;
    }
    if (event.key !== 'Tab') return;
    const elements = Array.from(
      dialogRef.current?.querySelectorAll(
        'button:not([disabled]):not([tabindex="-1"]), textarea:not([disabled]), input:not([disabled]), summary, [tabindex="0"]',
      ) || [],
    );
    const first = elements[0];
    const last = elements.at(-1);
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    }
  };

  const recoveryAllowed = canRecoverModelHealth(health, canWrite);
  return (
    <div className='ztapi-model-dialog-layer'>
      <style>{healthStyles}</style>
      <button
        type='button'
        className='ztapi-model-dialog-backdrop'
        tabIndex={-1}
        aria-label='关闭健康状态遮罩'
        disabled={busy}
        onClick={onClose}
      />
      <section
        className='ztapi-model-dialog ztapi-health-dialog'
        role='dialog'
        aria-modal='true'
        aria-labelledby='ztapi-health-title'
        ref={dialogRef}
        onKeyDown={keyDown}
      >
        <header>
          <div>
            <h2 id='ztapi-health-title'>
              <Activity size={18} aria-hidden='true' />
              模型健康状态
            </h2>
            <p className='ztapi-health-model-name'>
              {model.public_name || model.source_model}
            </p>
          </div>
          <div className='ztapi-health-actions'>
            <button
              type='button'
              className='ztapi-model-icon-button'
              aria-label='刷新健康状态'
              title='刷新健康状态'
              disabled={loading || busy}
              onClick={load}
            >
              <RefreshCw size={17} aria-hidden='true' />
            </button>
            <button
              type='button'
              className='ztapi-model-icon-button'
              aria-label='关闭健康状态'
              title='关闭健康状态'
              disabled={busy}
              onClick={onClose}
              ref={closeRef}
            >
              <X size={18} aria-hidden='true' />
            </button>
          </div>
        </header>
        <div className='ztapi-health-body'>
          {error ? (
            <p className='ztapi-health-error' role='alert'>
              {error}
            </p>
          ) : null}
          {notice ? (
            <p className='ztapi-health-notice' role='status'>
              {notice}
            </p>
          ) : null}
          {loading ? <p role='status'>正在加载健康状态...</p> : null}
          {health && !loading ? (
            <>
              <div className='ztapi-health-state-line'>
                <strong className={`ztapi-health-state is-${health.status}`}>
                  {statusLabels[health.status]}
                </strong>
                <span>
                  {health.enabled ? '实时采集已启用' : '实时采集未启用'}
                </span>
                {!canWrite ? <span>只读权限</span> : null}
              </div>
              <dl className='ztapi-health-metrics'>
                <Datum label='24 小时有效样本'>
                  {health.window.validSamples}
                </Datum>
                <Datum label='有效失败'>{health.window.failures}</Datum>
                <Datum label='失败率'>
                  {health.window.failureRate === null
                    ? '未知'
                    : `${health.window.failureRate.toFixed(2)}%`}
                </Datum>
                <Datum label='连续有效失败'>
                  {health.state?.consecutiveFailures}
                </Datum>
              </dl>
              <p className='ztapi-health-muted'>
                窗口：({date(health.window.start)}, {date(health.window.end)}] ·
                健康代次 {health.state?.generation ?? '未记录'}
              </p>
              <div
                className='ztapi-health-tabs'
                role='tablist'
                aria-label='健康记录视图'
              >
                {tabs.map(([id, label], index) => (
                  <button
                    type='button'
                    key={id}
                    role='tab'
                    id={`ztapi-health-tab-${id}`}
                    aria-selected={tab === id}
                    aria-controls={`ztapi-health-panel-${id}`}
                    tabIndex={tab === id ? 0 : -1}
                    onClick={() => setTab(id)}
                    onKeyDown={(event) => {
                      const next =
                        event.key === 'ArrowRight'
                          ? (index + 1) % tabs.length
                          : event.key === 'ArrowLeft'
                            ? (index + tabs.length - 1) % tabs.length
                            : event.key === 'Home'
                              ? 0
                              : event.key === 'End'
                                ? tabs.length - 1
                                : null;
                      if (next !== null) {
                        event.preventDefault();
                        setTab(tabs[next][0]);
                        document
                          .getElementById(`ztapi-health-tab-${tabs[next][0]}`)
                          ?.focus();
                      }
                    }}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <div
                role='tabpanel'
                id={`ztapi-health-panel-${tab}`}
                aria-labelledby={`ztapi-health-tab-${tab}`}
              >
                {tab === 'overview' ? (
                  <>
                    <Coverage rows={health.coverage} />
                    <WorkerStatus status={workerStatus} />
                    {health.state?.open && canWrite ? (
                      <section className='ztapi-health-section'>
                        <h3>
                          <ShieldCheck size={17} aria-hidden='true' />
                          人工恢复
                        </h3>
                        <form onSubmit={recover}>
                          <label htmlFor='ztapi-health-evidence'>
                            复验记录 / 证据引用（必填）
                          </label>
                          <textarea
                            id='ztapi-health-evidence'
                            required
                            rows={4}
                            maxLength={4096}
                            value={evidence}
                            disabled={busy}
                            onChange={(event) => {
                              setEvidence(event.target.value);
                              setConfirmed(false);
                            }}
                          />
                          <small className='ztapi-health-muted'>
                            {healthEvidenceBytes(evidence)} / 4096 字节
                          </small>
                          <label className='ztapi-health-confirm'>
                            <input
                              type='checkbox'
                              checked={confirmed}
                              disabled={
                                busy || needsRefresh || !recoveryAllowed
                              }
                              onChange={(event) =>
                                setConfirmed(event.target.checked)
                              }
                            />
                            已完成人工复验，确认解除熔断但保持下架
                          </label>
                          {!recoveryAllowed ? (
                            <p className='ztapi-health-error'>
                              健康代次不可用，无法提交恢复。
                            </p>
                          ) : null}
                          <button
                            type='submit'
                            className='ztapi-model-primary-button'
                            disabled={
                              busy ||
                              needsRefresh ||
                              !recoveryAllowed ||
                              !confirmed ||
                              !evidence.trim() ||
                              healthEvidenceBytes(evidence) > 4096
                            }
                          >
                            {busy ? '正在提交...' : '解除熔断（保持下架）'}
                          </button>
                        </form>
                      </section>
                    ) : null}
                  </>
                ) : tab === 'events' ? (
                  <Events rows={health.events} />
                ) : (
                  <Incidents health={health} />
                )}
              </div>
            </>
          ) : null}
        </div>
      </section>
    </div>
  );
}

const healthStyles = `
.ztapi-health-dialog { width:min(920px,100%); font-size:13px; color:#263445; }
.ztapi-health-dialog * { box-sizing:border-box; letter-spacing:0; }
.ztapi-health-dialog h2,.ztapi-health-dialog h3 { display:flex; align-items:center; gap:8px; }
.ztapi-health-dialog header > div:first-child { min-width:0; }
.ztapi-health-dialog .ztapi-health-model-name { overflow-wrap:anywhere; font-size:14px; color:#536272; }
.ztapi-health-actions { display:flex; gap:6px; flex:none; }
.ztapi-health-dialog button:disabled { cursor:not-allowed; opacity:.55; }
.ztapi-health-body { padding:18px; }
.ztapi-health-state-line { display:flex; flex-wrap:wrap; gap:12px; align-items:center; }
.ztapi-health-state { color:#76590d; }
.ztapi-health-state.is-tripped { color:#b42318; }
.ztapi-health-state.is-available { color:#216e4e; }
.ztapi-health-state.is-disabled,.ztapi-health-muted { color:#64748b; }
.ztapi-health-metrics { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:16px; margin:20px 0 12px; }
.ztapi-health-dialog dt { color:#64748b; font-size:12px; margin-bottom:5px; }
.ztapi-health-dialog dd { margin:0; overflow-wrap:anywhere; white-space:pre-wrap; }
.ztapi-health-metrics dd { color:#172033; font-size:23px; font-weight:600; }
.ztapi-health-muted { margin:8px 0; font-size:12px; overflow-wrap:anywhere; }
.ztapi-health-tabs { display:flex; gap:20px; border-bottom:1px solid #dce2e8; margin-top:18px; }
.ztapi-health-tabs button { background:none; border:0; border-bottom:2px solid transparent; padding:12px 0; color:#536272; cursor:pointer; }
.ztapi-health-tabs button[aria-selected=true] { border-color:#087ea4; color:#075d7d; font-weight:600; }
.ztapi-health-section { border-bottom:1px solid #e5eaef; padding:18px 0; }
.ztapi-health-section:last-child { border-bottom:0; }
.ztapi-health-section h3 { margin:0 0 12px; font-size:14px; }
.ztapi-health-section h3 small { color:#64748b; font-size:12px; font-weight:400; }
.ztapi-health-table-wrap { overflow:auto; }
.ztapi-health-dialog table { width:100%; border-collapse:collapse; font-size:12px; }
.ztapi-health-dialog th,.ztapi-health-dialog td { text-align:left; padding:9px 8px; border-bottom:1px solid #eef1f4; overflow-wrap:anywhere; }
.ztapi-health-dialog th { color:#536272; font-weight:500; }
.ztapi-health-dialog td small { display:block; color:#64748b; margin-top:3px; }
.ztapi-health-status-list { list-style:none; margin:0; padding:0; }
.ztapi-health-status-list li { display:flex; flex-wrap:wrap; gap:8px 12px; padding:5px 0; overflow-wrap:anywhere; }
.ztapi-health-status-list small { color:#64748b; margin-left:auto; }
.ztapi-health-dialog form { padding:0; gap:9px; }
.ztapi-health-dialog textarea { width:100%; resize:vertical; min-height:94px; padding:10px; border:1px solid #bfc9d4; border-radius:6px; background:white; color:#172033; font:inherit; }
.ztapi-health-confirm { display:flex; align-items:flex-start; gap:8px; margin:8px 0; }
.ztapi-health-confirm input { flex:none; margin-top:3px; }
.ztapi-health-dialog form button { justify-self:start; }
.ztapi-health-error { color:#b42318; padding:10px 0; margin:0; overflow-wrap:anywhere; }
.ztapi-health-notice { color:#216e4e; padding:10px 0; margin:0; }
.ztapi-health-record { border-top:1px solid #e5eaef; padding:14px 0; }
.ztapi-health-record h4 { margin:0; display:flex; flex-wrap:wrap; justify-content:space-between; gap:8px; font-size:13px; }
.ztapi-health-record h4 span { font-weight:400; color:#536272; }
.ztapi-health-details { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px 22px; margin:14px 0 0; }
.ztapi-health-details > div { min-width:0; }
.ztapi-health-event { border-top:1px solid #e5eaef; padding:12px 0; }
.ztapi-health-event summary { cursor:pointer; overflow-wrap:anywhere; }
.ztapi-health-event summary span { margin-left:12px; color:#536272; }
.ztapi-health-event summary time { display:block; color:#64748b; margin:5px 0 0 16px; font-size:12px; }
@media(max-width:560px) {
  .ztapi-health-body { padding:14px; }
  .ztapi-health-metrics { grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px; }
  .ztapi-health-details { grid-template-columns:minmax(0,1fr); }
  .ztapi-health-tabs { justify-content:space-between; gap:10px; }
  .ztapi-health-dialog header { padding:14px; }
}
`;
