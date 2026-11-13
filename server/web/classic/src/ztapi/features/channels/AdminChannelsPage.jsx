/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Activity,
  Pencil,
  PlayCircle,
  Plus,
  RefreshCw,
  Search,
  StopCircle,
  X,
} from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

const providerOptions = Object.freeze([
  { type: 1, label: 'OpenAI' },
  { type: 14, label: 'Claude' },
  { type: 24, label: 'Gemini' },
]);

const safeFailureLabels = Object.freeze({
  dns: 'DNS 解析失败',
  connect_timeout: '连接上游超时',
  authentication: '上游身份验证失败',
  rate_limit: '上游触发限流',
  upstream_response: '上游响应异常',
});

const emptyForm = Object.freeze({
  id: 0,
  name: '',
  type: 1,
  base_url: '',
  key: '',
  models: '',
  group: 'default',
  status: 2,
  priority: 0,
  weight: 1,
  model_mapping: '',
});

function providerLabel(type) {
  return (
    providerOptions.find((provider) => provider.type === Number(type))?.label ||
    '未知'
  );
}

function channelPayload(form) {
  return {
    id: Number(form.id) || undefined,
    name: form.name.trim(),
    type: Number(form.type),
    base_url: form.base_url.trim(),
    key: form.key.trim(),
    models: form.models.trim(),
    group: form.group.trim() || 'default',
    status: Number(form.status),
    priority: Number(form.priority),
    weight: Number(form.weight),
    model_mapping: form.model_mapping.trim(),
  };
}

