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

function query(filters, page = 1) {
  const params = new URLSearchParams();
  if (filters.userId.trim()) params.set('user_id', filters.userId.trim());
  if (filters.operatorId.trim())
    params.set('operator_id', filters.operatorId.trim());
  if (filters.source) params.set('source_type', filters.source);
  if (filters.requestId.trim())
    params.set('request_id', filters.requestId.trim());
  params.set('p', page);
  params.set('page_size', '20');
  return params;
}

export default function LedgerPage() {
  const empty = { userId: '', operatorId: '', source: '', requestId: '' };
  const [draft, setDraft] = useState(empty);
  const [filters, setFilters] = useState(empty);
  const [data, setData] = useState({ items: [], total: 0, page: 1 });
  const [state, setState] = useState('loading');
  const generation = useRef(0);
  const load = useCallback(
    async (next = filters, page = 1) => {
      const current = ++generation.current;
      setState('loading');
      try {
        const result = await adminRequest({
          url: `/api/admin/balance-ledger?${query(next, page)}`,
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
      { url: `/api/admin/balance-ledger/export?${query(filters)}` },
      'ztapi-balance-ledger.csv',
    );
  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>余额账本</h1>
          <p>不可修改的余额变动记录，作为财务核对依据。</p>
        </div>
        <div className='ztapi-ops-actions'>
          <button type='button' onClick={() => load(filters, data.page)}>
            <RefreshCw size={16} />
            刷新
          </button>
          <button type='button' aria-label='下载账本 CSV' onClick={download}>
            <Download size={16} />
            导出 CSV
          </button>
        </div>
      </header>
      <form className='ztapi-ops-filters' onSubmit={submit}>
        <label>
          用户 ID
          <input
            aria-label='账本用户 ID'
            value={draft.userId}
            onChange={(e) => setDraft({ ...draft, userId: e.target.value })}
          />
        </label>
        <label>
          操作者 ID
          <input
            value={draft.operatorId}
            onChange={(e) => setDraft({ ...draft, operatorId: e.target.value })}
          />
        </label>
        <label>
          账本来源
          <select
            aria-label='账本来源'
            value={draft.source}
            onChange={(e) => setDraft({ ...draft, source: e.target.value })}
          >
            <option value=''>全部</option>
            <option value='topup_completion'>充值完成</option>
            <option value='admin_adjustment'>人工调整</option>
          </select>
        </label>
        <label>
          Request ID
          <input
            value={draft.requestId}
            onChange={(e) => setDraft({ ...draft, requestId: e.target.value })}
          />
        </label>
        <button className='ztapi-ops-primary' type='submit'>
          查询账本
        </button>
      </form>
      {state === 'loading' ? <p role='status'>正在加载余额账本...</p> : null}
      {state === 'error' ? <p role='alert'>余额账本加载失败。</p> : null}
      {state === 'ready' ? (
        <>
          <div className='ztapi-ops-table-wrap'>
            <table className='ztapi-ops-table'>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>流水 ID</th>
                  <th>用户 ID</th>
                  <th>操作者 ID</th>
                  <th>变动</th>
                  <th>变动前</th>
                  <th>变动后</th>
                  <th>来源</th>
                  <th>原因</th>
                  <th>Request ID</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((item) => (
                  <tr key={item.id}>
                    <td>
                      {new Date(item.created_at).toLocaleString('zh-CN', {
                        hour12: false,
                      })}
                    </td>
                    <td>{item.id}</td>
                    <td>{item.user_id}</td>
                    <td>{item.operator_id}</td>
                    <td>{item.delta}</td>
                    <td>{item.balance_before}</td>
                    <td>{item.balance_after}</td>
                    <td>{item.source_type}</td>
                    <td>{item.reason}</td>
                    <td>{item.request_id}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!data.items.length ? (
              <p className='ztapi-ops-empty'>没有符合条件的账本记录。</p>
            ) : null}
          </div>
          <nav className='ztapi-ops-pagination' aria-label='余额账本分页'>
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
