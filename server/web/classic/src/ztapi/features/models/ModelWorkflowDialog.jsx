/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { CheckCircle2, RefreshCw, ShieldCheck, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import {
  modelProtocolOptions,
  modelProviderOptions,
  publicationBlockerLabels,
  workflowLabel,
} from './model-family.js';

const priceDimensions = Object.freeze([
  ['input_per_million', '输入 / 百万 Token', 'input_tokens'],
  ['output_per_million', '输出 / 百万 Token', 'output_tokens'],
  ['cache_read_per_million', '缓存读取 / 百万 Token', 'cache_read'],
  ['cache_write_per_million', '缓存写入 / 百万 Token', 'cache_write'],
  [
    'cache_write_5m_per_million',
    '5 分钟缓存写入 / 百万 Token',
    'cache_write_5m',
  ],
  [
    'cache_write_1h_per_million',
    '1 小时缓存写入 / 百万 Token',
    'cache_write_1h',
  ],
  ['image_unit_cost', '图片单价', 'image'],
  ['audio_unit_cost', '音频单价', 'audio'],
  ['request_unit_cost', '请求单价', 'request'],
]);

function todayValue() {
  return new Date().toISOString().slice(0, 10);
}

function parseGroups(value) {
  return [
    ...new Set(
      value
        .split(',')
        .map((group) => group.trim())
        .filter(Boolean),
    ),
  ];
}

function initialIdentity(model) {
  return {
    public_name: model.public_name || '',
    protocol: model.protocol || 'openai_compatible',
    provider_family: model.provider_family || 'openai',
    source_reference: '',
    reason: '',
    confirm: false,
  };
}

function initialPrice() {
  return {
    resource_type: 'account_pool',
    spend_tier: '0-1万元',
    currency: 'USD',
    cny_per_usd: '0',
    quotation_effective_at: todayValue(),
    source_document_checksum: '',
    reason: '',
    confirm: false,
    input_per_million: '',
    output_per_million: '',
    cache_read_per_million: '0',
    cache_write_per_million: '0',
    cache_write_5m_per_million: '0',
    cache_write_1h_per_million: '0',
    image_unit_cost: '0',
    audio_unit_cost: '0',
    request_unit_cost: '0',
  };
}

function pricePayload(model, form, confirm = false) {
  const billingDimensions = priceDimensions
    .filter(([field]) => Number(form[field]) > 0)
    .map(([, , dimension]) => dimension);
  return {
    version: model.version,
    source_model: model.source_model,
    resource_type: form.resource_type.trim(),
    spend_tier: form.spend_tier.trim(),
    billing_dimensions: billingDimensions,
    currency: form.currency,
    input_per_million: form.input_per_million || '0',
    output_per_million: form.output_per_million || '0',
    cache_read_per_million: form.cache_read_per_million || '0',
    cache_write_per_million: form.cache_write_per_million || '0',
    cache_write_5m_per_million: form.cache_write_5m_per_million || '0',
    cache_write_1h_per_million: form.cache_write_1h_per_million || '0',
    image_unit_cost: form.image_unit_cost || '0',
    audio_unit_cost: form.audio_unit_cost || '0',
    request_unit_cost: form.request_unit_cost || '0',
    cny_per_usd: form.currency === 'CNY' ? form.cny_per_usd : '0',
    quotation_effective_at: Math.floor(
      new Date(`${form.quotation_effective_at}T00:00:00Z`).getTime() / 1000,
    ),
    source_document_checksum: form.source_document_checksum.trim(),
    reason: form.reason.trim(),
    confirm,
  };
}

function publicationPayload(model, groups, published) {
  return {
    version: model.version,
    source_model: model.source_model,
    public_name: model.public_name,
    family: model.family,
    input_cost_per_million: Number(model.input_cost_per_million),
    output_cost_per_million: Number(model.output_cost_per_million),
    input_price_per_million: Number(model.input_price_per_million),
    output_price_per_million: Number(model.output_price_per_million),
    cache_read_ratio: Number(model.cache_read_ratio),
    cache_creation_ratio: Number(model.cache_creation_ratio),
    cache_creation_5m_ratio: Number(model.cache_creation_5m_ratio),
    cache_creation_1h_ratio: Number(model.cache_creation_1h_ratio),
    image_ratio: Number(model.image_ratio),
    audio_ratio: Number(model.audio_ratio),
    audio_completion_ratio: Number(model.audio_completion_ratio),
    enabled_groups: groups,
    published,
    confirm_below_cost: false,
  };
}

function channelModels(channel) {
  return String(channel?.models || '')
    .split(',')
    .map((model) => model.trim())
    .filter(Boolean);
}

export default function ModelWorkflowDialog({
  initialValue,
  onClose,
  onRefresh,
  onUpdated,
  returnFocusTo,
}) {
  const [model, setModel] = useState(initialValue);
  const [identity, setIdentity] = useState(() => initialIdentity(initialValue));
  const [price, setPrice] = useState(initialPrice);
  const [preview, setPreview] = useState(null);
  const [channels, setChannels] = useState([]);
  const [channelID, setChannelID] = useState('');
  const [groups, setGroups] = useState(() =>
    (initialValue.enabled_groups || []).join(','),
  );
  const [published, setPublished] = useState(Boolean(initialValue.published));
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const dialogRef = useRef(null);
  const firstInputRef = useRef(null);

  useEffect(() => {
    setModel(initialValue);
    setIdentity(initialIdentity(initialValue));
    setConflict(false);
  }, [initialValue]);

  useEffect(() => {
    let active = true;
    adminRequest({
      method: 'GET',
      url: '/api/channel/ztapi/?p=1&page_size=100',
    })
      .then((response) => {
        if (!active) return;
        const items = Array.isArray(response?.items) ? response.items : [];
        const sorted = [...items].sort((left, right) => {
          const leftHas = channelModels(left).includes(
            initialValue.source_model,
          );
          const rightHas = channelModels(right).includes(
            initialValue.source_model,
          );
          return Number(rightHas) - Number(leftHas);
        });
        setChannels(sorted);
        if (sorted.length > 0) setChannelID(String(sorted[0].id));
      })
      .catch(() => {
        if (active) setChannels([]);
      });
    firstInputRef.current?.focus();
    return () => {
      active = false;
      returnFocusTo?.focus?.();
    };
  }, [initialValue.source_model, returnFocusTo]);

  const blockers = Array.isArray(model.publication_blockers)
    ? model.publication_blockers
    : [];
  const blockerText = useMemo(
    () =>
      blockers.map(
        (blocker) =>
          publicationBlockerLabels[blocker] || `未知阻断项：${blocker}`,
      ),
    [blockers],
  );

  const handleKeyDown = (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusable = Array.from(
      dialogRef.current?.querySelectorAll(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
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

  const updateIdentity = (field) => (event) => {
    const value =
      event.target.type === 'checkbox'
        ? event.target.checked
        : event.target.value;
    setIdentity((current) => ({ ...current, [field]: value }));
    setError('');
  };

  const updatePrice = (field) => (event) => {
    const value =
      event.target.type === 'checkbox'
        ? event.target.checked
        : event.target.value;
    setPrice((current) => ({ ...current, [field]: value }));
    setPreview(null);
    setError('');
  };

  const run = async (name, request, successMessage) => {
    setBusy(name);
    setError('');
    setConflict(false);
    try {
      const response = await adminRequest(request);
      const refreshed = await onRefresh();
      if (refreshed) setModel(refreshed);
      onUpdated(successMessage);
      return response;
    } catch (requestError) {
      if (requestError?.status === 409) {
        setConflict(true);
      } else {
        setError('操作失败。请检查证据字段、上游状态和当前权限后重试。');
      }
      return null;
    } finally {
      setBusy('');
    }
  };

  const saveIdentity = async (event) => {
    event.preventDefault();
    if (!identity.confirm) {
      setError('请先确认公开名称、协议和供应商映射。');
      return;
    }
    await run(
      'identity',
      {
        method: 'PUT',
        url: `/api/models/ztapi/${model.id}/identity`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          version: model.version,
          source_model: model.source_model,
          ...identity,
          public_name: identity.public_name.trim(),
          source_reference: identity.source_reference.trim(),
          reason: identity.reason.trim(),
        }),
      },
      '身份映射证据已保存。',
    );
  };

  const previewPrice = async () => {
    setBusy('preview');
    setError('');
    try {
      const response = await adminRequest({
        method: 'POST',
        url: `/api/models/ztapi/${model.id}/price-preview`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(pricePayload(model, price, false)),
      });
      setPreview(response);
    } catch {
      setPreview(null);
      setError('报价预览失败。输入、输出成本和 64 位 SHA-256 校验值必须完整。');
    } finally {
      setBusy('');
    }
  };

  const importPrice = async (event) => {
    event.preventDefault();
    if (!price.confirm) {
      setError('请先确认报价来源和 40% 毛利规则。');
      return;
    }
    const response = await run(
      'price',
      {
        method: 'POST',
        url: `/api/models/ztapi/${model.id}/price-sources`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(pricePayload(model, price, true)),
      },
      '报价证据已导入，售价按 40% 毛利计算。',
    );
    if (response?.preview) setPreview(response.preview);
  };

  const verify = async () => {
    if (!channelID) {
      setError('没有可用于验证的受管上游通道。');
      return;
    }
    await run(
      'verify',
      {
        method: 'POST',
        url: `/api/models/ztapi/${model.id}/verify`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ channel_id: Number(channelID) }),
      },
      '上游非流式、流式、用量和错误分类验证已完成。',
    );
  };

  const savePublication = async (event) => {
    event.preventDefault();
    const enabledGroups = parseGroups(groups);
    if (enabledGroups.includes('all')) {
      setError('发布必须选择明确用户组，不能使用 all。');
      return;
    }
    if (published && enabledGroups.length === 0) {
      setError('发布前必须选择至少一个明确用户组。');
      return;
    }
    if (published && blockers.length > 0) {
      setError('后端证据门禁尚未通过，不能发布。');
      return;
    }
    await run(
      'publication',
      {
        method: 'PUT',
        url: `/api/models/ztapi/${model.id}`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(
          publicationPayload(model, enabledGroups, published),
        ),
      },
      published ? '模型已发布到指定用户组。' : '发布设置已保存为草稿。',
    );
  };

  const reload = async () => {
    setBusy('reload');
    setError('');
    try {
      const refreshed = await onRefresh();
      if (refreshed) {
        setModel(refreshed);
        setGroups((refreshed.enabled_groups || []).join(','));
        setPublished(Boolean(refreshed.published));
      }
      setConflict(false);
    } catch {
      setError('无法加载最新模型证据。');
    } finally {
      setBusy('');
    }
  };

  return (
    <div className='ztapi-model-dialog-layer'>
      <button
        type='button'
        className='ztapi-model-dialog-backdrop'
        aria-label='关闭模型工作流'
        onClick={onClose}
      />
      <section
        ref={dialogRef}
        className='ztapi-model-dialog ztapi-model-workflow-dialog'
        role='dialog'
        aria-modal='true'
        aria-label='模型上线工作流'
        onKeyDown={handleKeyDown}
      >
        <header>
          <div>
            <h2>{model.public_name || model.source_model}</h2>
            <p>
              {model.source_model} · {workflowLabel(model)}
            </p>
          </div>
          <button
            type='button'
            className='ztapi-model-icon-button'
            aria-label='关闭模型工作流'
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>

        <div className='ztapi-model-workflow-body'>
          <section
            className='ztapi-model-workflow-section'
            aria-labelledby='identity-title'
          >
            <div className='ztapi-model-workflow-title'>
              <span>1</span>
              <div>
                <h3 id='identity-title'>身份映射</h3>
                <p>公开名称不能暴露上游模型名。</p>
              </div>
            </div>
            <form onSubmit={saveIdentity}>
              <div className='ztapi-model-form-grid'>
                <label className='ztapi-model-wide-field'>
                  <span>公开模型名</span>
                  <input
                    ref={firstInputRef}
                    required
                    value={identity.public_name}
                    onChange={updateIdentity('public_name')}
                  />
                </label>
                <label>
                  <span>协议</span>
                  <select
                    value={identity.protocol}
                    onChange={updateIdentity('protocol')}
                  >
                    {modelProtocolOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  <span>供应商</span>
                  <select
                    value={identity.provider_family}
                    onChange={updateIdentity('provider_family')}
                  >
                    {modelProviderOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
                <label className='ztapi-model-wide-field'>
                  <span>证据来源</span>
                  <input
                    required
                    placeholder='合同、报价单或供应商文档编号'
                    value={identity.source_reference}
                    onChange={updateIdentity('source_reference')}
                  />
                </label>
                <label className='ztapi-model-wide-field'>
                  <span>变更原因</span>
                  <input
                    required
                    value={identity.reason}
                    onChange={updateIdentity('reason')}
                  />
                </label>
              </div>
              <label className='ztapi-model-confirm'>
                <input
                  type='checkbox'
                  checked={identity.confirm}
                  onChange={updateIdentity('confirm')}
                />
                <span>我已核对上游模型、协议和供应商</span>
              </label>
              <button
                type='submit'
                className='ztapi-model-secondary-button'
                disabled={Boolean(busy)}
              >
                {busy === 'identity' ? '正在保存...' : '保存身份映射'}
              </button>
            </form>
          </section>

          <section
            className='ztapi-model-workflow-section'
            aria-labelledby='price-title'
          >
            <div className='ztapi-model-workflow-title'>
              <span>2</span>
              <div>
                <h3 id='price-title'>报价证据</h3>
                <p>售价固定按可验证成本的 40% 毛利计算。</p>
              </div>
            </div>
            <form onSubmit={importPrice}>
              <div className='ztapi-model-form-grid'>
                <label>
                  <span>资源类型</span>
                  <input
                    required
                    value={price.resource_type}
                    onChange={updatePrice('resource_type')}
                  />
                </label>
                <label>
                  <span>消费档位</span>
                  <input
                    required
                    value={price.spend_tier}
                    onChange={updatePrice('spend_tier')}
                  />
                </label>
                <label>
                  <span>币种</span>
                  <select
                    value={price.currency}
                    onChange={updatePrice('currency')}
                  >
                    <option value='USD'>USD</option>
                    <option value='CNY'>CNY</option>
                  </select>
                </label>
                {price.currency === 'CNY' ? (
                  <label>
                    <span>人民币 / 美元汇率</span>
                    <input
                      type='number'
                      min='0.00000001'
                      step='any'
                      required
                      value={price.cny_per_usd}
                      onChange={updatePrice('cny_per_usd')}
                    />
                  </label>
                ) : null}
                {priceDimensions.map(([field, label]) => (
                  <label key={field}>
                    <span>{label}</span>
                    <input
                      type='number'
                      min='0'
                      step='any'
                      required={
                        field === 'input_per_million' ||
                        field === 'output_per_million'
                      }
                      value={price[field]}
                      onChange={updatePrice(field)}
                    />
                  </label>
                ))}
                <label>
                  <span>报价生效日期</span>
                  <input
                    type='date'
                    required
                    value={price.quotation_effective_at}
                    onChange={updatePrice('quotation_effective_at')}
                  />
                </label>
                <label className='ztapi-model-wide-field'>
                  <span>报价文件 SHA-256</span>
                  <input
                    required
                    minLength={64}
                    maxLength={64}
                    value={price.source_document_checksum}
                    onChange={updatePrice('source_document_checksum')}
                  />
                </label>
                <label className='ztapi-model-wide-field'>
                  <span>导入原因</span>
                  <input
                    required
                    value={price.reason}
                    onChange={updatePrice('reason')}
                  />
                </label>
              </div>
              {preview ? (
                <div className='ztapi-model-price-preview' role='status'>
                  <strong>40% 毛利预览</strong>
                  <span>
                    输入 ${preview.input_sale_usd_per_million} / 百万 Token
                  </span>
                  <span>
                    输出 ${preview.output_sale_usd_per_million} / 百万 Token
                  </span>
                </div>
              ) : null}
              <label className='ztapi-model-confirm'>
                <input
                  type='checkbox'
                  checked={price.confirm}
                  onChange={updatePrice('confirm')}
                />
                <span>我已核对报价来源和计费维度</span>
              </label>
              <div className='ztapi-model-inline-actions'>
                <button
                  type='button'
                  className='ztapi-model-secondary-button'
                  onClick={previewPrice}
                  disabled={Boolean(busy)}
                >
                  {busy === 'preview' ? '正在计算...' : '预览 40% 毛利售价'}
                </button>
                <button
                  type='submit'
                  className='ztapi-model-secondary-button'
                  disabled={Boolean(busy)}
                >
                  {busy === 'price' ? '正在导入...' : '导入报价证据'}
                </button>
              </div>
            </form>
          </section>

          <section
            className='ztapi-model-workflow-section'
            aria-labelledby='verification-title'
          >
            <div className='ztapi-model-workflow-title'>
              <span>3</span>
              <div>
                <h3 id='verification-title'>真实通道验证</h3>
                <p>验证非流式、流式、用量和错误分类。</p>
              </div>
            </div>
            <div className='ztapi-model-verification-row'>
              <label>
                <span>受管上游通道</span>
                <select
                  aria-label='受管上游通道'
                  value={channelID}
                  onChange={(event) => setChannelID(event.target.value)}
                >
                  <option value=''>请选择</option>
                  {channels.map((channel) => (
                    <option key={channel.id} value={channel.id}>
                      {channel.name}
                      {channelModels(channel).includes(model.source_model)
                        ? '（已发现该模型）'
                        : ''}
                    </option>
                  ))}
                </select>
              </label>
              <button
                type='button'
                className='ztapi-model-secondary-button'
                onClick={verify}
                disabled={Boolean(busy) || !channelID}
              >
                <ShieldCheck aria-hidden='true' size={16} />
                {busy === 'verify' ? '正在验证...' : '开始验证'}
              </button>
            </div>
          </section>

          <section
            className='ztapi-model-workflow-section'
            aria-labelledby='publication-title'
          >
            <div className='ztapi-model-workflow-title'>
              <span>4</span>
              <div>
                <h3 id='publication-title'>发布门禁</h3>
                <p>批量发现和导入永远不会自动发布。</p>
              </div>
            </div>
            {blockerText.length > 0 ? (
              <ul className='ztapi-model-blocker-list' aria-label='发布阻断项'>
                {blockerText.map((blocker) => (
                  <li key={blocker}>{blocker}</li>
                ))}
              </ul>
            ) : (
              <p className='ztapi-model-gate-ready'>
                <CheckCircle2 aria-hidden='true' size={17} />
                全部证据已通过，可以选择用户组发布。
              </p>
            )}
            <form onSubmit={savePublication}>
              <label>
                <span>明确用户组</span>
                <input
                  aria-label='明确用户组'
                  placeholder='例如 default,vip；禁止 all'
                  value={groups}
                  onChange={(event) => setGroups(event.target.value)}
                />
              </label>
              <label className='ztapi-model-switch'>
                <input
                  type='checkbox'
                  checked={published}
                  onChange={(event) => setPublished(event.target.checked)}
                />
                <span>对外发布</span>
              </label>
              <button
                type='submit'
                className='ztapi-model-primary-button'
                disabled={Boolean(busy) || (published && blockers.length > 0)}
              >
                {busy === 'publication'
                  ? '正在保存...'
                  : published
                    ? '发布到指定用户组'
                    : '保存为未发布草稿'}
              </button>
            </form>
          </section>

          {conflict ? (
            <div className='ztapi-model-conflict'>
              <p role='alert'>模型已被其他管理员更新，请重新加载后继续。</p>
              <button type='button' onClick={reload}>
                <RefreshCw aria-hidden='true' size={15} />
                重新加载最新证据
              </button>
            </div>
          ) : null}
          {error ? (
            <p role='alert' className='ztapi-model-form-error'>
              {error}
            </p>
          ) : null}
        </div>
      </section>
    </div>
  );
}
