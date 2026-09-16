/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/

import React, { useCallback, useEffect, useState } from 'react';
import { adminRequest } from '../../auth/admin-session.js';
import './fx-pricing.css';

const statusLabels = Object.freeze({
  reprice: '待改价',
  unchanged: '无需改动',
  blocked: '无法计算',
});

const kindLabels = Object.freeze({
  ab_text: '按报价表定价',
  legacy_cny: '旧版人民币定价',
  not_covered: '本期不参与',
});

function rate(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed.toFixed(2) : '—';
}

function price(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed === 0) return '—';
  if (parsed >= 10) return parsed.toFixed(2);
  if (parsed >= 1) return parsed.toFixed(3);
  return parsed.toFixed(4);
}

function change(current, proposed) {
  const from = Number(current);
  const to = Number(proposed);
  if (!Number.isFinite(from) || !Number.isFinite(to) || from === 0) return '—';
  const delta = (to / from - 1) * 100;
  if (Math.abs(delta) < 0.05) return '0.0%';
  return `${delta > 0 ? '+' : ''}${delta.toFixed(1)}%`;
}

function formatTime(seconds) {
  const value = Number(seconds);
  if (!Number.isFinite(value) || value <= 0) return '—';
  return new Date(value * 1000).toLocaleString('zh-CN', { hour12: false });
}

