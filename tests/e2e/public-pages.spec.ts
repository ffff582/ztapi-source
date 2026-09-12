import { expect, test, type Locator, type Page } from '@playwright/test';

const browserErrors = new WeakMap<Page, string[]>();

async function expectInsideViewport(page: Page, locator: Locator) {
  const box = await locator.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
}

async function expectNoHorizontalOverflow(page: Page) {
  expect(await page.evaluate(() =>
    document.documentElement.scrollWidth <= window.innerWidth,
  )).toBe(true);
}

async function expectBalancedAuthHeadline(page: Page) {
  const headline = page.locator('.auth-context__message h2');
  const lines = headline.locator('.auth-context__headline-line');
  await expect(lines).toHaveText(['一个接口，', '连接模型与业务。']);

  const lineMetrics = await lines.evaluateAll((elements) => elements.map((element) => {
    const box = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return {
      bottom: box.bottom,
      clientWidth: element.clientWidth,
      display: style.display,
      scrollWidth: element.scrollWidth,
      top: box.top,
      whiteSpace: style.whiteSpace,
    };
  }));
  expect(lineMetrics).toHaveLength(2);
  expect(lineMetrics.every(({ clientWidth, display, scrollWidth, whiteSpace }) =>
    display === 'block'
    && whiteSpace === 'nowrap'
    && scrollWidth <= clientWidth)).toBe(true);
  expect(lineMetrics[1].top).toBeGreaterThanOrEqual(lineMetrics[0].bottom - 1);
}

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  browserErrors.set(page, errors);
  page.on('console', (message) => {
    const isExpectedRefreshRejection =
      message.location().url.endsWith('/api/auth/refresh')
      && message.text().includes('401');
    if (
      message.type() === 'error'
      && !isExpectedRefreshRejection
    ) {
      errors.push(message.text());
    }
  });
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/api/status', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      success: true,
      data: { quota_per_unit: 250_000 },
    }),
  }));
  await page.route('**/api/pricing', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      success: true,
      data: [
        {
          model_name: 'gpt-4.1-mini',
          description: '快速通用模型',
          quota_type: 0,
          model_ratio: 1,
          model_price: 0,
          owner_by: 'OpenAI',
          completion_ratio: 4,
          enable_groups: ['default'],
          billing_mode: 'ratio',
          billing_expr: '',
        },
        {
          model_name: 'claude-sonnet-4',
          description: '复杂推理与代码模型',
          quota_type: 0,
          model_ratio: 2,
          model_price: 0,
          owner_by: 'Anthropic',
          completion_ratio: 5,
          enable_groups: ['default'],
          billing_mode: 'ratio',
          billing_expr: '',
        },
        {
          model_name: 'gemini-2.5-flash',
          description: '高效多模态模型',
          quota_type: 0,
          model_ratio: 0.5,
          model_price: 0,
          owner_by: 'Google',
          completion_ratio: 2,
          enable_groups: ['default'],
          billing_mode: 'ratio',
          billing_expr: '',
        },
      ],
      group_ratio: { default: 1 },
      usable_group: { default: 'Default' },
      pricing_version: 'browser-acceptance-v1',
    }),
  }));
  await page.route('**/api/auth/refresh', (route) => route.fulfill({
    status: 401,
    contentType: 'application/json',
    body: JSON.stringify({ success: false, message: 'unauthorized' }),
  }));
});

test.afterEach(async ({ page }) => {
  expect(browserErrors.get(page) ?? []).toEqual([]);
});