function ChannelDialog({ mode, initialValue, onClose, onSave, returnFocusTo }) {
  const [form, setForm] = useState({ ...emptyForm, ...initialValue, key: '' });
  const [saving, setSaving] = useState(false);
  const dialogRef = useRef(null);
  const nameInputRef = useRef(null);

  useEffect(() => {
    nameInputRef.current?.focus();
    return () => returnFocusTo?.focus?.();
  }, [returnFocusTo]);

  const handleDialogKeyDown = (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusable = Array.from(
      dialogRef.current?.querySelectorAll(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
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

  const update = (field) => (event) => {
    setForm((current) => ({ ...current, [field]: event.target.value }));
  };

  const submit = async (event) => {
    event.preventDefault();
    setSaving(true);
    try {
      await onSave(form);
    } finally {
      setSaving(false);
    }
  };

  const title = mode === 'create' ? '新建上游渠道' : '编辑上游渠道';
  return (
    <div className='ztapi-channel-dialog-layer'>
      <button
        className='ztapi-channel-dialog-backdrop'
        type='button'
        aria-label='关闭渠道表单'
        onClick={onClose}
      />
      <section
        ref={dialogRef}
        className='ztapi-channel-dialog'
        role='dialog'
        aria-modal='true'
        aria-label={title}
        onKeyDown={handleDialogKeyDown}
      >
        <header>
          <h2>{title}</h2>
          <button
            type='button'
            className='ztapi-channel-icon-button'
            aria-label='关闭渠道表单'
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>
        <form onSubmit={submit}>
          <label>
            <span>渠道名称</span>
            <input
              ref={nameInputRef}
              required
              value={form.name}
              onChange={update('name')}
            />
          </label>
          <label>
            <span>上游类型</span>
            <select value={form.type} onChange={update('type')}>
              {providerOptions.map((provider) => (
                <option value={provider.type} key={provider.type}>
                  {provider.label}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>API 地址</span>
            <input
              required
              type='url'
              value={form.base_url || ''}
              onChange={update('base_url')}
            />
          </label>
          <label>
            <span>API 密钥</span>
            <input
              required={mode === 'create' && Number(form.status) === 1}
              type='password'
              autoComplete='new-password'
              value={form.key}
              onChange={update('key')}
            />
          </label>
          {mode === 'edit' ? (
            <p className='ztapi-channel-form-hint'>留空将保留现有密钥。</p>
          ) : null}
          <label>
            <span>模型</span>
            <input
              required
              value={form.models || ''}
              onChange={update('models')}
              placeholder='多个模型用英文逗号分隔'
            />
          </label>
          <label>
            <span>用户组</span>
            <input value={form.group || ''} onChange={update('group')} />
          </label>
          <label>
            <span>状态</span>
            <select value={form.status} onChange={update('status')}>
              <option value={2}>停用</option>
              <option value={1}>启用</option>
            </select>
          </label>
          <label>
            <span>优先级</span>
            <input
              required
              type='number'
              min='0'
              value={form.priority}
              onChange={update('priority')}
            />
          </label>
          <label>
            <span>权重</span>
            <input
              required
              type='number'
              min='1'
              max='1000'
              value={form.weight}
              onChange={update('weight')}
            />
          </label>
          <label>
            <span>模型映射</span>
            <textarea
              value={form.model_mapping || ''}
              onChange={update('model_mapping')}
              placeholder='例如：{"gpt-4o":"provider-model"}'
            />
          </label>
          <p className='ztapi-channel-form-hint'>
            优先级越高越先使用；相同优先级按权重分配流量。
          </p>
          <footer>
            <button
              type='button'
              className='ztapi-channel-secondary-button'
              onClick={onClose}
            >
              取消
            </button>
            <button
              type='submit'
              className='ztapi-channel-primary-button'
              disabled={saving}
            >
              {saving
                ? '正在保存...'
                : mode === 'create'
                  ? '创建渠道'
                  : '保存渠道'}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}

function ChannelActions({ channel, onEdit, onToggle, onTest, onFetchModels }) {
  const enabled = channel.status === 1;
  return (
    <div className='ztapi-channel-actions'>
      <button
        type='button'
        aria-label={`编辑 ${channel.name}`}
        title='编辑'
        onClick={(event) => onEdit(channel, event.currentTarget)}
      >
        <Pencil aria-hidden='true' size={16} />
      </button>
      <button
        type='button'
        aria-label={`${enabled ? '禁用' : '启用'} ${channel.name}`}
        title={enabled ? '禁用' : '启用'}
        onClick={() => onToggle(channel)}
      >
        {enabled ? (
          <StopCircle aria-hidden='true' size={16} />
        ) : (
          <PlayCircle aria-hidden='true' size={16} />
        )}
      </button>
      <button
        type='button'
        aria-label={`测试 ${channel.name}`}
        title='测试连通性'
        onClick={() => onTest(channel)}
      >
        <Activity aria-hidden='true' size={16} />
      </button>
      <button
        type='button'
        aria-label={`获取并导入模型 ${channel.name}`}
        title='获取上游模型并生成私有发现快照'
        onClick={() => onFetchModels(channel)}
      >
        <RefreshCw aria-hidden='true' size={16} />
      </button>
    </div>
  );
}

export default function AdminChannelsPage({ canWrite = false }) {
  const [channels, setChannels] = useState([]);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('loading');
  const [notice, setNotice] = useState('');
  const [discoveries, setDiscoveries] = useState({});
  const [dialog, setDialog] = useState(null);
  const loadGeneration = useRef(0);
  const editGeneration = useRef(0);

  const load = useCallback(async (keyword = '') => {
    const generation = ++loadGeneration.current;
    setStatus('loading');
    const trimmed = keyword.trim();
    const url = trimmed
      ? `/api/channel/ztapi/search?keyword=${encodeURIComponent(trimmed)}&p=1&page_size=20`
      : '/api/channel/ztapi/?p=1&page_size=20';
    try {
      const response = await adminRequest({ method: 'GET', url });
      if (generation !== loadGeneration.current) return;
      setChannels(Array.isArray(response?.items) ? response.items : []);
      setStatus('ready');
    } catch {
      if (generation !== loadGeneration.current) return;
      setChannels([]);
      setStatus('error');
    }
  }, []);

  useEffect(() => {
    load();
    return () => {
      loadGeneration.current += 1;
      editGeneration.current += 1;
    };
  }, [load]);

  const submitSearch = (event) => {
    event.preventDefault();
    setNotice('');
    load(query);
  };

  const openEdit = async (channel, invoker) => {
    const generation = ++editGeneration.current;
    setNotice('');
    try {
      const detail = await adminRequest({
        method: 'GET',
        url: `/api/channel/ztapi/${channel.id}`,
      });
      if (generation !== editGeneration.current) return;
      setDialog({
        mode: 'edit',
        value: { ...detail, key: '' },
        returnFocusTo: invoker,
      });
    } catch {
      if (generation !== editGeneration.current) return;
      setNotice('无法加载渠道详情。');
    }
  };

  const closeDialog = () => {
    editGeneration.current += 1;
    setDialog(null);
  };

  const openCreate = (invoker) => {
    editGeneration.current += 1;
    setDialog({
      mode: 'create',
      value: emptyForm,
      returnFocusTo: invoker,
    });
  };

  const saveChannel = async (form) => {
    const payload = channelPayload(form);
    try {
      if (dialog.mode === 'create') {
        await adminRequest({
          method: 'POST',
          url: '/api/channel/ztapi/',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        });
      } else {
        await adminRequest({
          method: 'PUT',
          url: `/api/channel/ztapi/${payload.id}`,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        });
      }
      closeDialog();
      setNotice(dialog.mode === 'create' ? '渠道已创建。' : '渠道已保存。');
      await load(query);
    } catch {
      setNotice('保存失败，请检查渠道配置后重试。');
    }
  };

  const toggleChannel = async (channel) => {
    const nextStatus = channel.status === 1 ? 2 : 1;
    try {
      await adminRequest({
        method: 'PATCH',
        url: `/api/channel/ztapi/${channel.id}/status`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: nextStatus }),
      });
      setNotice(channel.status === 1 ? '渠道已禁用。' : '渠道已启用。');
      await load(query);
    } catch {
      setNotice('渠道状态修改失败。');
    }
  };

  const testChannel = async (channel) => {
    setNotice('');
    try {
      const result = await adminRequest({
        method: 'GET',
        url: `/api/channel/ztapi/test/${channel.id}`,
      });
      if (result?.error_category) {
        setNotice(
          safeFailureLabels[result.error_category] ||
            safeFailureLabels.upstream_response,
        );
        return;
      }
      const elapsed = Number.isFinite(result?.time) ? result.time : 0;
      setNotice(`连接正常，耗时 ${elapsed} 秒。`);
      await load(query);
    } catch {
      setNotice('连接测试失败。');
    }
  };

  const fetchModels = async (channel) => {
    setNotice('');
    try {
      const result = await adminRequest({
        method: 'GET',
        url: `/api/channel/ztapi/fetch_models/${channel.id}?import=true`,
      });
      if (result?.error_category) {
        setNotice(
          safeFailureLabels[result.error_category] ||
            safeFailureLabels.upstream_response,
        );
        return;
      }
      if (!result?.snapshot_id || !Array.isArray(result?.model_ids)) {
        setNotice('上游返回结果缺少发现快照，未导入。');
        return;
      }
      setDiscoveries((current) => ({
        ...current,
        [channel.id]: { ...result, channel_name: channel.name },
      }));
      setNotice(
        `已导入 ${result.imported_count} 个模型并生成快照 #${result.snapshot_id}，全部保持未发布。`,
      );
      await load(query);
    } catch {
      setNotice('获取上游模型失败。');
    }
  };

  const discoveryValues = Object.values(discoveries).sort(
    (left, right) => Number(right.fetched_at) - Number(left.fetched_at),
  );
  const comparison =
    discoveryValues.length >= 2
      ? (() => {
          const [left, right] = discoveryValues;
          const leftModels = new Set(left.model_ids);
          const rightModels = new Set(right.model_ids);
          return {
            left,
            right,
            overlap: [...leftModels].filter((model) => rightModels.has(model))
              .length,
            leftOnly: [...leftModels].filter((model) => !rightModels.has(model))
              .length,
            rightOnly: [...rightModels].filter(
              (model) => !leftModels.has(model),
            ).length,
          };
        })()
      : null;

  return (
    <section
      className='ztapi-admin-page ztapi-channels'
      aria-labelledby='ztapi-page-title'
    >
      <div className='ztapi-channel-heading'>
        <div>
          <h1 id='ztapi-page-title'>上游渠道</h1>
          <p>管理 OpenAI、Claude 和 Gemini 上游连接。</p>
        </div>
        {canWrite ? (
          <button
            type='button'
            className='ztapi-channel-primary-button'
            onClick={(event) => openCreate(event.currentTarget)}
          >
            <Plus aria-hidden='true' size={16} />
            新建上游渠道
          </button>
        ) : (
          <span className='ztapi-channel-readonly'>只读权限</span>
        )}
      </div>
      <form className='ztapi-channel-search' onSubmit={submitSearch}>
        <Search aria-hidden='true' size={17} />
        <input
          type='search'
          aria-label='搜索上游渠道'
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder='按名称、模型或地址搜索'
        />
        <button type='submit'>搜索</button>
      </form>
      {notice ? (
        <p className='ztapi-channel-notice' role='status'>
          {notice}
        </p>
      ) : null}
      {comparison ? (
        <div
          className='ztapi-channel-discovery-comparison'
          aria-label='上游模型快照对比'
        >
          <strong>
            {comparison.left.channel_name} / {comparison.right.channel_name}
          </strong>
          <span>共同模型 {comparison.overlap} 个</span>
          <span>
            {comparison.left.channel_name} 独有 {comparison.leftOnly} 个
          </span>
          <span>
            {comparison.right.channel_name} 独有 {comparison.rightOnly} 个
          </span>
          <small>对比只用于目录治理，不会自动发布模型。</small>
        </div>
      ) : null}
      {status === 'error' ? (
        <div className='ztapi-channel-empty' role='alert'>
          无法加载上游渠道。
          <button type='button' onClick={() => load(query)}>
            重试
          </button>
        </div>
      ) : null}
      {status === 'loading' ? (
        <div className='ztapi-channel-empty' role='status'>
          正在加载上游渠道...
        </div>
      ) : null}
      {status === 'ready' && channels.length === 0 ? (
        <div className='ztapi-channel-empty'>暂无上游渠道。</div>
      ) : null}
      {status === 'ready' && channels.length > 0 ? (
        <div
          className={`ztapi-channel-table${canWrite ? '' : ' is-readonly'}`}
          role='table'
          aria-label='上游渠道列表'
        >
          <div
            className='ztapi-channel-row ztapi-channel-table-head'
            role='row'
          >
            <span>渠道</span>
            <span>类型</span>
            <span>状态</span>
            <span>模型</span>
            <span>密钥</span>
            {canWrite ? <span>操作</span> : null}
          </div>
          {channels.map((channel) => (
            <div className='ztapi-channel-row' role='row' key={channel.id}>
              <div className='ztapi-channel-name'>
                <strong>{channel.name}</strong>
                <small>{channel.base_url || '默认 API 地址'}</small>
              </div>
              <span>{providerLabel(channel.type)}</span>
              <span
                className={`ztapi-channel-status${channel.status === 1 ? ' is-enabled' : ''}`}
              >
                {channel.status === 1 ? '已启用' : '已禁用'}
              </span>
              <div className='ztapi-channel-models'>
                <span>{channel.models || '未配置'}</span>
                {discoveries[channel.id] ? (
                  <small>
                    快照 #{discoveries[channel.id].snapshot_id} · 已私有导入{' '}
                    {discoveries[channel.id].imported_count} 个
                  </small>
                ) : null}
              </div>
              <span>
                {channel.channel_info?.is_multi_key
                  ? `${channel.channel_info.multi_key_size || 0} 个密钥（已脱敏）`
                  : '密钥已脱敏'}
              </span>
              {canWrite ? (
                <ChannelActions
                  channel={channel}
                  onEdit={openEdit}
                  onToggle={toggleChannel}
                  onTest={testChannel}
                  onFetchModels={fetchModels}
                />
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
      {dialog ? (
        <ChannelDialog
          mode={dialog.mode}
          initialValue={dialog.value}
          onClose={closeDialog}
          onSave={saveChannel}
          returnFocusTo={dialog.returnFocusTo}
        />
      ) : null}
    </section>
  );
}
