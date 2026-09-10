import {
  CheckCircle2,
  Clipboard,
  Clock3,
  QrCode,
  ShieldCheck,
  TriangleAlert,
  WalletCards,
  X,
} from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { apiClient } from '../../api/client';
import { AccountBalance, formatAccountUSD } from './AccountBalance';
import { PendingSettlements } from './PendingSettlements';
import { TopUpHistory, formatTopUpTime } from './TopUpHistory';
import {
  parseUSDTTopUpInfo,
  parseUSDTTopUpOrder,
  type USDTTopUpInfo,
  type USDTTopUpOrder,
} from '../../api/contracts';

const PENDING_ORDER_STORAGE_KEY = 'ztapi.usdt.pending_trade_no';
const POLL_INTERVAL_MS = 2_000;

type PageState = 'loading' | 'ready' | 'error';

const statusContent = {
  pending: {
    label: '等待链上确认',
    description: '仅按下方唯一金额转账，系统正在核验 TRON 链上到账记录。',
    className: 'pending',
    icon: Clock3,
  },
  confirming: {
    label: '正在确认到账',
    description: '付款窗口已经结束，请勿继续转账。系统正在核验有效期内发出的链上交易。',
    className: 'confirming',
    icon: ShieldCheck,
  },
  settled: {
    label: '充值已到账',
    description: '链上转账已确认，充值额度已经计入当前账户。',
    className: 'settled',
    icon: CheckCircle2,
  },
  expired: {
    label: '订单已过期',
    description: '该唯一金额已失效，请重新创建订单后再转账。',
    className: 'expired',
    icon: TriangleAlert,
  },
  manual_review: {
    label: '订单待人工核验',
    description: '系统发现需要人工确认的链上记录，请勿重复转账。',
    className: 'review',
    icon: ShieldCheck,
  },
} as const;

function remainingSeconds(expiresAt: number) {
  return Math.max(0, expiresAt - Math.floor(Date.now() / 1_000));
}

function formatCountdown(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return `${String(minutes).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`;
}