export default function FXPricingPage({ canWrite = false }) {
  const [policy, setPolicy] = useState(null);
  const [history, setHistory] = useState([]);
  const [rows, setRows] = useState([]);
  const [status, setStatus] = useState('loading');
  const [form, setForm] = useState({
    market_cny_per_usdt: '',
    stop_loss_cny: '0.03',
    upstream_cny_per_usd: '',
    upstream_source: 'pboc_mid',
    market_source: 'okx_alipay',
    reason: '',
  });
  const [quotes, setQuotes] = useState(null);
  const [midRate, setMidRate] = useState(null);
  const [busy, setBusy] = useState('');
  const [message, setMessage] = useState(null);

  const load = useCallback(async () => {
    setStatus('loading');
    try {
      const [fx, pricing] = await Promise.all([
        adminRequest({ method: 'GET', url: '/api/models/ztapi/fx' }),
        adminRequest({ method: 'GET', url: '/api/models/ztapi/fx/pricing' }),
      ]);
      const current = fx?.current || null;
      setPolicy(current);
      setHistory(Array.isArray(fx?.history) ? fx.history : []);
      setRows(Array.isArray(pricing?.rows) ? pricing.rows : []);
      setForm((previous) => ({
        ...previous,
        market_cny_per_usdt:
          previous.market_cny_per_usdt || rate(current?.market_cny_per_usdt),
        stop_loss_cny: previous.stop_loss_cny || rate(current?.stop_loss_cny),
        upstream_cny_per_usd:
          previous.upstream_cny_per_usd ||
          (Number(current?.upstream_cny_per_usd) > 0
            ? rate(current?.upstream_cny_per_usd)
            : ''),
        market_source: current?.market_source || previous.market_source,
        upstream_source: current?.upstream_source || previous.upstream_source,
      }));
      setStatus('ready');
    } catch {
      setStatus('error');
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const readOKX = useCallback(async () => {
    setBusy('okx');
    setMessage(null);
    try {
      const bid = await adminRequest({
        method: 'GET',
        url: '/api/models/ztapi/fx/okx-alipay',
      });
      setQuotes(bid);
      setForm((previous) => ({
        ...previous,
        market_cny_per_usdt: rate(bid?.median_cny_per_usdt),
        market_source: 'okx_alipay',
      }));
      setMessage({ tone: 'ok', text: '已读取欧意支付宝商家最新报价。' });
    } catch (error) {
      setMessage({
        tone: 'error',
        text: `读取欧意报价失败：${error?.message || '请稍后再试'}`,
      });
    } finally {
      setBusy('');
    }
  }, []);

  const readPBOC = useCallback(async () => {
    setBusy('pboc');
    setMessage(null);
    try {
      const mid = await adminRequest({
        method: 'GET',
        url: '/api/models/ztapi/fx/pboc-mid',
      });
      setMidRate(mid);
      setForm((previous) => ({
        ...previous,
        upstream_cny_per_usd: rate(mid?.cny_per_usd),
        upstream_source: 'pboc_mid',
      }));
      setMessage({
        tone: 'ok',
        text: `已读取 ${mid?.rate_date || '今日'} 的央行中间价。`,
      });
    } catch (error) {
      setMessage({
        tone: 'error',
        text: `读取央行中间价失败：${error?.message || '请稍后再试'}`,
      });
    } finally {
      setBusy('');
    }
  }, []);

  const savePolicy = useCallback(async () => {
    setBusy('save');
    setMessage(null);
    try {
      await adminRequest({
        method: 'POST',
        url: '/api/models/ztapi/fx',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      });
      setMessage({
        tone: 'ok',
        text: '汇率已保存。价格要点“按当前汇率改价”后才会变。',
      });
      await load();
    } catch (error) {
      setMessage({
        tone: 'error',
        text: `汇率未保存：${error?.message || '请检查填写的数字'}`,
      });
    } finally {
      setBusy('');
    }
  }, [form, load]);

  const reprice = useCallback(async () => {
    setBusy('reprice');
    setMessage(null);
    try {
      const result = await adminRequest({
        method: 'POST',
        url: '/api/models/ztapi/fx/reprice',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm: true }),
      });
      const skipped = Array.isArray(result?.skipped)
        ? result.skipped.length
        : 0;
      setMessage({
        tone: 'ok',
        text: `改价完成：${result?.republished || 0} 个模型改了价，${result?.unchanged || 0} 个无需改动${skipped ? `，${skipped} 个因上游美元汇率未设置跳过` : ''}。`,
      });
      await load();
    } catch (error) {
      setMessage({
        tone: 'error',
        text: `改价未完成，价格保持不变：${error?.message || '请稍后再试'}`,
      });
    } finally {
      setBusy('');
    }
  }, [load]);

  const pending = rows.filter((row) => row.status === 'reprice').length;
  const blocked = rows.filter((row) => row.status === 'blocked').length;
  const upstreamSet = Number(policy?.upstream_cny_per_usd) > 0;

  return (
    <div className='ztapi-fx-page'>
      <section className='ztapi-fx-card'>
        <h2>当前汇率</h2>
        <div className='ztapi-fx-current'>
          <div className='ztapi-fx-figure'>
            <span>平台汇率（¥ / U）</span>
            <strong>{rate(policy?.platform_cny_per_usdt)}</strong>
          </div>
          <div className='ztapi-fx-figure'>
            <span>欧意支付宝收 U 价</span>
            <strong>{rate(policy?.market_cny_per_usdt)}</strong>
          </div>
          <div className='ztapi-fx-figure'>
            <span>止损</span>
            <strong>{rate(policy?.stop_loss_cny)}</strong>
          </div>
          <div className={`ztapi-fx-figure${upstreamSet ? '' : ' pending'}`}>
            <span>
              上游美元汇率（¥ / $）
              {policy?.upstream_source === 'pboc_mid' ? ' · 央行中间价' : ''}
              {policy?.upstream_rate_date
                ? ` ${policy.upstream_rate_date}`
                : ''}
            </span>
            <strong>
              {upstreamSet ? rate(policy?.upstream_cny_per_usd) : '待设置'}
            </strong>
          </div>
          <div className='ztapi-fx-figure'>
            <span>更新时间</span>
            <strong style={{ fontSize: 14 }}>
              {formatTime(policy?.created_at)}
            </strong>
          </div>
        </div>
        <p>
          平台汇率 = 欧意支付宝商家收 U 价 −
          止损。所有模型先算人民币成本，再按平台汇率换成 U
          并加毛利；上游美元汇率只用于把美元报价折成人民币。
        </p>
      </section>

      <section className='ztapi-fx-card'>
        <h2>修改汇率</h2>
        <div className='ztapi-fx-form'>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-market'>欧意支付宝收 U 价（¥）</label>
            <input
              id='fx-market'
              type='number'
              step='0.01'
              value={form.market_cny_per_usdt}
              disabled={!canWrite}
              onChange={(event) =>
                setForm({ ...form, market_cny_per_usdt: event.target.value })
              }
            />
          </div>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-stop'>止损（¥）</label>
            <input
              id='fx-stop'
              type='number'
              step='0.01'
              value={form.stop_loss_cny}
              disabled={!canWrite}
              onChange={(event) =>
                setForm({ ...form, stop_loss_cny: event.target.value })
              }
            />
          </div>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-upstream'>上游美元汇率（¥ / $）</label>
            <input
              id='fx-upstream'
              type='number'
              step='0.01'
              placeholder='未设置'
              value={form.upstream_cny_per_usd}
              disabled={!canWrite}
              onChange={(event) =>
                setForm({ ...form, upstream_cny_per_usd: event.target.value })
              }
            />
          </div>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-upstream-source'>上游汇率来源</label>
            <select
              id='fx-upstream-source'
              value={form.upstream_source}
              disabled={!canWrite}
              onChange={(event) =>
                setForm({ ...form, upstream_source: event.target.value })
              }
            >
              <option value='pboc_mid'>央行每日中间价</option>
              <option value='manual'>手动填写</option>
            </select>
          </div>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-source'>取价方式</label>
            <select
              id='fx-source'
              value={form.market_source}
              disabled={!canWrite}
              onChange={(event) =>
                setForm({ ...form, market_source: event.target.value })
              }
            >
              <option value='okx_alipay'>欧意支付宝商家</option>
              <option value='manual'>手动填写</option>
            </select>
          </div>
          <div className='ztapi-fx-field'>
            <label htmlFor='fx-reason'>修改原因</label>
            <input
              id='fx-reason'
              type='text'
              value={form.reason}
              disabled={!canWrite}
              placeholder='例如：上游确认美元按 7.0 结算'
              onChange={(event) =>
                setForm({ ...form, reason: event.target.value })
              }
            />
          </div>
        </div>
        <div className='ztapi-fx-actions'>
          <button
            type='button'
            className='ztapi-fx-button'
            onClick={readOKX}
            disabled={!canWrite || busy !== ''}
          >
            {busy === 'okx' ? '正在读取…' : '立即读取欧意'}
          </button>
          <button
            type='button'
            className='ztapi-fx-button'
            onClick={readPBOC}
            disabled={!canWrite || busy !== ''}
          >
            {busy === 'pboc' ? '正在读取…' : '读取央行中间价'}
          </button>
          <button
            type='button'
            className='ztapi-fx-button primary'
            onClick={savePolicy}
            disabled={!canWrite || busy !== '' || !form.reason.trim()}
          >
            {busy === 'save' ? '正在保存…' : '保存汇率'}
          </button>
          {message ? (
            <span className={`ztapi-fx-message ${message.tone}`}>
              {message.text}
            </span>
          ) : null}
        </div>
        {midRate ? (
          <div className='ztapi-fx-quotes'>
            央行中间价 {rate(midRate.cny_per_usd)}（{midRate.rate_date}
            ，来源：中国货币网）。 上游按这个价把美元账单折成人民币。
          </div>
        ) : null}
        {quotes ? (
          <div className='ztapi-fx-quotes'>
            最高 {Array.isArray(quotes.quotes) ? quotes.quotes.length : 0}{' '}
            家商家报价：
            {(quotes.quotes || [])
              .map((quote) => `${quote.merchant} ${quote.price}`)
              .join('，')}
            ；中位数 {rate(quotes.median_cny_per_usdt)}，可成交量 ≥{' '}
            {quotes.min_available_usdt} U， 共 {quotes.eligible_ads}{' '}
            家符合条件。
          </div>
        ) : null}
      </section>

      <section className='ztapi-fx-card'>
        <h2>价格总览</h2>
        <p>
          {status === 'loading'
            ? '正在读取价格…'
            : `${rows.length} 个已上架模型：${pending} 个待改价，${blocked} 个无法计算。改价会生成新的价格版本，旧版本保留可查。`}
        </p>
        <div className='ztapi-fx-actions'>
          <button
            type='button'
            className='ztapi-fx-button primary'
            onClick={reprice}
            disabled={!canWrite || busy !== '' || pending === 0}
          >
            {busy === 'reprice' ? '正在改价…' : '按当前汇率改价'}
          </button>
          <button
            type='button'
            className='ztapi-fx-button'
            onClick={load}
            disabled={busy !== ''}
          >
            刷新
          </button>
        </div>
        <div className='ztapi-fx-table-wrap'>
          <table className='ztapi-fx-table'>
            <thead>
              <tr>
                <th>模型</th>
                <th>定价来源</th>
                <th>报价币种</th>
                <th className='num'>人民币成本 / 百万输入</th>
                <th className='num'>现价 U（输入 / 输出）</th>
                <th className='num'>新价 U（输入 / 输出）</th>
                <th className='num'>变动</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.public_name || row.source_model}>
                  <td>{row.public_name || row.source_model}</td>
                  <td>{kindLabels[row.kind] || row.kind}</td>
                  <td>{row.quote_currency || '—'}</td>
                  <td className='num'>
                    {row.cost_cny_input ? `¥${price(row.cost_cny_input)}` : '—'}
                  </td>
                  <td className='num'>
                    {price(row.current_input_sale)} /{' '}
                    {price(row.current_output_sale)}
                  </td>
                  <td className='num'>
                    {price(row.proposed_input_sale)} /{' '}
                    {price(row.proposed_output_sale)}
                  </td>
                  <td className='num'>
                    {change(row.current_input_sale, row.proposed_input_sale)}
                  </td>
                  <td>
                    <span className={`ztapi-fx-status ${row.status}`}>
                      {statusLabels[row.status] || row.status}
                    </span>
                    {row.reason ? ` ${row.reason}` : ''}
                  </td>
                </tr>
              ))}
              {rows.length === 0 && status === 'ready' ? (
                <tr>
                  <td colSpan={8}>没有已上架的模型。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className='ztapi-fx-card'>
        <h2>汇率修改记录</h2>
        <div className='ztapi-fx-history'>
          {history.length === 0 ? (
            <span>暂无记录。</span>
          ) : (
            history.map((item) => (
              <span key={item.id}>
                {formatTime(item.created_at)} · 平台{' '}
                {rate(item.platform_cny_per_usdt)}（欧意{' '}
                {rate(item.market_cny_per_usdt)} − 止损{' '}
                {rate(item.stop_loss_cny)}）· 上游美元{' '}
                {Number(item.upstream_cny_per_usd) > 0
                  ? rate(item.upstream_cny_per_usd)
                  : '未设置'}{' '}
                · 操作人 {item.operator_id || '系统'} · {item.reason}
              </span>
            ))
          )}
        </div>
      </section>
    </div>
  );
}