test('homepage is complete and has no horizontal overflow', async ({ page }, testInfo) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'ZTAPI' })).toBeVisible();
  await expect(page.getByText('一个 Key，连接全球主流 AI 模型')).toBeVisible();

  const hero = page.locator('.gateway-hero');
  const gatewayVisual = page.getByTestId('gateway-motion-visual');
  const capabilities = page.locator('#capabilities');
  const modelProof = page.locator('.model-proof');
  await expect(hero).toBeVisible();
  await expect(gatewayVisual).toBeVisible();
  await expect(gatewayVisual.locator('[data-route-provider]')).toHaveCount(3);
  expect(await gatewayVisual.locator('.gateway-motion__packet').count()).toBeGreaterThanOrEqual(4);
  await expect(gatewayVisual).toContainText('ZTAPI GATEWAY');
  await expect(hero.locator('img')).toHaveCount(0);
  await expect(hero.locator('canvas')).toHaveCount(0);
  await expect(capabilities).toBeVisible();
  await expect(modelProof.getByText('3 个实时公开模型')).toBeVisible();
  await expect(modelProof.getByText('gpt-4.1-mini')).toBeVisible();
  await expect(modelProof.getByText('claude-sonnet-4')).toBeVisible();
  await expect(modelProof.getByText('gemini-2.5-flash')).toBeVisible();
  await expectNoHorizontalOverflow(page);

  const viewport = page.viewportSize();
  const capabilityBox = await capabilities.boundingBox();
  expect(viewport).not.toBeNull();
  expect(capabilityBox).not.toBeNull();
  expect(capabilityBox!.y).toBeLessThan(viewport!.height);
  if (testInfo.project.name.startsWith('mobile-')) {
    expect(capabilityBox!.y).toBeLessThanOrEqual(viewport!.height - 32);
  }

  if (testInfo.project.name === 'desktop' || testInfo.project.name === 'laptop') {
    const pageScreens = await page.evaluate(() =>
      document.documentElement.scrollHeight / window.innerHeight,
    );
    expect(pageScreens).toBeGreaterThanOrEqual(3);
    expect(pageScreens).toBeLessThanOrEqual(5);
  }

  if (testInfo.project.name.startsWith('mobile-')) {
    const [heroBox, heroTitleBox, actionsBox, endpointBox] = await Promise.all([
      hero.boundingBox(),
      page.getByText('一个 Key，连接全球主流 AI 模型').boundingBox(),
      page.locator('.gateway-hero__actions').boundingBox(),
      page.locator('.gateway-hero__endpoint').boundingBox(),
    ]);
    expect(heroBox).not.toBeNull();
    expect(heroTitleBox).not.toBeNull();
    expect(actionsBox).not.toBeNull();
    expect(endpointBox).not.toBeNull();
    expect(heroTitleBox!.y + heroTitleBox!.height).toBeLessThanOrEqual(actionsBox!.y);
    expect(actionsBox!.y + actionsBox!.height).toBeLessThanOrEqual(endpointBox!.y);
    await expectInsideViewport(page, page.getByRole('link', { name: '开始使用' }));
  }

  const attribution = page.getByRole('link', {
    name: 'Frontend design and development by New API contributors.',
  });
  await expect(attribution).toBeVisible();
  await expect(attribution).toHaveAttribute(
    'href',
    'https://github.com/QuantumNous/new-api',
  );
  await expect(attribution).toHaveAttribute('target', '_blank');
  await expect(attribution).toHaveAttribute('rel', /noreferrer|noopener/);

  await page.screenshot({
    path: `test-results/visual/home-viewport-${testInfo.project.name}.png`,
    fullPage: false,
  });
  await page.screenshot({
    path: `test-results/visual/home-${testInfo.project.name}.png`,
    fullPage: true,
  });
});

for (const hashCase of [
  {
    label: 'same-route capabilities',
    startPath: '/',
    href: '/#capabilities',
    hash: '#capabilities',
  },
  {
    label: 'cross-route quickstart',
    startPath: '/models',
    href: '/#quickstart',
    hash: '#quickstart',
  },
] as const) {
  test(`public navigation scrolls to ${hashCase.label}`, async ({ page }, testInfo) => {
    await page.goto(hashCase.startPath);

    if (testInfo.project.name.startsWith('mobile-')) {
      await page.locator('.public-header__menu').click();
      await expect(page.locator('#public-navigation')).toHaveAttribute('data-open', 'true');
    }

    await page
      .locator(`#public-navigation .public-header__link[href="${hashCase.href}"]`)
      .click();

    await expect(page).toHaveURL(new RegExp(`${hashCase.hash}$`));
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);

    const target = page.locator(hashCase.hash);
    await expect(target).toBeVisible();
    await expect.poll(async () => {
      const targetTop = await target.evaluate((element) => (
        element.getBoundingClientRect().top
      ));
      return targetTop >= 60 && targetTop <= 220;
    }).toBe(true);
  });
}

test('homepage model tabs and copy control are keyboard operable', async ({ page }) => {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/');

  const tabs = page.getByRole('tab', { name: /OpenAI|Claude|Gemini/ });
  await expect(tabs).toHaveCount(3);
  await tabs.first().focus();
  await page.keyboard.press('End');
  await expect(tabs.last()).toBeFocused();
  await expect(tabs.last()).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('Home');
  await expect(tabs.first()).toBeFocused();
  await expect(tabs.first()).toHaveAttribute('aria-selected', 'true');

  await page.keyboard.press('End');
  const selectedCode = await page.locator('#integration-code-panel code').textContent();
  expect(selectedCode).not.toBeNull();

  const copy = page.locator('.integration-workbench__toolbar button');
  await copy.focus();
  await expect(copy).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(copy).toHaveText('已复制');
  await expect(page.locator('.integration-workbench__copy-announcement'))
    .toHaveText('已复制');
  const usesWindowsClipboardLineEndings = await page.evaluate(() =>
    navigator.platform.startsWith('Win'),
  );
  const expectedClipboard = usesWindowsClipboardLineEndings
    ? selectedCode!.replace(/\r?\n/g, '\r\n')
    : selectedCode;
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(expectedClipboard);
});

