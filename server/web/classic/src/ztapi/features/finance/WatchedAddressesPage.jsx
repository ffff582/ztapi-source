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
import { RefreshCw } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

function formatMoment(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—';
  return new Date(seconds * 1000).toLocaleString('zh-CN', { hour12: false });
}

export default function WatchedAddressesPage({ canWrite = false }) {
  const [data, setData] = useState({
    receiving_address: '',
    items: [],
    max_enabled: 0,
  });
  const [state, setState] = useState('loading');
  const [draft, setDraft] = useState({ address: '', label: '', reason: '' });
  const [notice, setNotice] = useState(null);
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setState('loading');
    try {
      const result = await adminRequest({
        url: '/api/admin/payment/watched-addresses',
      });
      if (current !== generation.current) return;
      setData({
        receiving_address: result?.receiving_address ?? '',
        items: Array.isArray(result?.items) ? result.items : [],
        max_enabled: Number(result?.max_enabled) || 0,
      });
      setState('ready');
    } catch {
      if (current === generation.current) setState('error');
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const add = async (event) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setNotice(null);
    try {
      await adminRequest({
        method: 'POST',
        url: '/api/admin/payment/watched-addresses',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          address: draft.address.trim(),
          label: draft.label.trim(),
          reason: draft.reason.trim(),
          confirm: true,
        }),
      });
      setDraft({ address: '', label: '', reason: '' });
      setNotice({ tone: 'ok', text: '已加入监听。' });
      await load();
    } catch (error) {
      setNotice({ tone: 'error', text: error?.message || '添加失败。' });
    } finally {
      setBusy(false);
    }
  };

  const toggle = async (item) => {
    if (busy) return;
    // The same reason field records why any change was made, so a change can
    // never reach the audit trail without one.
    const reason = draft.reason.trim();
    if (reason === '') {
      setNotice({ tone: 'error', text: '请先填写变更原因。' });
      return;
    }
    setBusy(true);
    setNotice(null);
    try {
      await adminRequest({
        method: 'PUT',
        url: `/api/admin/payment/watched-addresses/${item.id}`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          enabled: !item.enabled,
          reason,
          confirm: true,
        }),
      });
      setNotice({ tone: 'ok', text: '已更新。' });
      await load();
    } catch (error) {
      setNotice({ tone: 'error', text: error?.message || '更新失败。' });
    } finally {
      setBusy(false);
    }
  };

  const enabledCount = data.items.filter((item) => item.enabled).length;

  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>收款地址监听</h1>
          <p>
            监听地址只决定哪些到账能被自动入账。新订单要求客户付款的地址由部署密钥决定，无法在此修改。
          </p>
        </div>
        <div className='ztapi-ops-actions'>
          <button type='button' onClick={load}>
            <RefreshCw size={16} />
            刷新
          </button>
        </div>
      </header>

      <dl className='ztapi-ops-summary'>
        <div>
          <dt>当前收款地址（新订单）</dt>
          <dd>
            <code>{data.receiving_address || '未配置'}</code>
          </dd>
        </div>
        <div>
          <dt>已启用监听</dt>
          <dd>
            {enabledCount} / {data.max_enabled || '—'}
          </dd>
        </div>
      </dl>

      {canWrite && (
        <form className='ztapi-ops-filters' onSubmit={add}>
          <label>
            TRON 地址
            <input
              aria-label='待监听的 TRON 地址'
              value={draft.address}
              onChange={(event) =>
                setDraft({ ...draft, address: event.target.value })
              }
            />
          </label>
          <label>
            备注
            <input
              aria-label='监听地址备注'
              value={draft.label}
              onChange={(event) =>
                setDraft({ ...draft, label: event.target.value })
              }
            />
          </label>
          <label>
            变更原因
            <input
              aria-label='监听地址变更原因'
              value={draft.reason}
              onChange={(event) =>
                setDraft({ ...draft, reason: event.target.value })
              }
            />
          </label>
          <button type='submit' disabled={busy}>
            添加监听
          </button>
        </form>
      )}

      {notice && (
        <p
          role={notice.tone === 'error' ? 'alert' : 'status'}
          className={
            notice.tone === 'error' ? 'ztapi-ops-error' : 'ztapi-ops-notice'
          }
        >
          {notice.text}
        </p>
      )}

      {state === 'loading' && <p role='status'>正在加载监听地址...</p>}
      {state === 'error' && (
        <p role='alert' className='ztapi-ops-error'>
          监听地址加载失败，请刷新后重试。
        </p>
      )}
      {state === 'ready' && data.items.length === 0 && (
        <p>暂无额外监听地址，当前只监听部署配置的收款地址。</p>
      )}
      {state === 'ready' && data.items.length > 0 && (
        <table className='ztapi-ops-table'>
          <thead>
            <tr>
              <th scope='col'>地址</th>
              <th scope='col'>备注</th>
              <th scope='col'>状态</th>
              <th scope='col'>操作者</th>
              <th scope='col'>更新时间</th>
              {canWrite && <th scope='col'>操作</th>}
            </tr>
          </thead>
          <tbody>
            {data.items.map((item) => (
              <tr key={item.id}>
                <td>
                  <code>{item.address}</code>
                </td>
                <td>{item.label || '—'}</td>
                <td>{item.enabled ? '监听中' : '已停用'}</td>
                <td>{item.operator_id}</td>
                <td>{formatMoment(item.updated_at)}</td>
                {canWrite && (
                  <td>
                    <button
                      type='button'
                      disabled={busy}
                      onClick={() => toggle(item)}
                    >
                      {item.enabled ? '停用' : '启用'}
                    </button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
