import { CreditCard, ExternalLink, RefreshCw } from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { apiClient } from '../../api/client';
import { useLocale } from '../../i18n/locale';
import './cny-topup.css';

export type CNYPaymentMethod = 'alipay' | 'wxpay';
type State = 'pending' | 'paid' | 'refund_pending' | 'refunded';
type Order = {
  order_no: string;
  status: State;
  amount_cny: string;
  credit_u: string;
  cny_per_u: string;
  checkout_url: string;
  checkout_qr?: string;
  method: CNYPaymentMethod;
  created_at: number;
};
type Config = { enabled: true; min_cny: string; max_cny?: string; cny_per_u: string; methods: CNYPaymentMethod[] } | { enabled: false };

const ordersPath = '/user/topup/cny/orders';
const centAmount = /^\d{1,8}(\.\d{1,2})?$/;
const methodLabels: Record<CNYPaymentMethod, string> = { alipay: '支付宝', wxpay: '微信支付' };
const statusLabels: Record<State, string> = {
  pending: '等待支付确认', paid: '充值已到账', refund_pending: '退款核对中', refunded: '已原路退款',
};

function safeCheckout(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : null;
  } catch {
    return null;
  }
}

export function CNYTopUp({ method, onBalanceChange }: { method: CNYPaymentMethod; onBalanceChange: () => void }) {
  const { t } = useLocale();
  const [config, setConfig] = useState<Config | null>();
  const [orders, setOrders] = useState<Order[]>([]);
  const [amount, setAmount] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [submitError, setSubmitError] = useState(false);
  const [historyError, setHistoryError] = useState(false);
  const draft = useRef<Partial<Record<CNYPaymentMethod, { amount: string; key: string; orderNo?: string }>>>({});
  const previous = useRef<Order[]>([]);
  const awaitingRecentPayment = orders.some((order) => order.status === 'pending' && order.created_at > Date.now() / 1000 - 1800);

  useEffect(() => {
    let alive = true;
    void apiClient.get<Config>('/user/topup/cny/info').then((next) => {
      if (alive) setConfig(next);
    }).catch(() => {
      if (alive) setConfig(null);
    });
    return () => { alive = false; };
  }, []);

  useEffect(() => {
    if (!config) return;
    let alive = true;
    let busy = false;
    const read = async () => {
      if (busy) return;
      busy = true;
      try {
        const next = await apiClient.get<Order[]>(ordersPath);
        if (!alive || !Array.isArray(next)) return;
        const completed = next.filter((row) => row.status !== 'pending' && previous.current.some((old) => old.order_no === row.order_no && old.status !== row.status));
        if (completed.length > 0) {
          for (const row of completed) {
            if (draft.current[row.method]?.orderNo === row.order_no) delete draft.current[row.method];
          }
          onBalanceChange();
        }
        previous.current = next;
        setOrders(next);
        setHistoryError(false);
      } catch {
        if (alive) setHistoryError(true);
      } finally {
        busy = false;
      }
    };
    void read();
    const timer = awaitingRecentPayment ? window.setInterval(() => void read(), 4_000) : undefined;
    return () => { alive = false; if (timer !== undefined) window.clearInterval(timer); };
  }, [config, onBalanceChange, awaitingRecentPayment, refreshKey]);

  const amountValid = config?.enabled && centAmount.test(amount) && Number(amount) >= Number(config.min_cny) && (!config.max_cny || Number(amount) <= Number(config.max_cny));
  const methodEnabled = config?.enabled && Array.isArray(config.methods) && config.methods.includes(method);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!amountValid || !methodEnabled || submitting) return;
    const normalized = Number(amount).toFixed(2);
    const current = draft.current[method];
    const entry = current?.amount === normalized ? current : { amount: normalized, key: crypto.randomUUID(), orderNo: undefined };
    draft.current[method] = entry;
    setSubmitting(true);
    setSubmitError(false);
    try {
      const next = await apiClient.post<Order>(ordersPath, { amount_cny: normalized, method, idempotency_key: entry.key });
      const updated = [next, ...previous.current.filter((row) => row.order_no !== next.order_no)];
      previous.current = updated;
      setOrders(updated);
      if (next.status === 'paid') onBalanceChange();
      if (next.status === 'pending') entry.orderNo = next.order_no;
      else delete draft.current[method];
    } catch {
      setSubmitError(true);
      setRefreshKey((n) => n + 1);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <section className="console-panel cny-topup" aria-labelledby="cny-topup-heading">
      <div className="console-panel__heading">
        <CreditCard aria-hidden="true" size={20} />
        <h2 id="cny-topup-heading">{t(methodLabels[method])}</h2>
      </div>
      {config === undefined && <p className="console-field__help" aria-live="polite">{t('正在加载充值配置...')}</p>}
      {config === null && <p className="console-alert" role="alert">{t('充值配置加载失败，请稍后重试。')}</p>}
      {config && !methodEnabled && <p className="console-field__help">{t('该支付方式暂不可用。')}</p>}
      {config?.enabled && methodEnabled && (
        <form className="cny-topup__form" onSubmit={(event) => void create(event)}>
          <div className="console-field">
            <label htmlFor="cny-topup-amount">{t('充值金额')}</label>
            <div className="wallet-amount-control">
              <input id="cny-topup-amount" type="text" inputMode="decimal" maxLength={11} value={amount}
                disabled={submitting} onChange={(event) => setAmount(event.target.value)} />
              <span>¥</span>
            </div>
            <p className="console-field__help">{config.max_cny ? `¥${config.min_cny} - ¥${config.max_cny} · ` : ''}1 U = ¥{config.cny_per_u}</p>
          </div>
          <button className="console-button console-button--primary" type="submit" disabled={!amountValid || submitting}>
            {t(submitting ? '正在创建...' : '创建充值订单')}
          </button>
          {submitError && <p className="console-alert" role="alert">{t('订单结果暂未确认，请查看记录后重试原订单。')}</p>}
        </form>
      )}
      {(config?.enabled || orders.length > 0) && <p className="console-field__help"><a href="https://t.me/gan66" target="_blank" rel="noopener noreferrer">{t('退款请联系平台客服')}</a></p>}
      {(historyError || orders.length > 0) && <button type="button" className="console-button" onClick={() => setRefreshKey((n) => n + 1)}>
        <RefreshCw aria-hidden="true" size={16} />{t('重新查询订单')}
      </button>}
      {orders.length > 0 && <ul className="cny-topup__orders" aria-live="polite">
        {orders.map((order) => {
          const canPay = config?.enabled && order.status === 'pending';
          const checkout = canPay ? safeCheckout(order.checkout_url) : null;
          const qr = canPay ? order.checkout_qr : undefined;
          return <li key={order.order_no}>
            <div><strong>{t(statusLabels[order.status] ?? '状态待核验')}</strong><span className="cny-topup__id">{t(methodLabels[order.method])} · {order.order_no}</span></div>
            <dl><div><dt>{t('应付金额')}</dt><dd>¥{order.amount_cny}</dd></div>
              <div><dt>{t('订单额度')}</dt><dd>{order.credit_u} U</dd></div></dl>
            {checkout && <a className="console-button" href={checkout} target="_blank" rel="noopener noreferrer">
              {t('前往付款')}<ExternalLink aria-hidden="true" size={16} />
            </a>}
            {qr && <div className="cny-topup__qr">
              <QRCodeSVG value={qr} size={160} marginSize={4} role="img" aria-label={t('付款二维码')} />
              <span>{t(order.method === 'wxpay' ? '微信扫码付款' : '支付宝扫码付款')}</span>
            </div>}
            {order.status === 'pending' && !checkout && !qr && <p className="console-field__help">{t('付款状态待确认，请勿重复支付。')}</p>}
          </li>;
        })}
      </ul>}
    </section>
  );
}