test('mobile navigation is operable and contained', async ({ page }, testInfo) => {
  test.skip(!testInfo.project.name.startsWith('mobile-'));
  await page.goto('/');

  const menu = page.getByRole('button', { name: '打开导航菜单' });
  await expect(menu).toBeVisible();
  await menu.click();
  const navigation = page.locator('#public-navigation');
  await expect(navigation).toHaveAttribute('data-open', 'true');

  const links = navigation.getByRole('link');
  await expect(links).toHaveCount(5);
  await page.keyboard.press('Tab');
  await expect(links.first()).toBeFocused();
  for (const link of await links.all()) {
    await expect(link).toBeVisible();
    await expectInsideViewport(page, link);
  }

  await page.keyboard.press('Escape');
  await expect(navigation).toHaveAttribute('data-open', 'false');
  await expect(menu).toBeFocused();
});

test('homepage remains complete when WebGL is unavailable', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'WebGLRenderingContext', {
      configurable: true,
      value: undefined,
    });
    Object.defineProperty(window, 'WebGL2RenderingContext', {
      configurable: true,
      value: undefined,
    });
  });
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'ZTAPI' })).toBeVisible();
  await expect(page.getByTestId('gateway-motion-visual')).toBeVisible();
  await expect(page.locator('.gateway-hero img')).toHaveCount(0);
  await expect(page.locator('.gateway-hero canvas')).toHaveCount(0);
  await expect(page.locator('.gateway-hero .button-link--primary')).toBeVisible();
});

for (const path of ['/login', '/register'] as const) {
  test(`${path} remains task focused and usable`, async ({ page }, testInfo) => {
    await page.goto(path);
    const username = page.getByLabel('账号', { exact: true });
    const password = page.getByLabel('密码', { exact: true });
    await expect(username).toBeVisible();
    await expect(password).toBeVisible();
    await expect(username).toBeFocused();
    await expect(page.getByRole('link', { name: '返回首页' })).toBeVisible();
    await expectNoHorizontalOverflow(page);
    const passwordToggle = page.locator('form .auth-password button');
    if (testInfo.project.name === 'mobile-320') {
      await username.fill('alice');
      await page.keyboard.press('Tab');
      await expect(password).toBeFocused();
      await password.fill('correct-horse');
      await page.keyboard.press('Tab');
      await expect(passwordToggle).toBeFocused();
      await page.keyboard.press('Enter');
      await expect(passwordToggle).toBeFocused();
    } else {
      await password.fill('correct-horse');
      await passwordToggle.click();
    }
    await expect(password).toHaveAttribute('type', 'text');
    await expect(password).toHaveValue('correct-horse');

    if (testInfo.project.name === 'desktop' || testInfo.project.name === 'laptop') {
      await expectBalancedAuthHeadline(page);
    }

    if (testInfo.project.name.startsWith('mobile-')) {
      await expectInsideViewport(page, username);
      await expectInsideViewport(page, password);
      const submitName = path === '/login' ? '登录' : '创建账号';
      await expectInsideViewport(page, page.getByRole('button', {
        name: submitName,
      }));
    }

    await page.screenshot({
      path: `test-results/visual/${path.slice(1)}-${testInfo.project.name}.png`,
      fullPage: true,
    });
  });
}

test('reduced motion keeps the homepage content available', async ({ page }, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'ZTAPI' })).toBeVisible();
  await expect(page.getByTestId('gateway-motion-visual')).toBeVisible();
  await expect(page.locator('.gateway-hero img')).toHaveCount(0);
  await expect(page.locator('.gateway-hero canvas')).toHaveCount(0);
  await expect(page.locator('.gateway-hero .button-link--primary')).toBeVisible();
  await expect(page.locator('.gateway-motion__packet').first()).toHaveCSS('animation-name', 'none');
  await expect(page.locator('.gateway-motion__pulse').first()).toHaveCSS('animation-name', 'none');
  if (!testInfo.project.name.startsWith('mobile-')) {
    await page.screenshot({
      path: `test-results/visual/home-reduced-motion-${testInfo.project.name}.png`,
      fullPage: false,
    });
  }
});
