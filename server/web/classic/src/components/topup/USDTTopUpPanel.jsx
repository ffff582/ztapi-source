import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Banner, Button, InputNumber, Tag, Typography } from '@douyinfe/semi-ui';
import {
  CheckCircle2,
  Clock3,
  Copy,
  ShieldCheck,
  TriangleAlert,
  X,
} from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';

const { Text, Title } = Typography;

const statusLabels = {
  pending: '等待到账',
  settled: '已到账',
  expired: '已过期',
  manual_review: '需人工核对',
};

const statusColors = {
  pending: 'amber',
  settled: 'green',
  expired: 'grey',
  manual_review: 'red',
};

function unwrapResponse(response) {
  if (!response || response.success !== true || !response.data) {
    throw new Error(response?.message || 'USDT 订单请求失败');
  }
  return response.data;
}

function formatCountdown(seconds) {
  const safeSeconds = Math.max(0, seconds);
  const minutes = Math.floor(safeSeconds / 60);
  const remainder = safeSeconds % 60;
  return `${String(minutes).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`;
}

function secondsUntil(expiresAt, now) {
  return Math.max(0, Math.ceil(expiresAt - now() / 1000));
}

const USDTTopUpPanel = ({
  enabled = false,
  minimumTopUp = 10,
  initialOrder = null,
  request,
  onClose = () => {},
  onOrderChange = () => {},
  onSettled = () => {},
  now = Date.now,
}) => {
  const [amount, setAmount] = useState(minimumTopUp);
  const [order, setOrder] = useState(initialOrder);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [remainingSeconds, setRemainingSeconds] = useState(() =>
    initialOrder ? secondsUntil(initialOrder.expires_at, now) : 0,
  );
  const settledNotifiedRef = useRef(initialOrder?.status === 'settled');

  const displayStatus = useMemo(() => {
    if (order?.status === 'pending' && remainingSeconds === 0) {
      return 'expired';
    }
    return order?.status || 'pending';
  }, [order?.status, remainingSeconds]);

  useEffect(() => {
    if (!order) return undefined;
    const updateCountdown = () => {
      setRemainingSeconds(secondsUntil(order.expires_at, now));
    };
    updateCountdown();
    if (order.status !== 'pending') return undefined;
    const timer = window.setInterval(updateCountdown, 1_000);
    return () => window.clearInterval(timer);
  }, [order, now]);

  useEffect(() => {
    if (!order || order.status !== 'pending') return undefined;
    let disposed = false;
    let polling = false;
    const timer = window.setInterval(async () => {
      if (polling || disposed) return;
      polling = true;
      try {
        const response = await request({
          method: 'GET',
          url: `/api/user/topup/usdt-trc20/orders/${encodeURIComponent(order.trade_no)}`,
        });
        const nextOrder = unwrapResponse(response);
        if (disposed) return;
        setOrder(nextOrder);
        onOrderChange(nextOrder);
        if (nextOrder.status === 'settled' && !settledNotifiedRef.current) {
          settledNotifiedRef.current = true;
          onSettled(nextOrder);
        }
      } catch (pollError) {
        if (!disposed) setError(pollError.message || '订单状态查询失败');
      } finally {
        polling = false;
      }
    }, 2_000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [onOrderChange, onSettled, order, request]);

  if (!enabled) return null;

  const createOrder = async () => {
    setError('');
    if (!Number.isInteger(amount) || amount < minimumTopUp) {
      setError(`最低充值 ${minimumTopUp} USDT，且只能输入整数`);
      return;
    }
    setSubmitting(true);
    try {
      const response = await request({
        method: 'POST',
        url: '/api/user/topup/usdt-trc20/orders',
        body: { amount },
      });
      const nextOrder = unwrapResponse(response);
      settledNotifiedRef.current = nextOrder.status === 'settled';
      setOrder(nextOrder);
      onOrderChange(nextOrder);
      setRemainingSeconds(secondsUntil(nextOrder.expires_at, now));
    } catch (createError) {
      setError(createError.message || 'USDT 订单创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  const copy = async (value) => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      setError('复制失败，请手动复制');
    }
  };

  if (!order) {
    return (
      <section className='w-full border-t border-semi-color-border pt-5'>
        <div className='mb-4 flex items-start justify-between gap-4'>
          <div>
            <Title heading={6} className='!m-0'>
              USDT 充值
            </Title>
            <Text type='tertiary'>仅支持 TRC-20 网络，最低 {minimumTopUp} USDT</Text>
          </div>
          <ShieldCheck size={22} aria-hidden='true' />
        </div>
        {error && <Banner type='danger' description={error} closeIcon={null} />}
        <div className='mt-4 flex flex-col gap-3 sm:flex-row sm:items-end'>
          <label className='flex min-w-0 flex-1 flex-col gap-2'>
            <Text strong>充值数量</Text>
            <InputNumber
              aria-label='充值数量'
              value={amount}
              min={minimumTopUp}
              step={1}
              precision={0}
              onChange={(value) => setAmount(Number(value))}
              style={{ width: '100%' }}
            />
          </label>
          <Button
            type='primary'
            theme='solid'
            loading={submitting}
            onClick={createOrder}
          >
            创建 USDT 订单
          </Button>
        </div>
      </section>
    );
  }

  return (
    <section className='w-full border-t border-semi-color-border pt-5'>
      <div className='mb-4 flex items-start justify-between gap-4'>
        <div className='min-w-0'>
          <div className='flex flex-wrap items-center gap-2'>
            <Title heading={6} className='!m-0'>
              USDT 充值
            </Title>
            <Tag color={statusColors[displayStatus]}>
              {statusLabels[displayStatus]}
            </Tag>
          </div>
          <Text type='tertiary'>订单号：{order.trade_no}</Text>
        </div>
        <Button
          icon={<X size={18} />}
          theme='borderless'
          aria-label='关闭 USDT 充值'
          onClick={onClose}
        />
      </div>

      {error && <Banner type='danger' description={error} closeIcon={null} />}

      <div className='mt-4 grid grid-cols-1 gap-5 md:grid-cols-[196px_minmax(0,1fr)]'>
        <div className='flex min-h-[196px] items-center justify-center bg-white p-3'>
          <QRCodeSVG
            value={order.receiving_address}
            size={172}
            level='M'
            aria-label='USDT 收款地址二维码'
          />
        </div>

        <div className='min-w-0 space-y-4'>
          <div>
            <Text type='tertiary'>应付金额</Text>
            <div className='mt-1 flex flex-wrap items-center gap-2'>
              <Title heading={3} className='!m-0'>
                {order.pay_amount} USDT
              </Title>
              <Button
                icon={<Copy size={16} />}
                theme='borderless'
                aria-label='复制金额'
                onClick={() => copy(order.pay_amount)}
              />
            </div>
          </div>

          <div>
            <Text type='tertiary'>收款地址</Text>
            <div className='mt-1 flex items-start gap-2'>
              <Text code className='min-w-0 break-all'>
                {order.receiving_address}
              </Text>
              <Button
                icon={<Copy size={16} />}
                theme='borderless'
                aria-label='复制地址'
                onClick={() => copy(order.receiving_address)}
              />
            </div>
          </div>

          <div className='flex flex-wrap items-center gap-3'>
            <Tag color='blue'>{order.network}</Tag>
            <span className='inline-flex items-center gap-1.5'>
              {displayStatus === 'settled' ? (
                <CheckCircle2 size={17} aria-hidden='true' />
              ) : displayStatus === 'manual_review' ? (
                <TriangleAlert size={17} aria-hidden='true' />
              ) : (
                <Clock3 size={17} aria-hidden='true' />
              )}
              <Text strong>{formatCountdown(remainingSeconds)}</Text>
            </span>
          </div>

          <Banner
            type='warning'
            closeIcon={null}
            description='必须使用 TRC-20 网络并支付完整精确金额；少付、多付或使用其他网络都不会自动到账。'
          />
        </div>
      </div>
    </section>
  );
};

export default USDTTopUpPanel;
