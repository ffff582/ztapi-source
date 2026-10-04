/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/

import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { Activity, FileImage, History, Pencil, Search } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import ModelWorkflowDialog from './ModelWorkflowDialog.jsx';
import ModelHealthDialog from './ModelHealthDialog.jsx';
import MediaContractDialog from './MediaContractDialog.jsx';
import {
  modelProtocolOptions,
  modelProviderOptions,
  modelWorkflowOptions,
  protocolLabel,
  providerLabel,
  publicationBlockerLabels,
  workflowLabel,
  workflowState,
} from './model-family.js';

const auditLabels = Object.freeze({
  'model.identity_mapped': '完成身份映射',
  'model.price_source_imported': '导入报价证据',
  'model.verification_completed': '完成上游验证',
  'model.updated': '更新发布设置',
  'model.published': '发布模型',
});

function routeText(model) {
  const count = Number(model?.enabled_route_count) || 0;
  return model?.route_ready ? `${count} 条可信线路` : '线路未就绪';
}

function blockerSummary(model) {
  const blockers = Array.isArray(model?.publication_blockers)
    ? model.publication_blockers
    : [];
  if (blockers.length === 0) return '无阻断项';
  const first = publicationBlockerLabels[blockers[0]] || blockers[0];
  return blockers.length === 1
    ? first
    : `${first}，另有 ${blockers.length - 1} 项`;
}

function ModelRow({ canWrite, model, onEdit, onHealth, onMedia }) {
  const state = workflowState(model);
  return (
    <div className='ztapi-model-row ztapi-model-evidence-row' role='row'>
      <div className='ztapi-model-name' role='cell'>
        <div
          style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}
        >
          <strong>{model.public_name || '尚未设置公开名称'}</strong>
          <button
            type='button'
            className='ztapi-model-icon-button'
            style={{ flexShrink: 0 }}
            aria-label={`健康状态 ${model.public_name || model.source_model}`}
            title='查看模型健康状态'
            onClick={(event) => onHealth(model, event.currentTarget)}
          >
            <Activity aria-hidden='true' size={16} />
          </button>
          {model.modality === 'image' || model.modality === 'video' ? (
            <button
              type='button'
              className='ztapi-model-icon-button'
              style={{ flexShrink: 0 }}
              aria-label={`媒体证据 ${model.public_name || model.source_model}`}
              title='查看媒体报价、协议与财务证据'
              onClick={(event) => onMedia(model, event.currentTarget)}
            >
              <FileImage aria-hidden='true' size={16} />
            </button>
          ) : null}
        </div>
        <small>{model.source_model}</small>
      </div>
      <div className='ztapi-model-provider-cell' role='cell'>
        <strong>{providerLabel(model.provider_family)}</strong>
        <small>{protocolLabel(model.protocol)}</small>
      </div>
      <span role='cell' className={`ztapi-model-workflow-badge is-${state}`}>
        {workflowLabel(model)}
      </span>
      <span
        role='cell'
        className={
          model.route_ready ? 'ztapi-model-ready' : 'ztapi-model-blocked'
        }
      >
        {routeText(model)}
      </span>
      <span role='cell' className='ztapi-model-prices'>
        {model.modality === 'image' || model.modality === 'video'
          ? '按规格计费'
          : `输入 ${model.input_price_per_million || '—'} / 输出 ${
              model.output_price_per_million || '—'
            }`}
      </span>
      <span
        role='cell'
        className='ztapi-model-blocker-summary'
        title={blockerSummary(model)}
      >
        {blockerSummary(model)}
      </span>
      {canWrite ? (
        <span role='cell'>
          <button
            type='button'
            className='ztapi-model-icon-button'
            aria-label={`配置 ${model.public_name || model.source_model}`}
            title='配置上线证据'
            onClick={(event) => onEdit(model, event.currentTarget)}
          >
            <Pencil aria-hidden='true' size={16} />
          </button>
        </span>
      ) : null}
    </div>
  );
}

function formatAuditTime(timestamp) {
  if (!Number(timestamp)) return '时间未知';
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'short',
  }).format(new Date(Number(timestamp) * 1000));
}

