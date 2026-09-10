import React from 'react';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import USDTTopUpPanel from './USDTTopUpPanel';

vi.mock('qrcode.react', () => ({
  QRCodeSVG: ({ value }) => (
    <div aria-label='USDT 收款地址二维码' data-qr-value={value} />
  ),
}));

const address = 'TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2';
const pendingOrder = {
  id: 17,
  trade_no: 'ZT-USDT-17',
  credit_units: 10,
  pay_amount: '10.37',
  receiving_address: address,
  network: 'TRC-20',
  asset: 'USDT',
  expires_at: 1_900_000_600,
  status: 'pending',
};

function createRequest(responses) {
  const queue = [...responses];
  return vi.fn(async () => {
    if (queue.length === 0) throw new Error('unexpected request');
    return queue.shift();
  });
}

describe('USDTTopUpPanel', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('creates a whole-unit order and shows the exact TRC-20 payment instruction', async () => {
    const request = createRequest([{ success: true, data: pendingOrder }]);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });

    render(
      <USDTTopUpPanel
        enabled
        minimumTopUp={10}
        request={request}
        now={() => 1_900_000_000_000}
      />,
    );

    fireEvent.change(screen.getByLabelText('充值数量'), {
      target: { value: '10' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 USDT 订单' }));

    await screen.findByText('10.37 USDT');
    expect(screen.getByText('TRC-20')).toBeInTheDocument();
    expect(screen.getByText('等待到账')).toBeInTheDocument();
    expect(screen.getByText('10:00')).toBeInTheDocument();
    expect(screen.getByLabelText('USDT 收款地址二维码')).toHaveAttribute(
      'data-qr-value',
      address,
    );
    expect(request).toHaveBeenCalledWith({
      method: 'POST',
      url: '/api/user/topup/usdt-trc20/orders',
      body: { amount: 10 },
    });

    fireEvent.click(screen.getByRole('button', { name: '复制金额' }));
    fireEvent.click(screen.getByRole('button', { name: '复制地址' }));
    await waitFor(() => {
      expect(writeText).toHaveBeenNthCalledWith(1, '10.37');
      expect(writeText).toHaveBeenNthCalledWith(2, address);
    });
  });

  it('polls a pending order every two seconds and reports settlement once', async () => {
    vi.useFakeTimers();
    const onSettled = vi.fn();
    const request = createRequest([
      {
        success: true,
        data: { ...pendingOrder, status: 'settled', settled_at: 1_900_000_002 },
      },
    ]);

    render(
      <USDTTopUpPanel
        enabled
        initialOrder={pendingOrder}
        request={request}
        onSettled={onSettled}
        now={() => 1_900_000_000_000}
      />,
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_999);
    });
    expect(request).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(screen.getByText('已到账')).toBeInTheDocument();
    expect(request).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledWith({
      method: 'GET',
      url: '/api/user/topup/usdt-trc20/orders/ZT-USDT-17',
    });
    expect(onSettled).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_000);
    });
    expect(request).toHaveBeenCalledTimes(1);
    expect(onSettled).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['expired', '已过期'],
    ['manual_review', '需人工核对'],
  ])('renders %s orders as %s', (status, label) => {
    render(
      <USDTTopUpPanel
        enabled
        initialOrder={{ ...pendingOrder, status }}
        request={vi.fn()}
        now={() => 1_900_000_000_000}
      />,
    );

    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it('closes locally without cancelling the server order', () => {
    const request = vi.fn();
    const onClose = vi.fn();
    render(
      <USDTTopUpPanel
        enabled
        initialOrder={pendingOrder}
        request={request}
        onClose={onClose}
        now={() => 1_900_000_000_000}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: '关闭 USDT 充值' }));

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(request).not.toHaveBeenCalled();
  });
});
