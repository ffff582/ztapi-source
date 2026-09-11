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
import { localeTag, useLocale } from '../../i18n/locale';

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
  const { locale, t } = useLocale();
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
        t('最低充值 {{amount}} USDT，且只能输入整数。', { amount: configuration?.minimum_top_up ?? 10 }),
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
      setError(t('暂时无法创建充值订单，请稍后重试。'));
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
      setError(t('复制失败，请手动选择并复制。'));
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
          <p className="console-eyebrow">{t('账户余额')}</p>
          <h1>{t('余额充值')}</h1>
        </div>
        <p>{t('通过 TRON Mainnet 的 USDT（TRC-20）为当前 ZTAPI 账户充值。')}</p>
      </header>

      <AccountBalance refreshKey={`${order?.trade_no ?? ''}:${order?.status ?? ''}`} />
      <PendingSettlements />

      {pageState === 'loading' && (
        <div className="console-state" aria-live="polite" aria-busy="true">
          {t('正在加载充值配置...')}
        </div>
      )}
      {pageState === 'error' && (
        <div className="console-state console-state--error" role="alert">
          {t('充值配置加载失败，请稍后重试。')}
        </div>
      )}
      {pageState === 'ready' && configuration !== null && !configuration.enabled && (
        <div className="console-state">{t('USDT 充值当前未开放。')}</div>
      )}

      {pageState === 'ready' && configuration?.enabled && (
        <div className="wallet-workbench">
          <section className="console-panel wallet-create" aria-labelledby="wallet-create-heading">
            <div className="console-panel__heading">
              <WalletCards aria-hidden="true" size={20} />
              <h2 id="wallet-create-heading">{t('USDT 充值')}</h2>
            </div>
            <p className="wallet-lead">
              {t('输入需要到账的整数额度。系统会生成一个 10 分钟有效的唯一付款金额。')}
            </p>

            <div className="console-field wallet-amount-field">
              <label htmlFor="wallet-amount">{t('充值数量')}</label>
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
                {t('最低 {{amount}} USDT，只计入整数额度。', { amount: configuration.minimum_top_up })}
              </p>
            </div>

            {error !== '' && <div className="console-alert" role="alert">{error}</div>}

            {orderIsOpen && !showOrder ? (
              <button
                className="console-button console-button--primary wallet-create__submit"
                type="button"
                onClick={() => setShowOrder(true)}
              >
                {t(visibleOrderStatus === 'pending' ? '继续支付现有订单' : '查看确认进度')}
              </button>
            ) : (
              <button
                className="console-button console-button--primary wallet-create__submit"
                disabled={creating || orderIsOpen}
                type="button"
                onClick={() => void createOrder()}
              >
                {t(creating ? '正在创建...' : '创建支付订单')}
              </button>
            )}

            <div className="wallet-rules" aria-label={t('充值规则')}>
              <span><ShieldCheck aria-hidden="true" size={16} />{t('到账后自动入账')}</span>
              <span><Clock3 aria-hidden="true" size={16} />{t('订单有效期 10 分钟')}</span>
              <span><QrCode aria-hidden="true" size={16} />{t('仅支持 TRC-20')}</span>
            </div>
          </section>

          {order !== null && showOrder && status !== null ? (
            <section className="console-panel wallet-order" aria-labelledby="wallet-order-heading">
              <button
                aria-label={t('关闭支付详情')}
                className="wallet-order__close"
                title={t('关闭支付详情')}
                type="button"
                onClick={() => setShowOrder(false)}
              >
                <X aria-hidden="true" size={18} />
              </button>

              <div className={`wallet-order__status wallet-order__status--${status.className}`}>
                <status.icon aria-hidden="true" size={20} />
                <div>
                  <h2 id="wallet-order-heading">{t(status.label)}</h2>
                  <p>{t(status.description)}</p>
                </div>
              </div>

              {order.status === 'settled' && (
                <dl className="wallet-receipt">
                  <div><dt>{t('本次入账')}</dt><dd>{formatAccountUSD(order.credit_units)} USD</dd></div>
                  <div><dt>{t('实际支付')}</dt><dd>{order.pay_amount} USDT</dd></div>
                  <div><dt>{t('到账时间')}</dt><dd>{formatTopUpTime(order.settled_at ?? 0, locale)} </dd></div>
                  <div><dt>{t('订单号')}</dt><dd><code>{order.trade_no}</code></dd></div>
                  {order.tx_id && <div><dt>{t('链上交易')}</dt><dd><code>{order.tx_id}</code></dd></div>}
                </dl>
              )}

              {visibleOrderStatus === 'pending' && <div className="wallet-payment-grid">
                <div className="wallet-qr" data-qr-value={order.receiving_address} data-testid="usdt-address-qr">
                  <QRCodeSVG
                    aria-label={t('USDT 收款地址二维码')}
                    bgColor="#ffffff"
                    fgColor="#080a0d"
                    level="M"
                    size={184}
                    value={order.receiving_address}
                  />
                </div>

                <div className="wallet-payment-details">
                  <div className="wallet-payment-row wallet-payment-row--amount">
                    <span>{t('必须支付的唯一金额')}</span>
                    <div>
                      <strong>{order.pay_amount} USDT</strong>
                      <button
                        aria-label={t('复制唯一付款金额')}
                        className="console-icon-action"
                        title={t('复制金额')}
                        type="button"
                        onClick={() => void copyValue('amount', order.pay_amount)}
                      >
                        <Clipboard aria-hidden="true" size={16} />
                        <span>{t(copied === 'amount' ? '已复制' : '复制')}</span>
                      </button>
                    </div>
                  </div>
                  <div className="wallet-payment-row">
                    <span>{t('网络')}</span>
                    <strong>TRON Mainnet · TRC-20</strong>
                  </div>
                  <div className="wallet-payment-row">
                    <span>{t('收款地址')}</span>
                    <div>
                      <code>{order.receiving_address}</code>
                      <button
                        aria-label={t('复制收款地址')}
                        className="console-icon-action"
                        title={t('复制地址')}
                        type="button"
                        onClick={() => void copyValue('address', order.receiving_address)}
                      >
                        <Clipboard aria-hidden="true" size={16} />
                        <span>{t(copied === 'address' ? '已复制' : '复制')}</span>
                      </button>
                    </div>
                  </div>
                  <div className="wallet-payment-row">
                    <span>{t(order.status === 'pending' ? '剩余时间' : '订单状态')}</span>
                    <strong>
                      {order.status === 'pending'
                        ? formatCountdown(countdown)
                        : order.status === 'settled'
                          ? t('已确认')
                          : order.status === 'expired'
                            ? t('已结束')
                            : t('人工核验中')}
                    </strong>
                  </div>
                </div>
              </div>}

              {visibleOrderStatus === 'pending' && <p className="wallet-warning">
                {t('只发送 USDT（TRC-20），且金额必须与上方数字完全一致。转错网络或金额无法自动入账。')}
              </p>}
            </section>
          ) : (
            <section className="console-panel wallet-guide" aria-label={t('充值说明')}>
              <p className="console-eyebrow">{t('付款流程')}</p>
              <ol>
                <li><span>1</span>{t('创建唯一付款金额')}</li>
                <li><span>2</span>{t('使用 TRON 钱包准确转账')}</li>
                <li><span>3</span>{t('链上确认后自动到账')}</li>
              </ol>
              <p>{t('没有待支付订单时，后台不会持续查询链上交易。')}</p>
            </section>
          )}
        </div>
      )}
      <TopUpHistory key={`${order?.trade_no ?? ''}:${order?.status ?? ''}`} />
    </div>
  );
}
