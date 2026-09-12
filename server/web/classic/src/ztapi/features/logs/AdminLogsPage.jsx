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

function initialFilters() {
  const to = Math.floor(Date.now() / 1000);
  return {
    requestId: '',
    status: '',
    userId: '',
    model: '',
    from: to - 86400,
    to,
  };
}

function requestQuery(filters, page = 1) {
  const params = new URLSearchParams();
  if (filters.requestId.trim())
    params.set('request_id', filters.requestId.trim());
  if (filters.status) params.set('status', filters.status);
  if (filters.userId.trim()) params.set('user_id', filters.userId.trim());
  if (filters.model.trim()) params.set('model', filters.model.trim());
  params.set('from', String(filters.from));
  params.set('to', String(filters.to));
  params.set('p', String(page));
  params.set('page_size', '20');
  return params;
}

export default function AdminLogsPage() {
  const first = useRef(initialFilters()).current;
  const [draft, setDraft] = useState(first);
  const [filters, setFilters] = useState(first);
  const [data, setData] = useState({ items: [], total: 0, page: 1 });
  const [state, setState] = useState('loading');
  const generation = useRef(0);
  const load = useCallback(
    async (next = filters, page = 1) => {
      const current = ++generation.current;
      setState('loading');
      try {
        const result = await adminRequest({
          url: `/api/admin/request-logs?${requestQuery(next, page)}`,
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
      { url: `/api/admin/request-logs/export?${requestQuery(filters)}` },
      'ztapi-request-logs.csv',
    );
  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>请求日志</h1>
          <p>默认显示最近 24 小时，仅提供脱敏后的计费与性能字段。</p>
        </div>
        <div className='ztapi-ops-actions'>
          <button type='button' onClick={() => load(filters)}>
            <RefreshCw size={16} />
            刷新
          </button>
          <button
            type='button'
            aria-label='下载请求日志 CSV'
            onClick={download}
          >
            <Download size={16} />
            导出 CSV
          </button>
        </div>
      </header>
      <form className='ztapi-ops-filters' onSubmit={submit}>
        <label>
          Request ID
          <input
            type='search'
            role='searchbox'
            aria-label='搜索 request ID'
            value={draft.requestId}
            onChange={(e) => setDraft({ ...draft, requestId: e.target.value })}
          />
        </label>
        <label>
          请求结果
          <select
            aria-label='请求结果'
            value={draft.status}
            onChange={(e) => setDraft({ ...draft, status: e.target.value })}
          >
            <option value=''>全部</option>
            <option value='success'>成功</option>
            <option value='error'>失败</option>
          </select>
        </label>
        <label>
          用户 ID
          <input
            value={draft.userId}
            onChange={(e) => setDraft({ ...draft, userId: e.target.value })}
          />
        </label>
        <label>
          模型
          <input
            value={draft.model}
            onChange={(e) => setDraft({ ...draft, model: e.target.value })}
          />
        </label>
        <button className='ztapi-ops-primary' type='submit'>
          查询日志
        </button>
      </form>
      {state === 'loading' ? <p role='status'>正在加载请求日志...</p> : null}
      {state === 'error' ? <p role='alert'>请求日志加载失败。</p> : null}
      {state === 'ready' ? (
        <>
          <div className='ztapi-ops-table-wrap'>
            <table className='ztapi-ops-table'>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>Request ID</th>
                  <th>用户</th>
                  <th>模型</th>
                  <th>结果</th>
                  <th>延迟</th>
                  <th>输入 Token</th>
                  <th>输出 Token</th>
                  <th>总 Token</th>
                  <th>计费额度</th>
                  <th>金额</th>
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
                    <td>{item.request_id}</td>
                    <td>{item.username || `#${item.user_id}`}</td>
                    <td>{item.model || '-'}</td>
                    <td>{item.status}</td>
                    <td>{item.latency} 秒</td>
                    <td>{item.prompt_tokens}</td>
                    <td>{item.completion_tokens}</td>
                    <td>{item.total_tokens}</td>
                    <td>{item.quota}</td>
                    <td>{item.billed_amount ?? '-'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!data.items.length ? (
              <p className='ztapi-ops-empty'>所选范围内没有请求日志。</p>
            ) : null}
          </div>
          <nav className='ztapi-ops-pagination' aria-label='请求日志分页'>
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
