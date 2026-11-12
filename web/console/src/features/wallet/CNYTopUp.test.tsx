import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { apiClient } from '../../api/client';
import { CNYTopUp } from './CNYTopUp';

const onBalanceChange = () => undefined;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it('does not offer CNY payment when new orders are closed', async () => {
  const get = vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: false }).mockResolvedValueOnce([]);
  await act(async () => { render(<CNYTopUp method="alipay" onBalanceChange={onBalanceChange} />); });
  await waitFor(() => expect(get).toHaveBeenCalledWith('/user/topup/cny/orders'));
  expect(screen.getByText('该支付方式暂不可用。')).toBeVisible();
  expect(screen.queryByLabelText('充值金额')).not.toBeInTheDocument();
});

it('lets a customer enter an arbitrary cent amount and shows the locked order before leaving the site', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', max_cny: '50.00', cny_per_u: '6.67', methods: ['alipay', 'wxpay'] }).mockResolvedValueOnce([]);
  const post = vi.spyOn(apiClient, 'post').mockResolvedValue({
    order_no: 'CNY-test-order', status: 'pending', method: 'wxpay', amount_cny: '12.37', credit_u: '1.854572', cny_per_u: '6.67',
    checkout_url: 'https://checkout.example.test/order', created_at: Date.now() / 1000,
  });
  render(<CNYTopUp method="wxpay" onBalanceChange={onBalanceChange} />);
  fireEvent.change(await screen.findByLabelText('充值金额'), { target: { value: '12.37' } });
  fireEvent.click(screen.getByRole('button', { name: '创建充值订单' }));
  expect(await screen.findByText('¥12.37')).toBeVisible();
  expect(screen.getByText('1.854572 U')).toBeVisible();
  expect(screen.getByRole('link', { name: '前往付款' })).toHaveAttribute('href', 'https://checkout.example.test/order');
  expect(post).toHaveBeenCalledWith('/user/topup/cny/orders', { amount_cny: '12.37', method: 'wxpay', idempotency_key: expect.any(String) });
});

it('renders a QR-only checkout as a local QR image without a navigation link', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', max_cny: '1.00', cny_per_u: '6.67', methods: ['alipay', 'wxpay'] })
    .mockResolvedValue([{
      order_no: 'CNY-test-qr', status: 'pending', method: 'wxpay', amount_cny: '1.00', credit_u: '0.149925',
      checkout_url: '', checkout_qr: 'weixin://wxpay/bizpayurl/up?pr=fixture', created_at: Date.now() / 1000,
    }]);
  render(<CNYTopUp method="wxpay" onBalanceChange={onBalanceChange} />);
  const qr = await screen.findByRole('img', { name: '付款二维码' });
  expect(qr.tagName).toBe('svg');
  expect(qr.querySelector('image')).toBeNull();
  expect(screen.queryByRole('link', { name: '前往付款' })).not.toBeInTheDocument();
});

it('does not silently substitute an enabled method for the selected unavailable method', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', max_cny: '50.00', cny_per_u: '7', methods: ['alipay'] }).mockResolvedValueOnce([]);
  const { rerender } = render(<CNYTopUp method="wxpay" onBalanceChange={onBalanceChange} />);
  expect(await screen.findByText('该支付方式暂不可用。')).toBeVisible();
  expect(screen.queryByRole('button', { name: '创建充值订单' })).not.toBeInTheDocument();
  rerender(<CNYTopUp method="alipay" onBalanceChange={onBalanceChange} />);
  expect(screen.getByLabelText('充值金额')).toBeVisible();
  expect(screen.getByRole('link', { name: '退款请联系平台客服' })).toHaveAttribute('href', 'https://t.me/gan66');
});

it('keeps an uncertain order retry tied to its original payment method after switching', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', cny_per_u: '7', methods: ['alipay', 'wxpay'] }).mockResolvedValue([]);
  const post = vi.spyOn(apiClient, 'post').mockRejectedValue(new Error('Order result unknown'));
  const { rerender } = render(<CNYTopUp method="alipay" onBalanceChange={onBalanceChange} />);
  fireEvent.change(await screen.findByLabelText('充值金额'), { target: { value: '12.37' } });

  async function submit() {
    await act(async () => fireEvent.click(screen.getByRole('button', { name: '创建充值订单' })));
  }

  await submit();
  rerender(<CNYTopUp method="wxpay" onBalanceChange={onBalanceChange} />);
  await submit();
  rerender(<CNYTopUp method="alipay" onBalanceChange={onBalanceChange} />);
  await submit();
  const bodies = post.mock.calls.map(([, body]) => body as { idempotency_key: string });
  expect(bodies).toHaveLength(3);
  expect(bodies[0]).toMatchObject({ method: 'alipay', amount_cny: '12.37' });
  expect(bodies[1]).toMatchObject({ method: 'wxpay', amount_cny: '12.37' });
  expect(bodies[1]?.idempotency_key).not.toBe(bodies[0]?.idempotency_key);
  expect(bodies[2]).toEqual(bodies[0]);
});

it('does not enforce a retail maximum when the server omits it', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '0.01', cny_per_u: '7', methods: ['alipay'] }).mockResolvedValueOnce([]);
  render(<CNYTopUp method="alipay" onBalanceChange={onBalanceChange} />);
  fireEvent.change(await screen.findByLabelText('充值金额'), { target: { value: '600.00' } });
  expect(screen.getByRole('button', { name: '创建充值订单' })).toBeEnabled();
});
