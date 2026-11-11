import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { apiClient } from '../../api/client';
import { CNYTopUp } from './CNYTopUp';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it('does not offer CNY payment when new orders are closed', async () => {
  const get = vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: false }).mockResolvedValueOnce([]);
  await act(async () => { render(<CNYTopUp onBalanceChange={() => undefined} />); });
  await waitFor(() => expect(get).toHaveBeenCalledWith('/user/topup/cny/orders'));
  expect(screen.queryByLabelText('人民币金额')).not.toBeInTheDocument();
});

it('lets a customer enter an arbitrary cent amount and shows the locked order before leaving the site', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', max_cny: '50.00', cny_per_u: '6.67' }).mockResolvedValueOnce([]);
  const post = vi.spyOn(apiClient, 'post').mockResolvedValue({
    order_no: 'CNY-test-order', status: 'pending', method: 'wxpay', amount_cny: '12.37', credit_u: '1.854572', cny_per_u: '6.67',
    checkout_url: 'https://checkout.example.test/order', created_at: Date.now() / 1000,
  });
  render(<CNYTopUp onBalanceChange={() => undefined} />);
  fireEvent.change(await screen.findByLabelText('人民币金额'), { target: { value: '12.37' } });
  fireEvent.click(screen.getByLabelText('微信支付'));
  fireEvent.click(screen.getByRole('button', { name: '创建人民币订单' }));
  expect(await screen.findByText('¥12.37')).toBeVisible();
  expect(screen.getByText('1.854572 U')).toBeVisible();
  expect(screen.getByRole('link', { name: '前往付款' })).toHaveAttribute('href', 'https://checkout.example.test/order');
  expect(post).toHaveBeenCalledWith('/user/topup/cny/orders', { amount_cny: '12.37', method: 'wxpay', idempotency_key: expect.any(String) });
});

it('renders a QR-only checkout as a local QR image without a navigation link', async () => {
  vi.spyOn(apiClient, 'get').mockResolvedValueOnce({ enabled: true, min_cny: '1.00', max_cny: '1.00', cny_per_u: '6.67' })
    .mockResolvedValue([{
      order_no: 'CNY-test-qr', status: 'pending', method: 'wxpay', amount_cny: '1.00', credit_u: '0.149925',
      checkout_url: '', checkout_qr: 'weixin://wxpay/bizpayurl/up?pr=fixture', created_at: Date.now() / 1000,
    }]);
  render(<CNYTopUp onBalanceChange={() => undefined} />);
  const qr = await screen.findByRole('img', { name: '付款二维码' });
  expect(qr.tagName).toBe('svg');
  expect(qr.querySelector('image')).toBeNull();
  expect(screen.queryByRole('link', { name: '前往付款' })).not.toBeInTheDocument();
});
