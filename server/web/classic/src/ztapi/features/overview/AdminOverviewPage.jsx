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

import React, { useEffect, useRef, useState } from 'react';
import { getAdminOverview } from './overview-api.js';

const rangeSeconds = 7 * 24 * 60 * 60;

function formatNumber(value) { return new Intl.NumberFormat('zh-CN').format(value || 0); }
function currentRange() { const to = Math.floor(Date.now() / 1000); return { from: to - rangeSeconds, to }; }
function formatDay(value) { return new Date(value * 1000).toISOString().slice(0, 10); }
function isOverviewResponse(value) { return value && typeof value === 'object' && value.requests && Number.isFinite(value.requests.total) && Number.isFinite(value.requests.success) && Number.isFinite(value.requests.failure); }
function Stat({ value, label }) { return <div className='ztapi-overview-stat'><strong>{formatNumber(value)}</strong><span>{label}</span></div>; }
function EmptyList({ children }) { return <p className='ztapi-overview-empty-list'>{children}</p>; }

export default function AdminOverviewPage() {
  const [state, setState] = useState({ status: 'loading', data: null });
  const requestRef = useRef({ generation: 0, controller: null });

  const load = () => {
    requestRef.current.controller?.abort();
    const generation = requestRef.current.generation + 1;
    const controller = new AbortController();
    requestRef.current = { generation, controller };
    const range = currentRange();
    setState({ status: 'loading', data: null });
    getAdminOverview(range.from, range.to, controller.signal).then(
      (data) => {
        if (requestRef.current.generation === generation) setState(isOverviewResponse(data) ? { status: 'ready', data } : { status: 'error', data: null });
      },
      () => {
        if (requestRef.current.generation === generation) setState({ status: 'error', data: null });
      },
    );
  };

  useEffect(() => { load(); return () => { requestRef.current.generation += 1; requestRef.current.controller?.abort(); }; }, []);

  if (state.status === 'loading') return <section className='ztapi-admin-page ztapi-overview-skeleton' data-testid='overview-loading' style={{ minHeight: 360 }} aria-busy='true'><h1 id='ztapi-page-title'>概览</h1><button type='button' className='ztapi-overview-refresh' onClick={load}>刷新概览</button><div className='ztapi-overview-skeleton-band' /><div className='ztapi-overview-skeleton-main' /></section>;
  if (state.status === 'error') return <section className='ztapi-admin-page ztapi-overview-error' aria-labelledby='ztapi-page-title'><h1 id='ztapi-page-title'>概览</h1><p role='alert'>无法加载运营概览。</p><button type='button' onClick={load}>重试概览</button></section>;

  const data = state.data;
  const hasFinancials = Object.hasOwn(data, 'billed_quota');
  const hasChannels = Object.hasOwn(data, 'channel_statuses');
  const hasFailures = Object.hasOwn(data, 'recent_failures');
  const hasAudit = Object.hasOwn(data, 'recent_audit_operations');
  const hasSeries = Array.isArray(data.series) && data.series.length > 0;
  const maxRequests = hasSeries ? Math.max(...data.series.map((item) => item.requests), 1) : 1;
  const maxQuota = hasSeries ? Math.max(...data.series.map((item) => item.billed_quota), 1) : 1;
  return <section className='ztapi-admin-page ztapi-overview' aria-labelledby='ztapi-page-title'>
    <div className='ztapi-overview-heading'><div><h1 id='ztapi-page-title'>概览</h1><p>当前显示所选七天范围内的运营情况。</p></div><button type='button' className='ztapi-overview-refresh' onClick={load}>刷新概览</button></div>
    <div className='ztapi-overview-stat-band'><Stat value={data.requests.total} label='请求数' /><Stat value={data.requests.success} label='成功' /><Stat value={data.requests.failure} label='失败' />{hasFinancials ? <><Stat value={data.billed_quota} label='已计费额度' /><Stat value={data.user_balance_total} label='用户余额' /><Stat value={data.pending_top_ups} label='待处理充值' /></> : null}</div>
    {data.requests.total === 0 ? <p className='ztapi-overview-empty'>所选范围内暂无运营记录。</p> : null}
    <section className='ztapi-overview-panel ztapi-overview-chart' aria-label='请求量和已计费额度图表'><h2>请求量和已计费额度</h2>{hasSeries ? <div className='ztapi-overview-chart-points'>{data.series.map((item) => <div className='ztapi-overview-chart-point' key={item.start} aria-label={`${formatDay(item.start)}：${item.requests} 次请求，${item.billed_quota} 已计费额度`}><span>{formatDay(item.start)}</span><div className='ztapi-overview-chart-track'><i className='is-requests' style={{ width: `${(item.requests / maxRequests) * 100}%` }} /><i className='is-quota' style={{ width: `${(item.billed_quota / maxQuota) * 100}%` }} /></div><strong>{formatNumber(item.requests)} / {formatNumber(item.billed_quota)}</strong></div>)}</div> : <EmptyList>所选范围内暂无图表数据。</EmptyList>}</section>
    <div className='ztapi-overview-grid'>
      {hasChannels ? <section className='ztapi-overview-panel'><h2>渠道状态</h2>{data.channel_statuses.length ? <ul className='ztapi-overview-list'>{data.channel_statuses.map((item) => <li key={item.status}><span>状态 {item.status}</span><strong>{formatNumber(item.count)}</strong></li>)}</ul> : <EmptyList>暂无渠道记录。</EmptyList>}</section> : null}
      {hasFailures ? <section className='ztapi-overview-panel'><h2>最近失败</h2>{data.recent_failures.length ? <ul className='ztapi-overview-list'>{data.recent_failures.map((item) => <li key={item.id}><span>{item.summary}</span><small>{item.model_name || '未记录模型'}</small></li>)}</ul> : <EmptyList>暂无最近失败。</EmptyList>}</section> : null}
      {hasAudit ? <section className='ztapi-overview-panel'><h2>最近管理员操作</h2>{data.recent_audit_operations.length ? <ul className='ztapi-overview-list'>{data.recent_audit_operations.map((item) => <li key={item.id}><span>{item.action}</span></li>)}</ul> : <EmptyList>暂无最近管理员操作。</EmptyList>}</section> : null}
    </div>
  </section>;
}