export default function AdminModelsPage({ canWrite = false }) {
  const [models, setModels] = useState([]);
  const [auditEvents, setAuditEvents] = useState([]);
  const [query, setQuery] = useState('');
  const [provider, setProvider] = useState('all');
  const [protocol, setProtocol] = useState('all');
  const [workflow, setWorkflow] = useState('all');
  const [status, setStatus] = useState('loading');
  const [notice, setNotice] = useState('');
  const [dialog, setDialog] = useState(null);
  const [healthDialog, setHealthDialog] = useState(null);
  const [mediaDialog, setMediaDialog] = useState(null);
  const listGeneration = useRef(0);
  const detailGeneration = useRef(0);
  const auditGeneration = useRef(0);

  const load = useCallback(async () => {
    const generation = ++listGeneration.current;
    setStatus('loading');
    try {
      const response = await adminRequest({
        method: 'GET',
        url: '/api/models/ztapi/?page=1&page_size=100&keyword=',
      });
      if (generation !== listGeneration.current) return;
      setModels(Array.isArray(response?.items) ? response.items : []);
      setStatus('ready');
    } catch {
      if (generation !== listGeneration.current) return;
      setModels([]);
      setStatus('error');
    }
  }, []);

  const loadAudit = useCallback(async () => {
    const generation = ++auditGeneration.current;
    try {
      const response = await adminRequest({
        method: 'GET',
        url: '/api/models/ztapi/audit-events?page=1&page_size=8',
      });
      if (generation !== auditGeneration.current) return;
      setAuditEvents(Array.isArray(response?.items) ? response.items : []);
    } catch {
      if (generation === auditGeneration.current) setAuditEvents([]);
    }
  }, []);

  useEffect(() => {
    load();
    loadAudit();
    return () => {
      listGeneration.current += 1;
      detailGeneration.current += 1;
      auditGeneration.current += 1;
    };
  }, [load, loadAudit]);

  const visibleModels = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return models.filter((model) => {
      if (
        keyword &&
        !String(model.public_name || '')
          .toLowerCase()
          .includes(keyword) &&
        !String(model.source_model || '')
          .toLowerCase()
          .includes(keyword)
      )
        return false;
      if (provider !== 'all' && model.provider_family !== provider)
        return false;
      if (protocol !== 'all' && model.protocol !== protocol) return false;
      if (workflow !== 'all' && workflowState(model) !== workflow) return false;
      return true;
    });
  }, [models, protocol, provider, query, workflow]);

  const counts = useMemo(
    () =>
      models.reduce((result, model) => {
        const state = workflowState(model);
        result[state] = (result[state] || 0) + 1;
        return result;
      }, {}),
    [models],
  );

  const openWorkflow = async (model, invoker) => {
    const generation = ++detailGeneration.current;
    setNotice('');
    try {
      const detail = await adminRequest({
        method: 'GET',
        url: `/api/models/ztapi/${model.id}`,
      });
      if (generation !== detailGeneration.current) return;
      setDialog({ value: detail, returnFocusTo: invoker });
    } catch {
      if (generation === detailGeneration.current)
        setNotice('无法加载模型证据详情。');
    }
  };

  const closeDialog = () => {
    detailGeneration.current += 1;
    setDialog(null);
  };

  const openWorkflowFromMedia = (model) => {
    const returnFocusTo = mediaDialog?.returnFocusTo;
    setMediaDialog(null);
    openWorkflow(model, returnFocusTo);
  };

  const openHealthFromMedia = (model) => {
    const returnFocusTo = mediaDialog?.returnFocusTo;
    setMediaDialog(null);
    setHealthDialog({ value: model, returnFocusTo });
  };

  const reloadDetail = async () => {
    if (!dialog?.value?.id) return null;
    const generation = ++detailGeneration.current;
    const detail = await adminRequest({
      method: 'GET',
      url: `/api/models/ztapi/${dialog.value.id}`,
    });
    if (generation !== detailGeneration.current) return null;
    setDialog((current) => (current ? { ...current, value: detail } : current));
    return detail;
  };

  const handleUpdated = (message) => {
    setNotice(message);
    load();
    loadAudit();
  };

  return (
    <section className='ztapi-admin-page' aria-labelledby='ztapi-model-title'>
      <header className='ztapi-model-heading'>
        <div>
          <h1 id='ztapi-model-title'>模型上线管理</h1>
          <p>模型必须按发现、映射、定价、验证、发布的证据链逐步上线。</p>
        </div>
        {canWrite ? null : (
          <span className='ztapi-model-readonly'>只读权限</span>
        )}
      </header>

      <div className='ztapi-model-state-summary' aria-label='模型流程统计'>
        {modelWorkflowOptions
          .filter((option) => option.value !== 'all')
          .map((option) => (
            <button
              type='button'
              key={option.value}
              className={workflow === option.value ? 'is-active' : ''}
              onClick={() => setWorkflow(option.value)}
            >
              <span>{option.label}</span>
              <strong>{counts[option.value] || 0}</strong>
            </button>
          ))}
      </div>

      <div className='ztapi-model-filterbar'>
        <label className='ztapi-model-search'>
          <Search aria-hidden='true' size={17} />
          <input
            type='search'
            aria-label='搜索模型'
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder='搜索公开名称或上游模型名'
          />
        </label>
        <label>
          <span>供应商</span>
          <select
            aria-label='供应商筛选'
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
          >
            <option value='all'>全部供应商</option>
            {modelProviderOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <label>
          <span>协议</span>
          <select
            aria-label='协议筛选'
            value={protocol}
            onChange={(event) => setProtocol(event.target.value)}
          >
            <option value='all'>全部协议</option>
            {modelProtocolOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <label>
          <span>流程状态</span>
          <select
            aria-label='流程状态筛选'
            value={workflow}
            onChange={(event) => setWorkflow(event.target.value)}
          >
            {modelWorkflowOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {notice ? (
        <p className='ztapi-model-notice' role='status'>
          {notice}
        </p>
      ) : null}
      {status === 'loading' ? <p role='status'>正在加载模型证据...</p> : null}
      {status === 'error' ? (
        <div className='ztapi-model-empty'>
          <p role='alert'>模型证据加载失败。</p>
          <button type='button' onClick={load}>
            重试
          </button>
        </div>
      ) : null}
      {status === 'ready' && visibleModels.length === 0 ? (
        <p className='ztapi-model-empty'>没有匹配当前筛选条件的模型。</p>
      ) : null}
      {status === 'ready' && visibleModels.length > 0 ? (
        <div
          className={`ztapi-model-table ztapi-model-evidence-table${canWrite ? '' : ' is-readonly'}`}
          role='table'
          aria-label='模型上线证据列表'
        >
          <div
            className='ztapi-model-row ztapi-model-evidence-row ztapi-model-table-head'
            role='row'
          >
            <span role='columnheader'>公开名称 / 上游模型</span>
            <span role='columnheader'>供应商 / 协议</span>
            <span role='columnheader'>流程</span>
            <span role='columnheader'>可信线路</span>
            <span role='columnheader'>售价</span>
            <span role='columnheader'>首要阻断项</span>
            {canWrite ? <span role='columnheader'>操作</span> : null}
          </div>
          {visibleModels.map((model) => (
            <ModelRow
              key={model.id}
              model={model}
              canWrite={canWrite}
              onEdit={openWorkflow}
              onHealth={(value, returnFocusTo) =>
                setHealthDialog({ value, returnFocusTo })
              }
              onMedia={(value, returnFocusTo) =>
                setMediaDialog({ value, returnFocusTo })
              }
            />
          ))}
        </div>
      ) : null}

      <section
        className='ztapi-model-audit'
        aria-labelledby='ztapi-model-audit-title'
      >
        <header>
          <History aria-hidden='true' size={17} />
          <h2 id='ztapi-model-audit-title'>最近证据操作</h2>
        </header>
        {auditEvents.length === 0 ? (
          <p>暂无模型证据操作记录。</p>
        ) : (
          <div className='ztapi-model-audit-list' role='list'>
            {auditEvents.map((event) => (
              <div role='listitem' key={event.id}>
                <strong>
                  {event.public_name || `模型 #${event.model_config_id}`}
                </strong>
                <span>{auditLabels[event.action] || event.action}</span>
                <span>操作员 #{event.operator_id}</span>
                <time
                  dateTime={new Date(
                    Number(event.created_at) * 1000,
                  ).toISOString()}
                >
                  {formatAuditTime(event.created_at)}
                </time>
              </div>
            ))}
          </div>
        )}
      </section>

      {healthDialog ? (
        <ModelHealthDialog
          key={healthDialog.value.id}
          model={healthDialog.value}
          canWrite={canWrite}
          returnFocusTo={healthDialog.returnFocusTo}
          onClose={() => setHealthDialog(null)}
          onRecovered={handleUpdated}
        />
      ) : null}

      {mediaDialog ? (
        <MediaContractDialog
          key={mediaDialog.value.id}
          model={mediaDialog.value}
          canWrite={canWrite}
          returnFocusTo={mediaDialog.returnFocusTo}
          onClose={() => setMediaDialog(null)}
          onOpenWorkflow={openWorkflowFromMedia}
          onOpenHealth={openHealthFromMedia}
        />
      ) : null}

      {dialog ? (
        <ModelWorkflowDialog
          initialValue={dialog.value}
          returnFocusTo={dialog.returnFocusTo}
          onClose={closeDialog}
          onRefresh={reloadDetail}
          onUpdated={handleUpdated}
        />
      ) : null}
    </section>
  );
}
