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

const statusLabels = {
  pending: '待处理',
  success: '成功',
  failed: '失败',
  expired: '已过期',
  rejected: '已拒绝',
};

function formatTime(value) {
  if (!value) return '-';
  return new Date(Number(value) * 1000).toLocaleString('zh-CN', {
    hour12: false,
  });
}

function buildQuery(filters, page = 1) {
  const params = new URLSearchParams();
  if (filters.keyword.trim()) params.set('keyword', filters.keyword.trim());
  if (filters.status) params.set('status', filters.status);
  if (filters.userId.trim()) params.set('user_id', filters.userId.trim());
  if (filters.provider.trim()) {
    params.set('payment_provider', filters.provider.trim());
  }
  params.set('p', String(page));
  params.set('page_size', '20');
  return params;
}

export default function OrdersPage({ canWrite = false }) {
  const [draft, setDraft] = useState({
    keyword: '',
    status: '',
    userId: '',
    provider: '',
  });
  const [filters, setFilters] = useState(draft);
  const [data, setData] = useState({ items: [], total: 0, page: 1 });
  const [state, setState] = useState('loading');
  const [notice, setNotice] = useState('');
  const [decision, setDecision] = useState(null);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [conflict, setConflict] = useState('');
  const generation = useRef(0);

  const load = useCallback(
    async (nextFilters = filters, page = 1) => {
      const requestGeneration = ++generation.current;
      setState('loading');
      try {
        const result = await adminRequest({
          url: `/api/admin/topups?${buildQuery(nextFilters, page)}`,
        });
        if (requestGeneration !== generation.current) return;
        setData(result);
        setState('ready');
      } catch {
        if (requestGeneration !== generation.current) return;
        setState('error');
      }
    },
    [filters],
  );

  useEffect(() => {
    load(filters, 1);
  }, []); // The initial request must run exactly once.

  const search = (event) => {
    event.preventDefault();
    const next = { ...draft };
    setFilters(next);
    load(next, 1);
  };

  const openDecision = (order, type) => {
    setDecision({ order, type });
    setReason('');
    setConflict('');
  };

  const submitDecision = async () => {
    if (submitting || reason.trim().length < 2) return;
    setSubmitting(true);
    setConflict('');
    try {
      const result = await adminRequest({
        method: 'POST',
        url: `/api/admin/topups/${decision.order.id}/${decision.type}`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          expected_status: 'pending',
          reason: reason.trim(),
        }),
      });
      setData((current) => ({
        ...current,
        items: current.items.map((item) =>
          item.id === result.id ? { ...item, ...result } : item,
        ),
      }));
      if (result.warning === 'committed_cache_sync_pending') {
        setNotice('订单已完成，缓存同步仍在进行。');
      } else {
        setNotice(
          decision.type === 'complete' ? '订单已完成并入账。' : '订单已拒绝。',
        );
      }
      setDecision(null);
    } catch (error) {
      if (error.status === 409) {
        const currentStatus = statusLabels[error.data?.status] || '已处理';
        setConflict(`订单已由其他管理员处理，当前状态为${currentStatus}。`);
      } else {
        setConflict('订单处理失败，请刷新后重试。');
      }
    } finally {
      setSubmitting(false);
    }
  };

  const exportRows = async () => {
    await adminDownload(
      { url: `/api/admin/topups/export?${buildQuery(filters, 1)}` },
      'ztapi-topups.csv',
    );
  };

  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <div>
          <h1>充值订单</h1>
          <p>审核充值到账记录，所有人工处理均写入审计日志。</p>
        </div>
        <div className='ztapi-ops-actions'>
          <button
            type='button'
            onClick={() => load(filters, data.page)}
            aria-label='刷新订单'
          >
            <RefreshCw size={16} />
            刷新
          </button>
          <button type='button' onClick={exportRows} aria-label='下载订单 CSV'>
            <Download size={16} />
            导出 CSV
          </button>
        </div>
      </header>
      {notice ? (
        <p className='ztapi-ops-notice' role='status'>
          {notice}
        </p>
      ) : null}
      <form className='ztapi-ops-filters' onSubmit={search}>
        <label>
          订单号或用户名
          <input
            type='search'
            role='searchbox'
            aria-label='搜索订单'
            value={draft.keyword}
            onChange={(e) => setDraft({ ...draft, keyword: e.target.value })}
          />
        </label>
        <label>
          订单状态
          <select
            aria-label='订单状态'
            value={draft.status}
            onChange={(e) => setDraft({ ...draft, status: e.target.value })}
          >
            <option value=''>全部状态</option>
            <option value='pending'>仅待处理</option>
            <option value='success'>仅成功订单</option>
            <option value='rejected'>仅已拒绝</option>
            <option value='failed'>仅失败订单</option>
            <option value='expired'>仅已过期</option>
          </select>
        </label>
        <label>
          用户 ID
          <input
            inputMode='numeric'
            value={draft.userId}
            onChange={(e) => setDraft({ ...draft, userId: e.target.value })}
          />
        </label>
        <label>
          支付提供方
          <input
            value={draft.provider}
            onChange={(e) => setDraft({ ...draft, provider: e.target.value })}
          />
        </label>
        <button className='ztapi-ops-primary' type='submit'>
          查询订单
        </button>
      </form>
      {state === 'loading' ? <p role='status'>正在加载充值订单...</p> : null}
      {state === 'error' ? <p role='alert'>充值订单加载失败。</p> : null}
      {state === 'ready' ? (
        <>
          <div className='ztapi-ops-table-wrap'>
            <table className='ztapi-ops-table'>
              <thead>
                <tr>
                  <th>创建时间</th>
                  <th>订单号</th>
                  <th>用户</th>
                  <th>充值额度</th>
                  <th>实付金额</th>
                  <th>支付</th>
                  <th>状态</th>
                  {canWrite ? <th>操作</th> : null}
                </tr>
              </thead>
              <tbody>
                {data.items.map((order) => (
                  <tr key={order.id}>
                    <td>{formatTime(order.create_time)}</td>
                    <td>{order.trade_no}</td>
                    <td>{order.username || `#${order.user_id}`}</td>
                    <td>{order.amount}</td>
                    <td>{order.money}</td>
                    <td>
                      {order.payment_provider || order.payment_method || '-'}
                    </td>
                    <td>{statusLabels[order.status] || order.status}</td>
                    {canWrite ? (
                      <td>
                        {order.status === 'pending' ? (
                          <div className='ztapi-ops-row-actions'>
                            <button
                              type='button'
                              aria-label={`手工完成 ${order.trade_no}`}
                              onClick={() => openDecision(order, 'complete')}
                            >
                              手工完成
                            </button>
                            <button
                              type='button'
                              aria-label={`拒绝 ${order.trade_no}`}
                              onClick={() => openDecision(order, 'reject')}
                            >
                              拒绝
                            </button>
                          </div>
                        ) : (
                          '-'
                        )}
                      </td>
                    ) : null}
                  </tr>
                ))}
              </tbody>
            </table>
            {!data.items.length ? (
              <p className='ztapi-ops-empty'>没有符合条件的充值订单。</p>
            ) : null}
          </div>
          <nav className='ztapi-ops-pagination' aria-label='充值订单分页'>
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
      {decision ? (
        <div className='ztapi-ops-backdrop'>
          <section
            className='ztapi-ops-dialog'
            role='dialog'
            aria-modal='true'
            aria-label={
              decision.type === 'complete' ? '手工完成订单' : '拒绝充值订单'
            }
          >
            <h2>
              {decision.type === 'complete' ? '手工完成订单' : '拒绝充值订单'}
            </h2>
            <p>
              {decision.order.trade_no} ·{' '}
              {decision.order.username || `#${decision.order.user_id}`}
            </p>
            {conflict ? (
              <p role='alert' className='ztapi-ops-error'>
                {conflict}
              </p>
            ) : null}
            <label>
              处理原因
              <textarea
                aria-label='处理原因'
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <footer>
              <button
                type='button'
                disabled={submitting}
                onClick={() => setDecision(null)}
              >
                取消
              </button>
              <button
                className='ztapi-ops-primary'
                type='button'
                disabled={submitting || reason.trim().length < 2}
                onClick={submitDecision}
              >
                {submitting
                  ? '正在处理...'
                  : decision.type === 'complete'
                    ? '确认完成并入账'
                    : '确认拒绝订单'}
              </button>
            </footer>
          </section>
        </div>
      ) : null}
    </section>
  );
}
