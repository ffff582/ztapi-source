import React, { useEffect, useRef, useState } from 'react';
import { RefreshCw, Search } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

const endpoint = '/api/admin/payments/cny/orders';
const statuses = {
  pending: '待核验',
  paid: '已核验到账',
  refund_pending: '退款核对中',
  refunded: '已原路退款',
};
const methods = { alipay: '支付宝', wxpay: '微信支付' };
const formatTime = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'short',
  timeStyle: 'medium',
  timeZone: 'Asia/Shanghai',
});

export default function CNYOrdersPage() {
  const [draft, setDraft] = useState('');
  const [query, setQuery] = useState({ number: '', page: 1 });
  const [result, setResult] = useState(null);
  const [failed, setFailed] = useState(false);
  const generation = useRef(0);

  useEffect(() => {
    const request = ++generation.current;
    const params = new URLSearchParams({ p: String(query.page) });
    if (query.number) params.set('order_no', query.number);
    adminRequest({ url: `${endpoint}?${params}` })
      .then((data) => {
        if (request === generation.current) setResult(data);
      })
      .catch(() => {
        if (request === generation.current) setFailed(true);
      });
    return () => { generation.current++; };
  }, [query]);

  const load = (number, page) => {
    setResult(null);
    setFailed(false);
    setQuery({ number, page });
  };

  return (
    <section className='ztapi-ops-page'>
      <header className='ztapi-ops-heading'>
        <h1>人民币订单</h1>
        <div className='ztapi-ops-actions'>
          <button type='button' aria-label='刷新人民币订单' onClick={() => load(query.number, query.page)}>
            <RefreshCw size={16} aria-hidden='true' />刷新
          </button>
        </div>
      </header>
      <form className='ztapi-ops-filters' onSubmit={(event) => {
        event.preventDefault();
        load(draft.trim(), 1);
      }}>
        <label>
          商户或平台订单号
          <input type='search' value={draft} maxLength={64} onChange={(event) => setDraft(event.target.value)} />
        </label>
        <button className='ztapi-ops-primary' type='submit'>
          <Search size={16} aria-hidden='true' />查询
        </button>
      </form>
      {failed ? <p role='alert'>人民币订单加载失败。</p> : null}
      {!failed && !result ? <p role='status'>正在加载人民币订单...</p> : null}
      {result ? (
        <>
          <div className='ztapi-ops-table-wrap' tabIndex={0} aria-label='人民币订单列表'>
            <table className='ztapi-ops-table'>
              <thead><tr>
                <th>创建时间（北京时间）</th><th>商户订单号</th><th>黑猫平台订单号</th>
                <th>支付方式</th><th>应付金额</th><th>已核验实付</th><th>状态</th>
              </tr></thead>
              <tbody>{result.items.map((order) => (
                <tr key={order.order_no}>
                  <td>{formatTime.format(order.created_at * 1000)}</td>
                  <td>{order.order_no}</td>
                  <td>{order.provider_trade_no || '待核对'}</td>
                  <td>{methods[order.method] || '-'}</td>
                  <td>¥{order.amount_cny}</td>
                  <td>{order.paid_amount_cny == null ? '未核验' : `¥${order.paid_amount_cny}`}</td>
                  <td>{statuses[order.status] || '待核对'}</td>
                </tr>
              ))}</tbody>
            </table>
            {result.items.length === 0 ? <p className='ztapi-ops-empty'>没有符合条件的人民币订单。</p> : null}
          </div>
          <nav className='ztapi-ops-pagination' aria-label='人民币订单分页'>
            <button type='button' disabled={result.page <= 1} onClick={() => load(query.number, result.page - 1)}>上一页</button>
            <span>第 {result.page} 页 · 共 {result.total} 条</span>
            <button type='button' disabled={result.page * result.page_size >= result.total} onClick={() => load(query.number, result.page + 1)}>下一页</button>
          </nav>
        </>
      ) : null}
    </section>
  );
}
