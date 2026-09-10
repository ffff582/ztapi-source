import { expect, test, type Page } from '@playwright/test';

const receivingAddress = 'TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2';

function authSession() {
  return {
    success: true,
    data: {
      access_token: 'e2e-wallet-session',
      expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' },
    },
  };
}

function order(status: 'pending' | 'confirming' | 'settled') {
  return {
    success: true,
    data: {
      id: 91,
      trade_no: 'USDT-E2E-91',
      credit_units: 10,
      pay_amount: '10.37',
      receiving_address: receivingAddress,
      network: 'tron-mainnet',
      asset: 'USDT',
      expires_at: Math.floor(Date.now() / 1_000) + 600,
      status,
      ...(status === 'settled'
        ? {
            tx_id: 'e2e-confirmed-transaction',
            settled_at: Math.floor(Date.now() / 1_000),
          }
        : {}),
    },
  };
}

async function expectNoHorizontalOverflow(page: Page) {
  expect(await page.evaluate(() =>
    document.documentElement.scrollWidth <= window.innerWidth,
  )).toBe(true);
}

test('authenticated user creates and settles a USDT TRC-20 top-up', async ({ page }, testInfo) => {
  const browserErrors: string[] = [];
  let orderReads = 0;
  page.on('console', (message) => {
    if (message.type() === 'error') {
      browserErrors.push(message.text());
    }
  });
  page.on('pageerror', (error) => browserErrors.push(error.message));

  await page.route('**/api/auth/refresh', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(authSession()),
  }));
  await page.route('**/api/status', (route) => route.fulfill({
    json: { success: true, data: { quota_per_unit: 500_000 } },
  }));
  await page.route('**/api/user/self', (route) => route.fulfill({
    json: { success: true, data: { quota: orderReads >= 2 ? 5_000_000 : 0 } },
  }));
  await page.route('**/api/user/self/pending-settlements?*', (route) => route.fulfill({
    json: { success: true, data: { items: [], next_after_id: 0 } },
  }));
  await page.route('**/api/user/topup/self?*', (route) => route.fulfill({
    json: { success: true, data: {
      page: 1, page_size: 5, total: orderReads >= 2 ? 1 : 0,
      items: orderReads >= 2 ? [{
        id: 91, amount: 10, money: 10.37, trade_no: 'USDT-E2E-91',
        payment_provider: 'usdt_trc20', payment_method: 'usdt_trc20',
        create_time: 1788585565, complete_time: 1788585730, status: 'success',
      }] : [],
    } },
  }));
  await page.route('**/api/user/topup/info', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      success: true,
      data: {
        enable_usdt_trc20_topup: true,
        usdt_trc20_network: 'tron-mainnet',
        usdt_trc20_asset: 'USDT',
        usdt_trc20_min_topup: 10,
        usdt_trc20_order_ttl_seconds: 600,
      },
    }),
  }));
  await page.route('**/api/user/topup/usdt-trc20/orders**', async (route) => {
    const request = route.request();
    if (request.method() === 'POST' && request.url().endsWith('/orders')) {
      expect(request.postDataJSON()).toEqual({ amount: 10 });
      expect(request.headers().authorization).toBe('Bearer e2e-wallet-session');
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify(order('pending')),
      });
      return;
    }

    orderReads += 1;
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(order(orderReads === 1 ? 'confirming' : 'settled')),
    });
  });

  await page.goto('/console/wallet');
  await expect(page.getByRole('region', { name: '账户余额' }).getByText('$0.00')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: '余额充值' })).toBeVisible();
  const activeWalletNavigation = page.getByRole('link', { name: '余额充值' });
  await expect(activeWalletNavigation).toBeVisible();
  const navigationBox = await activeWalletNavigation.boundingBox();
  const viewport = page.viewportSize();
  expect(navigationBox).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(navigationBox!.x).toBeGreaterThanOrEqual(0);
  expect(navigationBox!.x + navigationBox!.width).toBeLessThanOrEqual(viewport!.width);
  await page.getByLabel('充值数量').fill('10');
  await page.getByRole('button', { name: '创建支付订单' }).click();

  await expect(page.getByText('10.37 USDT')).toBeVisible();
  await expect(page.getByText(receivingAddress)).toBeVisible();
  await expect(page.getByText('TRON Mainnet · TRC-20')).toBeVisible();
  const qr = page.getByTestId('usdt-address-qr');
  await expect(qr).toHaveAttribute('data-qr-value', receivingAddress);
  await expect(qr.locator('svg')).toBeVisible();
  await expectNoHorizontalOverflow(page);

  await expect(page.getByText('正在确认到账')).toBeVisible({ timeout: 5_000 });
  await expect(page.getByText('10.37 USDT')).toBeHidden();
  await expect(page.getByText(receivingAddress)).toBeHidden();
  await expect(qr).toBeHidden();
  await expectNoHorizontalOverflow(page);

  await expect(page.getByText('充值已到账')).toBeVisible({ timeout: 5_000 });
  await expect(page.getByRole('region', { name: '账户余额' }).getByText('$10.00')).toBeVisible();
  await expect(page.getByRole('region', { name: '充值记录' }).getByText('已到账')).toBeVisible();
  const receiptStatusBox = await page.getByRole('region', { name: '充值记录' }).getByText('已到账').boundingBox();
  expect(receiptStatusBox).not.toBeNull();
  expect(receiptStatusBox!.x).toBeGreaterThanOrEqual(0);
  expect(receiptStatusBox!.x + receiptStatusBox!.width).toBeLessThanOrEqual(viewport!.width);
  expect(orderReads).toBe(2);
  await expectNoHorizontalOverflow(page);
  expect(browserErrors).toEqual([]);

  await page.screenshot({
    path: `test-results/visual/usdt-topup-${testInfo.project.name}.png`,
    fullPage: true,
  });

  await page.reload();
  await expect(page.getByRole('region', { name: '账户余额' }).getByText('$10.00')).toBeVisible();
  await expect(page.getByRole('region', { name: '充值记录' }).getByText('USDT-E2E-91')).toBeVisible();
  await expect(page.getByRole('region', { name: '充值记录' }).getByText('已到账')).toBeVisible();
  expect(orderReads).toBe(2);
  await expectNoHorizontalOverflow(page);
  expect(browserErrors).toEqual([]);
});
