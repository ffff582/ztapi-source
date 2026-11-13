// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
import React from 'react';

const count = (value) =>
  Number.isSafeInteger(value) && value >= 0 ? value : '未提供';
const amount = (value) =>
  value === null || value === undefined || value === ''
    ? '待核对'
    : `${value} USDT`;
const coverage = {
  included: '账单内',
  extra: '账单外额外费用',
  unknown: '覆盖未知',
};
const scope = { inclusive: '含缓存', uncached: '未缓存输入' };
const status = { confirmed: '已确认实扣', estimate: '估算', unconfirmed: '待确认' };

export default function CustomerServiceCosts({ report }) {
  const supplied = Array.isArray(report.customer_service_costs);
  const rows = supplied ? report.customer_service_costs : [];
  const unavailable = report.support_source_status === 'unavailable';
  return (
    <section aria-label='客服消耗成本'>
      <header className='finance-toolbar'>
        <div>
          <h2>客服消耗成本</h2>
          <span className='finance-muted'>
            {unavailable ? '费用汇总待核对' : <>
              已确认 {amount(report.customer_service_confirmed_total)} · 估算{' '}
              {amount(report.customer_service_estimate_total)}
            </>}
          </span>
        </div>
        <span className='finance-muted'>
          {supplied ? `${rows.length} 笔 · 平台承担` : '分类待同步'}
        </span>
      </header>
      {unavailable && <p role='alert'>客服来源不可用，客服费用不完整，实际成本与利润待核对。</p>}
      {report.support_source_status === 'not_configured' && <p role='status'>客服来源未配置，仅有已录入费用。</p>}
      <div className='finance-table-wrap'>
        <table aria-label='客服 Token 与成本明细'>
          <thead>
            <tr>
              <th scope='col'>客服请求</th>
              <th scope='col'>模型 / 渠道</th>
              <th scope='col'>输入 / 输出 Token</th>
              <th scope='col'>输入口径</th>
              <th scope='col'>缓存读取 / 写入 5m / 1h</th>
              <th scope='col'>费用状态</th>
              <th scope='col'>上游额度</th>
              <th scope='col'>现金成本</th>
              <th scope='col'>账单覆盖</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.source_ref || (row.call_id ? `support:${row.call_id}` : row.fact_id ? `fact:${row.fact_id}` : `request:${row.request_id}:${row.attempt}:${row.channel_id}`)}>
                <td>{row.request_id || row.call_id || '待核对'}</td>
                <td>
                  {row.model || '待核对'}
                  <small>#{row.channel_id}</small>
                </td>
                <td>
                  {count(row.input_tokens)} / {count(row.output_tokens)}
                </td>
                <td>{scope[row.input_token_scope] || '未提供'}</td>
                <td>
                  {count(row.cache_read_tokens)} /{' '}
                  {count(row.cache_write_5m_tokens)} /{' '}
                  {count(row.cache_write_1h_tokens)}
                </td>
                <td>{status[row.status] || '待确认'}</td>
                <td>
                  {row.status !== 'unconfirmed' && row.net_credit !== '' &&
                  row.net_credit != null
                    ? `${row.net_credit} Xcloud 额度${row.call_id ? '（报价口径）' : ''}`
                    : '未提供'}
                </td>
                <td>{amount(row.status === 'unconfirmed' ? null : row.net_usdt)}</td>
                <td>{coverage[row.coverage] || '覆盖未知'}</td>
              </tr>
            ))}
            {!rows.length && (
              <tr>
                <td colSpan={9}>
                  {unavailable ? '客服费用记录暂不可读取。' : supplied
                    ? '本月暂无客服费用记录。'
                    : '当前报表未提供客服费用分类。'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <p className='finance-footnote'>
        平台承担，不扣客户余额。账单内费用已计入上游成本，不重复扣减毛利；估算不等于实扣。
      </p>
    </section>
  );
}