export function WalletPage() {
  const [pageState, setPageState] = useState<PageState>('loading');
  const [configuration, setConfiguration] = useState<USDTTopUpInfo | null>(null);
  const [amount, setAmount] = useState('');
  const [order, setOrder] = useState<USDTTopUpOrder | null>(null);
  const [showOrder, setShowOrder] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState<'amount' | 'address' | null>(null);
  const [countdown, setCountdown] = useState(0);
  const pollingRef = useRef(false);

  const commitOrder = useCallback((nextOrder: USDTTopUpOrder) => {
    setOrder(nextOrder);
    setShowOrder(true);
    setCountdown(remainingSeconds(nextOrder.expires_at));
    if (nextOrder.status === 'pending' || nextOrder.status === 'confirming') {
      sessionStorage.setItem(PENDING_ORDER_STORAGE_KEY, nextOrder.trade_no);
    } else {
      sessionStorage.removeItem(PENDING_ORDER_STORAGE_KEY);
    }
  }, []);

  useEffect(() => {
    let active = true;
    const pendingTradeNo = sessionStorage.getItem(PENDING_ORDER_STORAGE_KEY);

    void apiClient
      .get<unknown>('/user/topup/info')
      .then(parseUSDTTopUpInfo)
      .then(async (nextConfiguration) => {
        if (!active) {
          return;
        }
        setConfiguration(nextConfiguration);
        setAmount(String(nextConfiguration.minimum_top_up));
        setPageState('ready');

        if (pendingTradeNo !== null) {
          const restored = parseUSDTTopUpOrder(
            await apiClient.get<unknown>(
              `/user/topup/usdt-trc20/orders/${encodeURIComponent(pendingTradeNo)}`,
            ),
          );
          if (active) {
            commitOrder(restored);
          }
        }
      })
      .catch(() => {
        if (active) {
          sessionStorage.removeItem(PENDING_ORDER_STORAGE_KEY);
          setPageState('error');
        }
      });

    return () => {
      active = false;
    };
  }, [commitOrder]);

  useEffect(() => {
    if (order?.status !== 'pending' && order?.status !== 'confirming') {
      return undefined;
    }

    const timer = window.setInterval(() => {
      setCountdown(remainingSeconds(order.expires_at));
    }, 1_000);
    return () => window.clearInterval(timer);
  }, [order]);

  useEffect(() => {
    if (order?.status !== 'pending' && order?.status !== 'confirming') {
      return undefined;
    }

    let active = true;
    const poll = async () => {
      if (pollingRef.current) {
        return;
      }
      pollingRef.current = true;
      try {
        const nextOrder = parseUSDTTopUpOrder(
          await apiClient.get<unknown>(
            `/user/topup/usdt-trc20/orders/${encodeURIComponent(order.trade_no)}`,
          ),
        );
        if (active) {
          commitOrder(nextOrder);
        }
      } catch {
        // A transient status request failure must not discard a valid payment order.
      } finally {
        pollingRef.current = false;
      }
    };
    const timer = window.setInterval(() => void poll(), POLL_INTERVAL_MS);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [commitOrder, order]);

  const parsedAmount = Number(amount);
  const orderIsOpen = order?.status === 'pending' || order?.status === 'confirming';
  const visibleOrderStatus =
    order?.status === 'pending' && countdown === 0 ? 'confirming' : order?.status;
  const amountIsValid =
    configuration !== null &&
    Number.isInteger(parsedAmount) &&
    parsedAmount >= configuration.minimum_top_up;

  async function createOrder() {
    if (!amountIsValid || configuration === null) {
      setError(
        `最低充值 ${configuration?.minimum_top_up ?? 10} USDT，且只能输入整数。`,
      );
      return;
    }

    setError('');
    setCreating(true);
    try {
      const created = parseUSDTTopUpOrder(
        await apiClient.post<unknown>('/user/topup/usdt-trc20/orders', {
          amount: parsedAmount,
        }),
      );
      commitOrder(created);
    } catch {
      setError('暂时无法创建充值订单，请稍后重试。');
    } finally {
      setCreating(false);
    }
  }

  async function copyValue(kind: 'amount' | 'address', value: string) {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(kind);
      window.setTimeout(() => setCopied(null), 1_500);
    } catch {
      setError('复制失败，请手动选择并复制。');
    }
  }

  const status = useMemo(
    () => (visibleOrderStatus === undefined ? null : statusContent[visibleOrderStatus]),
    [visibleOrderStatus],
  );

  return (
    <div className="console-page wallet-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">账户余额</p>
          <h1>余额充值</h1>
        </div>
        <p>通过 TRON Mainnet 的 USDT（TRC-20）为当前 ZTAPI 账户充值。</p>
      </header>

      <AccountBalance refreshKey={`${order?.trade_no ?? ''}:${order?.status ?? ''}`} />
      <PendingSettlements />

      {pageState === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          正在加载充值配置...
        </div>
      )}
      {pageState === 'error' && (
        <div className="console-state console-state--error" role="alert">
          充值配置加载失败，请稍后重试。
        </div>
      )}
      {pageState === 'ready' && configuration !== null && !configuration.enabled && (
        <div className="console-state">USDT 充值当前未开放。</div>
      )}

      {pageState === 'ready' && configuration?.enabled && (
        <div className="wallet-workbench">
          <section className="console-panel wallet-create" aria-labelledby="wallet-create-heading">
            <div className="console-panel__heading">
              <WalletCards aria-hidden="true" size={20} />
              <h2 id="wallet-create-heading">USDT 充值</h2>
            </div>
            <p className="wallet-lead">
              输入需要到账的整数额度。系统会生成一个 10 分钟有效的唯一付款金额。
            </p>

            <div className="console-field wallet-amount-field">
              <label htmlFor="wallet-amount">充值数量</label>
              <div className="wallet-amount-control">
                <input
                  id="wallet-amount"
                  inputMode="numeric"
                  min={configuration.minimum_top_up}
                  step="1"
                  type="number"
                  value={amount}
                  onChange={(event) => {
                    setAmount(event.target.value);
                    setError('');
                  }}
                />
                <span>USDT</span>
              </div>
              <p className="console-field__help">
                最低 {configuration.minimum_top_up} USDT，只计入整数额度。
              </p>
            </div>

            {error !== '' && <div className="console-alert" role="alert">{error}</div>}

            {orderIsOpen && !showOrder ? (
              <button
                className="console-button console-button--primary wallet-create__submit"
                type="button"
                onClick={() => setShowOrder(true)}
              >
                {visibleOrderStatus === 'pending' ? '继续支付现有订单' : '查看确认进度'}
              </button>
            ) : (
              <button
                className="console-button console-button--primary wallet-create__submit"
                disabled={creating || orderIsOpen}
                type="button"
                onClick={() => void createOrder()}
              >
                {creating ? '正在创建...' : '创建支付订单'}
              </button>
            )}

            <div className="wallet-rules" aria-label="充值规则">
              <span><ShieldCheck aria-hidden="true" size={16} />到账后自动入账</span>
              <span><Clock3 aria-hidden="true" size={16} />订单有效期 10 分钟</span>
              <span><QrCode aria-hidden="true" size={16} />仅支持 TRC-20</span>
            </div>
          </section>

          {order !== null && showOrder && status !== null ? (
            <section className="console-panel wallet-order" aria-labelledby="wallet-order-heading">
              <button
                aria-label="关闭支付详情"
                className="wallet-order__close"
                title="关闭支付详情"
                type="button"
                onClick={() => setShowOrder(false)}
              >
                <X aria-hidden="true" size={18} />
              </button>

              <div className={`wallet-order__status wallet-order__status--${status.className}`}>
                <status.icon aria-hidden="true" size={20} />
                <div>
                  <h2 id="wallet-order-heading">{status.label}</h2>
                  <p>{status.description}</p>
                </div>
              </div>

              {order.status === 'settled' && (
                <dl className="wallet-receipt">
                  <div><dt>本次入账</dt><dd>{formatAccountUSD(order.credit_units)} USD</dd></div>
                  <div><dt>实际支付</dt><dd>{order.pay_amount} USDT</dd></div>
                  <div><dt>到账时间</dt><dd>{formatTopUpTime(order.settled_at ?? 0)}</dd></div>
                  <div><dt>订单号</dt><dd><code>{order.trade_no}</code></dd></div>
                  {order.tx_id && <div><dt>链上交易</dt><dd><code>{order.tx_id}</code></dd></div>}
                </dl>
              )}

              {visibleOrderStatus === 'pending' && <div className="wallet-payment-grid">
                <div className="wallet-qr" data-qr-value={order.receiving_address} data-testid="usdt-address-qr">
                  <QRCodeSVG
                    aria-label="USDT 收款地址二维码"
                    bgColor="#ffffff"
                    fgColor="#080a0d"
                    level="M"
                    size={184}
                    value={order.receiving_address}
                  />
                </div>

                <div className="wallet-payment-details">
                  <div className="wallet-payment-row wallet-payment-row--amount">
                    <span>必须支付的唯一金额</span>
                    <div>
                      <strong>{order.pay_amount} USDT</strong>
                      <button
                        aria-label="复制唯一付款金额"
                        className="console-icon-action"
                        title="复制金额"
                        type="button"
                        onClick={() => void copyValue('amount', order.pay_amount)}
                      >
                        <Clipboard aria-hidden="true" size={16} />
                        <span>{copied === 'amount' ? '已复制' : '复制'}</span>
                      </button>
                    </div>
                  </div>
                  <div className="wallet-payment-row">
                    <span>网络</span>
                    <strong>TRON Mainnet · TRC-20</strong>
                  </div>
                  <div className="wallet-payment-row">
                    <span>收款地址</span>
                    <div>
                      <code>{order.receiving_address}</code>
                      <button
                        aria-label="复制收款地址"
                        className="console-icon-action"
                        title="复制地址"
                        type="button"
                        onClick={() => void copyValue('address', order.receiving_address)}
                      >
                        <Clipboard aria-hidden="true" size={16} />
                        <span>{copied === 'address' ? '已复制' : '复制'}</span>
                      </button>
                    </div>
                  </div>
                  <div className="wallet-payment-row">
                    <span>{order.status === 'pending' ? '剩余时间' : '订单状态'}</span>
                    <strong>
                      {order.status === 'pending'
                        ? formatCountdown(countdown)
                        : order.status === 'settled'
                          ? '已确认'
                          : order.status === 'expired'
                            ? '已结束'
                            : '人工核验中'}
                    </strong>
                  </div>
                </div>
              </div>}

              {visibleOrderStatus === 'pending' && <p className="wallet-warning">
                只发送 USDT（TRC-20），且金额必须与上方数字完全一致。转错网络或金额无法自动入账。
              </p>}
            </section>
          ) : (
            <section className="console-panel wallet-guide" aria-label="充值说明">
              <p className="console-eyebrow">付款流程</p>
              <ol>
                <li><span>1</span>创建唯一付款金额</li>
                <li><span>2</span>使用 TRON 钱包准确转账</li>
                <li><span>3</span>链上确认后自动到账</li>
              </ol>
              <p>没有待支付订单时，后台不会持续查询链上交易。</p>
            </section>
          )}
        </div>
      )}
      <TopUpHistory key={`${order?.trade_no ?? ''}:${order?.status ?? ''}`} />
    </div>
  );
}
