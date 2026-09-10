/*
Copyright (C) 2025 QuantumNous
SPDX-License-Identifier: AGPL-3.0-or-later
*/

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Activity, Pencil, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import { loadModelHealth } from './model-health.js';

const modalityLabels = { image: '图片生成', video: '视频生成' };
const policyLabels = {
  enterprise_40_margin: '企业 40% 毛利',
  pool_discount_margin: '号池折扣定价',
  quoted_sale_price: '报价单售价',
};
const healthLabels = {
  tripped: '已熔断',
  available: '运行正常',
  unknown: '暂无有效样本',
  disabled: '健康采集未启用',
};
const resultLabels = {
  success: '成功',
  failure: '有效失败',
  excluded: '已排除',
  unknown: '未知',
};

function date(value) {
  if (!Number(value)) return '未记录';
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'short',
  }).format(new Date(Number(value) * 1000));
}

function Datum({ label, children, code = false }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className={code ? 'ztapi-media-contract-code' : ''}>
        {children || '未记录'}
      </dd>
    </div>
  );
}

export default function MediaContractDialog({
  model,
  canWrite = false,
  onClose,
  onOpenWorkflow,
  onOpenHealth,
  returnFocusTo,
}) {
  const [contract, setContract] = useState(null);
  const [health, setHealth] = useState(null);
  const [status, setStatus] = useState('loading');
  const [healthUnavailable, setHealthUnavailable] = useState(false);
  const dialogRef = useRef(null);
  const closeButtonRef = useRef(null);

  useEffect(() => {
    let active = true;
    setStatus('loading');
    Promise.allSettled([
      adminRequest({
        method: 'GET',
        url: `/api/models/ztapi/${model.id}/media-contract`,
      }),
      loadModelHealth(model.id),
    ]).then(([contractResult, healthResult]) => {
      if (!active) return;
      if (contractResult.status === 'rejected') {
        setStatus('error');
        return;
      }
      setContract(contractResult.value);
      if (healthResult.status === 'fulfilled') {
        setHealth(healthResult.value);
      } else {
        setHealthUnavailable(true);
      }
      setStatus('ready');
    });
    closeButtonRef.current?.focus();
    return () => {
      active = false;
      returnFocusTo?.focus?.();
    };
  }, [model.id, returnFocusTo]);

  const latestEvent = useMemo(() => health?.events?.[0] || null, [health]);

  const handleKeyDown = (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusable = Array.from(
      dialogRef.current?.querySelectorAll(
        'button:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) || [],
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <div className='ztapi-model-dialog-layer'>
      <button
        type='button'
        className='ztapi-model-dialog-backdrop'
        aria-label='关闭媒体产品证据'
        onClick={onClose}
      />
      <section
        ref={dialogRef}
        className='ztapi-model-dialog ztapi-media-contract-dialog'
        role='dialog'
        aria-modal='true'
        aria-label='媒体产品证据'
        onKeyDown={handleKeyDown}
      >
        <header>
          <div>
            <h2>{model.public_name || model.source_model}</h2>
            <p>{modalityLabels[model.modality] || '媒体产品'} · 商业证据快照</p>
          </div>
          <button
            ref={closeButtonRef}
            type='button'
            className='ztapi-model-icon-button'
            aria-label='关闭媒体产品证据'
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>

        <div className='ztapi-media-contract-body'>
          {status === 'loading' ? (
            <p role='status'>正在加载媒体产品证据...</p>
          ) : null}
          {status === 'error' ? (
            <p role='alert'>媒体产品证据加载失败。</p>
          ) : null}
          {status === 'ready' && contract ? (
            <>
              <section
                className='ztapi-media-contract-section'
                aria-labelledby='media-quote-title'
              >
                <h3 id='media-quote-title'>报价与冻结价格</h3>
                <dl>
                  <Datum label='报价位置'>
                    {contract.quotation_sheet && contract.quotation_cell
                      ? `${contract.quotation_sheet} / ${contract.quotation_cell}`
                      : ''}
                  </Datum>
                  <Datum label='报价标签'>{contract.quotation_label}</Datum>
                  <Datum label='资源 / 币种'>
                    {[contract.quotation_resource, contract.quotation_currency]
                      .filter(Boolean)
                      .join(' / ')}
                  </Datum>
                  <Datum label='定价政策'>
                    {policyLabels[contract.price_policy] ||
                      contract.price_policy}
                  </Datum>
                  <Datum label='价格证据版本'>
                    {contract.price_source_version
                      ? `v${contract.price_source_version}`
                      : ''}
                  </Datum>
                  <Datum label='冻结价格版本'>
                    {contract.frozen_pricing_version}
                  </Datum>
                  <Datum label='报价生效时间'>
                    {date(contract.quotation_effective_at)}
                  </Datum>
                  <Datum label='报价文件校验值' code>
                    {contract.source_document_checksum}
                  </Datum>
                </dl>
              </section>

              <section
                className='ztapi-media-contract-section'
                aria-labelledby='media-protocol-title'
              >
                <h3 id='media-protocol-title'>协议与财务状态</h3>
                <dl>
                  <Datum label='协议证据 SHA-256' code>
                    {contract.protocol_evidence_sha256}
                  </Datum>
                  <Datum label='待对账'>
                    {contract.pending_reconciliation_count > 0
                      ? `${contract.pending_reconciliation_count} 笔待对账`
                      : '无待对账记录'}
                  </Datum>
                  <Datum label='发布状态'>
                    {contract.published ? '已发布' : '未发布'}
                  </Datum>
                </dl>
              </section>

              <section
                className='ztapi-media-contract-section'
                aria-labelledby='media-health-title'
              >
                <h3 id='media-health-title'>最新健康结果</h3>
                {healthUnavailable ? <p>健康状态暂时无法读取。</p> : null}
                {health ? (
                  <dl>
                    <Datum label='当前状态'>
                      {healthLabels[health.status] || health.status}
                    </Datum>
                    <Datum label='最近结果'>
                      {latestEvent
                        ? resultLabels[latestEvent.result] || latestEvent.result
                        : '暂无有效健康事件'}
                    </Datum>
                    <Datum label='完成原因'>
                      {latestEvent?.finishReasons?.join(', ')}
                    </Datum>
                    <Datum label='失败原因'>{latestEvent?.reason}</Datum>
                    <Datum label='最近完成'>
                      {date(latestEvent?.completedAt)}
                    </Datum>
                  </dl>
                ) : null}
              </section>

              {canWrite ? (
                <footer className='ztapi-media-contract-actions'>
                  <button
                    type='button'
                    className='ztapi-model-secondary-button'
                    onClick={() => onOpenHealth(model)}
                  >
                    <Activity aria-hidden='true' size={16} />
                    打开健康恢复
                  </button>
                  <button
                    type='button'
                    className='ztapi-model-primary-button'
                    onClick={() => onOpenWorkflow(model)}
                  >
                    <Pencil aria-hidden='true' size={16} />
                    打开发布工作流
                  </button>
                </footer>
              ) : (
                <p className='ztapi-model-readonly'>只读权限</p>
              )}
            </>
          ) : null}
        </div>
      </section>
    </div>
  );
}
