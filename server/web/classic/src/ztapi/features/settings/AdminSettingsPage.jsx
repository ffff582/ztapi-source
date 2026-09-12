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
import { Link } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

const providerLabels = {
  stripe: 'Stripe',
  epay: '易支付',
  waffo: 'Waffo',
  waffo_pancake: 'Waffo Pancake',
};

export default function AdminSettingsPage() {
  const [snapshot, setSnapshot] = useState(null);
  const [draft, setDraft] = useState(null);
  const [state, setState] = useState('loading');
  const [pending, setPending] = useState(null);
  const [reason, setReason] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const generation = useRef(0);
  const load = useCallback(async () => {
    const current = ++generation.current;
    setState('loading');
    setError('');
    try {
      const result = await adminRequest({ url: '/api/admin/settings' });
      if (current !== generation.current) return;
      setSnapshot(result);
      setDraft(result);
      setState('ready');
    } catch {
      if (current === generation.current) setState('error');
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);
  const requestSave = (key, label) => {
    setPending({ key, label, before: snapshot[key], after: draft[key] });
    setReason('');
    setError('');
  };
  const save = async () => {
    if (saving || reason.trim().length < 2) return;
    setSaving(true);
    setError('');
    try {
      const result = await adminRequest({
        method: 'PATCH',
        url: `/api/admin/settings/${pending.key}`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          value: pending.after,
          expected_value: pending.before,
          reason: reason.trim(),
        }),
      });
      const next =
        result.brand_name !== undefined
          ? result
          : { ...snapshot, [pending.key]: result.value };
      setSnapshot(next);
      setDraft((current) => ({ ...current, [pending.key]: next[pending.key] }));
      setPending(null);
    } catch (err) {
      setError(
        err.status === 409
          ? '设置已被其他管理员修改，请刷新后重新确认。'
          : '设置保存失败，请稍后重试。',
      );
    } finally {
      setSaving(false);
    }
  };
  if (state === 'loading')
    return (
      <section className='ztapi-ops-page'>
        <p role='status'>正在加载系统设置...</p>
      </section>
    );
  if (state === 'error')
    return (
      <section className='ztapi-ops-page'>
        <p role='alert'>系统设置加载失败。</p>
        <button type='button' onClick={load} aria-label='重试加载设置'>
          <RefreshCw size={16} />
          重试
        </button>
      </section>
    );
  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>系统设置</h1>
          <p>仅开放经过服务端校验的运营配置，密钥不会出现在本页。</p>
        </div>
        <button type='button' onClick={load}>
          <RefreshCw size={16} />
          刷新
        </button>
      </header>
      <div className='ztapi-settings-grid'>
        <section className='ztapi-settings-section'>
          <h2>品牌</h2>
          <label>
            品牌名称
            <input
              aria-label='品牌名称'
              value={draft.brand_name}
              onChange={(e) =>
                setDraft({ ...draft, brand_name: e.target.value })
              }
            />
          </label>
          <button
            type='button'
            disabled={
              draft.brand_name === snapshot.brand_name ||
              !draft.brand_name.trim()
            }
            onClick={() => requestSave('brand_name', '品牌名称')}
          >
            保存品牌名称
          </button>
        </section>
        <section className='ztapi-settings-section'>
          <h2>运营公告</h2>
          <label>
            运营公告
            <textarea
              aria-label='运营公告'
              value={draft.announcement}
              maxLength={5000}
              onChange={(e) =>
                setDraft({ ...draft, announcement: e.target.value })
              }
            />
          </label>
          <button
            type='button'
            disabled={draft.announcement === snapshot.announcement}
            onClick={() => requestSave('announcement', '运营公告')}
          >
            保存运营公告
          </button>
        </section>
        <section className='ztapi-settings-section'>
          <h2>注册</h2>
          <label className='ztapi-settings-toggle'>
            <input
              type='checkbox'
              checked={draft.registration_enabled}
              onChange={(e) =>
                setDraft({ ...draft, registration_enabled: e.target.checked })
              }
            />
            允许新用户注册
          </label>
          <button
            type='button'
            disabled={
              draft.registration_enabled === snapshot.registration_enabled
            }
            onClick={() => requestSave('registration_enabled', '注册开关')}
          >
            保存注册设置
          </button>
        </section>
        <section className='ztapi-settings-section'>
          <h2>模型公开状态</h2>
          <p>已公开 {snapshot.model_publication.published_count} 个模型</p>
          <Link to={snapshot.model_publication.manage_path}>管理公开模型</Link>
        </section>
        <section className='ztapi-settings-section'>
          <h2>已配置支付提供方</h2>
          {snapshot.payments.length ? (
            snapshot.payments.map((item) => (
              <div className='ztapi-payment-row' key={item.provider}>
                <strong>
                  {providerLabels[item.provider] || item.provider}
                </strong>
                <span>最低充值 {item.minimum_topup}</span>
              </div>
            ))
          ) : (
            <p>当前没有已配置的支付提供方。</p>
          )}
        </section>
      </div>
      {pending ? (
        <div className='ztapi-ops-backdrop'>
          <section
            className='ztapi-ops-dialog'
            role='dialog'
            aria-modal='true'
            aria-label='确认保存设置'
          >
            <h2>确认保存设置</h2>
            <dl className='ztapi-settings-change'>
              <dt>设置项</dt>
              <dd>{pending.label}</dd>
              <dt>原值</dt>
              <dd>{String(pending.before)}</dd>
              <dt>新值</dt>
              <dd>{String(pending.after)}</dd>
            </dl>
            {error ? (
              <p role='alert' className='ztapi-ops-error'>
                {error}
              </p>
            ) : null}
            <label>
              保存原因
              <textarea
                aria-label='保存原因'
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <footer>
              <button
                type='button'
                disabled={saving}
                onClick={() => setPending(null)}
              >
                取消
              </button>
              <button
                className='ztapi-ops-primary'
                type='button'
                disabled={saving || reason.trim().length < 2}
                onClick={save}
              >
                {saving ? '正在保存...' : '确认保存'}
              </button>
            </footer>
          </section>
        </div>
      ) : null}
    </section>
  );
}
