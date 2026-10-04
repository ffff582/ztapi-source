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
import { Download, RefreshCw } from 'lucide-react';
import { adminDownload, adminRequest } from '../../auth/admin-session.js';

const actionLabels = {
  'topup.complete': '完成充值订单',
  'topup.reject': '拒绝充值订单',
  'settings.update': '修改系统设置',
  'balance.adjustment': '调整用户余额',
  'user.status_update': '修改用户状态',
  'user.role_update': '修改员工角色',
  'export.orders': '导出充值订单',
  'export.ledger': '导出余额账本',
  'export.request_logs': '导出请求日志',
  'export.audit_logs': '导出审计日志',
};
function defaults() {
  const to = Math.floor(Date.now() / 1000);
  return { action: '', operatorId: '', requestId: '', from: to - 86400, to };
}
function auditQuery(filters, page = 1) {
  const p = new URLSearchParams();
  if (filters.action) p.set('action', filters.action);
  if (filters.operatorId.trim())
    p.set('operator_id', filters.operatorId.trim());
  if (filters.requestId.trim()) p.set('request_id', filters.requestId.trim());
  p.set('from', filters.from);
  p.set('to', filters.to);
  p.set('p', String(page));
  p.set('page_size', '20');
  return p;
}
function reason(params = {}) {
  return params.reason || params.result || '-';
}

export default function AuditLogPage() {
  const first = useRef(defaults()).current;
  const [draft, setDraft] = useState(first);
  const [filters, setFilters] = useState(first);
  const [data, setData] = useState({ items: [] });
  const [state, setState] = useState('loading');
  const generation = useRef(0);
  const load = useCallback(
    async (next = filters, page = 1) => {
      const current = ++generation.current;
      setState('loading');
      try {
        const result = await adminRequest({
          url: `/api/admin/audit-logs?${auditQuery(next, page)}`,
        });
        if (current !== generation.current) return;
        setData(result);
        setState('ready');
      } catch {
        if (current === generation.current) setState('error');
      }
    },
    [filters],
  );
  useEffect(() => {
    load(filters);
  }, []);
  const submit = (e) => {
    e.preventDefault();
    const next = { ...draft };
    setFilters(next);
    load(next);
  };
  const download = () =>
    adminDownload(
      { url: `/api/admin/audit-logs/export?${auditQuery(filters)}` },
      'ztapi-audit-logs.csv',
    );
  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>管理员审计</h1>
          <p>只读记录，详情只展示服务端白名单字段。</p>
        </div>
        <div className='ztapi-ops-actions'>
          <button type='button' onClick={() => load(filters)}>
            <RefreshCw size={16} />
            刷新
          </button>
          <button type='button' aria-label='下载审计 CSV' onClick={download}>
            <Download size={16} />
            导出 CSV
          </button>
        </div>
      </header>
      <form className='ztapi-ops-filters' onSubmit={submit}>
        <label>
          审计动作
          <select
            aria-label='审计动作'
            value={draft.action}
            onChange={(e) => setDraft({ ...draft, action: e.target.value })}
          >
            <option value=''>全部</option>
            <option value='topup.complete'>完成充值</option>
            <option value='topup.reject'>拒绝充值</option>
            <option value='settings.update'>设置变更</option>
            <option value='balance.adjustment'>余额调整</option>
          </select>
        </label>
        <label>
          操作者 ID
          <input
            aria-label='操作者 ID'
            value={draft.operatorId}
            onChange={(e) => setDraft({ ...draft, operatorId: e.target.value })}
          />
        </label>
        <label>
          Request ID
          <input
            value={draft.requestId}
            onChange={(e) => setDraft({ ...draft, requestId: e.target.value })}
          />
        </label>
        <button className='ztapi-ops-primary' type='submit'>
          查询审计
        </button>
      </form>
      {state === 'loading' ? <p role='status'>正在加载管理员审计...</p> : null}
      {state === 'error' ? <p role='alert'>管理员审计加载失败。</p> : null}
      {state === 'ready' ? (
        <>
          <div className='ztapi-ops-table-wrap'>
            <table className='ztapi-ops-table'>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>操作者</th>
                  <th>动作</th>
                  <th>对象/结果</th>
                  <th>Request ID</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((item) => (
                  <tr key={item.id}>
                    <td>
                      {new Date(item.created_at * 1000).toLocaleString(
                        'zh-CN',
                        {
                          hour12: false,
                        },
                      )}
                    </td>
                    <td>
                      {item.operator?.admin_username ||
                        `#${item.operator?.admin_id || '-'}`}
                    </td>
                    <td>{actionLabels[item.action] || item.action}</td>
                    <td>{reason(item.params)}</td>
                    <td>{item.request_id}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!data.items.length ? (
              <p className='ztapi-ops-empty'>所选范围内没有管理员审计记录。</p>
            ) : null}
          </div>
          <nav className='ztapi-ops-pagination' aria-label='管理员审计分页'>
            <button
              type='button'
              disabled={data.page <= 1}
              onClick={() => load(filters, data.page - 1)}
            >
              上一页
            </button>
            <span>
              第 {data.page || 1} 页 · 共 {data.total || 0} 条
            </span>
            <button
              type='button'
              disabled={
                (data.page || 1) * (data.page_size || 20) >= (data.total || 0)
              }
              onClick={() => load(filters, (data.page || 1) + 1)}
            >
              下一页
            </button>
          </nav>
        </>
      ) : null}
    </section>
  );
}
