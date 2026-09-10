import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest';
import { StrictMode } from 'react';
import { clearAuthSession } from '../../api/client';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';

const plaintextKey = 'sk-zt-created-once';
const NativeRequest = globalThis.Request;

const pricing = [
  {
    model_name: 'gemini-2.5-pro',
    description: 'Google reasoning model',
    quota_type: 0,
    model_ratio: 0.625,
    model_price: 0,
    owner_by: 'Google',
    completion_ratio: 4,
    enable_groups: ['default'],
    supported_endpoint_types: ['openai'],
  },
  {
    model_name: 'gpt-4o',
    description: 'OpenAI general model',
    quota_type: 0,
    model_ratio: 1.25,
    model_price: 0,
    owner_by: 'OpenAI',
    completion_ratio: 4,
    enable_groups: ['default'],
    supported_endpoint_types: ['openai'],
  },
  {
    model_name: 'claude-3-5-sonnet',
    description: 'Anthropic general model',
    quota_type: 0,
    model_ratio: 1.5,
    model_price: 0,
    owner_by: 'Anthropic',
    completion_ratio: 5,
    enable_groups: ['default'],
    supported_endpoint_types: ['openai'],
  },
];

const enabledToken = {
  id: 7,
  status: 1,
  name: 'enabled-key',
  created_time: 1_900_000_000,
  accessed_time: 1_900_000_100,
  expired_time: -1,
  remain_quota: 5_000_000,
  unlimited_quota: false,
  model_limits_enabled: true,
  model_limits: 'gpt-4o',
  allow_ips: '192.0.2.10/32',
  key_prefix: 'sk-zt-abcd...',
};

const disabledToken = {
  ...enabledToken,
  id: 8,
  status: 2,
  name: 'disabled-key',
  key_prefix: 'sk-zt-efgh...',
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function authResponse() {
  return jsonResponse({
    success: true,
    data: {
      access_token: 'memory-only-session',
      expires_in: 900,
      user: {
        id: 41,
        username: 'alice',
        role: 1,
        group: 'default',
      },
    },
  });
}

function pricingResponse() {
  return jsonResponse({
    success: true,
    data: pricing,
    group_ratio: { default: 1 },
    usable_group: { default: '默认分组' },
    pricing_version: 'pricing-test-v1',
  });
}

function statusResponse(quotaPerUnit = 500_000) {
  return jsonResponse({
    success: true,
    data: { quota_per_unit: quotaPerUnit },
  });
}

function tokenPageResponse(
  items: typeof enabledToken[],
  {
    page = 1,
    pageSize = 100,
    total = items.length,
  }: { page?: number; pageSize?: number; total?: number } = {},
) {
  return jsonResponse({
    success: true,
    data: {
      page,
      page_size: pageSize,
      total,
      items,
    },
  });
}

function requestUrl(input: RequestInfo | URL) {
  if (typeof input === 'string') {
    return input;
  }

  return input instanceof URL ? input.href : input.url;
}

function requestMethod(init?: RequestInit) {
  return init?.method ?? 'GET';
}

function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function oneTimeKeyFetchMock() {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = requestUrl(input);
    const method = requestMethod(init);

    if (url.endsWith('/api/auth/refresh')) {
      return authResponse();
    }
    if (url.endsWith('/api/status')) {
      return statusResponse();
    }
    if (url.endsWith('/api/pricing')) {
      return pricingResponse();
    }
    if (url.includes('/api/token/') && method === 'GET') {
      return tokenPageResponse([]);
    }
    if (url.endsWith('/api/token/') && method === 'POST') {
      return jsonResponse({
        success: true,
        data: {
          id: 7,
          key: plaintextKey,
          key_prefix: 'sk-zt-lifecycle',
        },
      });
    }
    if (url.endsWith('/api/auth/logout') && method === 'POST') {
      return jsonResponse({ success: true });
    }
    return jsonResponse({ success: false, message: 'unexpected' }, 500);
  });
}

function renderKeys() {
  return render(
    <AppProviders>
      <RouterProvider router={createZTAPIRouter(['/console/keys'])} />
    </AppProviders>,
  );
}

async function createKeyWithDefaults(name = 'lifecycle-key') {
  fireEvent.change(await screen.findByLabelText('密钥名称'), {
    target: { value: name },
  });
  fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
  return screen.findByText(plaintextKey);
}

function storageContents(storage: Storage) {
  return Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index);
    return key === null ? '' : `${key}:${storage.getItem(key) ?? ''}`;
  }).join('|');
}

beforeAll(() => {
  globalThis.Request = class TestRequest extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? init : { ...init, signal: undefined });
    }
  };
});

afterAll(() => {
  globalThis.Request = NativeRequest;
});

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  clearAuthSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ZTAPI one-time API keys', () => {
  it('keeps key creation unavailable until model data has loaded', async () => {
    let resolvePricing!: (response: Response) => void;
    const pendingPricing = new Promise<Response>((resolve) => {
      resolvePricing = resolve;
    });
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.includes('/api/token/') && method === 'GET') {
          return tokenPageResponse([]);
        }
        if (url.endsWith('/api/pricing')) {
          return pendingPricing;
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await screen.findByRole('heading', { name: 'API 密钥' });
    expect(screen.getByText('正在加载创建选项...')).toBeVisible();
    expect(screen.queryByLabelText('密钥名称')).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: '创建 API Key' }),
    ).not.toBeInTheDocument();

    await act(async () => {
      resolvePricing(pricingResponse());
      await pendingPricing;
    });

    expect(await screen.findByLabelText('密钥名称')).toBeEnabled();
    expect(screen.getByLabelText('限制可用模型')).toBeEnabled();
    expect(
      screen.getByRole('button', { name: '创建 API Key' }),
    ).toBeEnabled();
  });

  it.each(['pricing', 'status'] as const)(
    'keeps token revocation available when %s creation metadata fails',
    async (failedMetadata) => {
      let revoked = false;
      const fetchMock = vi.fn(
        async (input: RequestInfo | URL, init?: RequestInit) => {
          const url = requestUrl(input);
          const method = requestMethod(init);

          if (url.endsWith('/api/auth/refresh')) {
            return authResponse();
          }
          if (url.endsWith('/api/status')) {
            return failedMetadata === 'status'
              ? jsonResponse({ success: false, message: 'unavailable' }, 503)
              : statusResponse();
          }
          if (url.includes('/api/token/') && method === 'GET') {
            return tokenPageResponse(revoked ? [] : [enabledToken]);
          }
          if (url.endsWith('/api/pricing')) {
            return failedMetadata === 'pricing'
              ? jsonResponse({ success: false, message: 'unavailable' }, 503)
              : pricingResponse();
          }
          if (url.endsWith('/api/token/7') && method === 'DELETE') {
            revoked = true;
            return jsonResponse({ success: true });
          }
          return jsonResponse({ success: false, message: 'unexpected' }, 500);
        },
      );
      vi.stubGlobal('fetch', fetchMock);
      renderKeys();

      expect(
        await screen.findByText('创建选项加载失败，请刷新后重试。'),
      ).toBeVisible();
      expect(screen.queryByLabelText('密钥名称')).not.toBeInTheDocument();

      const tokenRow = (await screen.findByText('enabled-key')).closest('tr');
      expect(tokenRow).not.toBeNull();
      fireEvent.click(
        within(tokenRow as HTMLElement).getByRole('button', { name: '撤销' }),
      );
      fireEvent.click(
        within(tokenRow as HTMLElement).getByRole('button', {
          name: '确认撤销',
        }),
      );
      await waitFor(() => {
        expect(screen.queryByText('enabled-key')).not.toBeInTheDocument();
      });
    },
  );

  it('validates every restriction before sending a create request', async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.includes('/api/token/') && method === 'GET') {
          return tokenPageResponse([]);
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    const submit = await screen.findByRole('button', { name: '创建 API Key' });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
    expect(screen.getByText('请输入密钥名称')).toBeVisible();

    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: '密钥名称长度需要按照字节进行严格验证' },
    });
    fireEvent.click(submit);
    expect(
      screen.getByText('密钥名称不能超过 50 个 UTF-8 字节'),
    ).toBeVisible();

    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'test-key' },
    });
    fireEvent.change(screen.getByLabelText('有效期'), {
      target: { value: 'custom' },
    });
    fireEvent.click(submit);
    expect(screen.getByText('请选择未来的过期时间')).toBeVisible();

    fireEvent.change(screen.getByLabelText('过期时间'), {
      target: { value: '2030-01-02T03:04' },
    });
    fireEvent.click(screen.getByLabelText('限制可用模型'));
    fireEvent.click(submit);
    expect(screen.getByText('至少选择一个可用模型')).toBeVisible();

    fireEvent.click(screen.getByLabelText('gpt-4o'));
    fireEvent.click(screen.getByLabelText('不限制密钥额度'));
    fireEvent.click(submit);
    expect(screen.getByText('请输入大于 0 的额度上限')).toBeVisible();

    fireEvent.change(screen.getByLabelText('额度上限'), {
      target: { value: '10' },
    });
    fireEvent.change(screen.getByLabelText('IP 白名单'), {
      target: { value: 'not-an-ip' },
    });
    fireEvent.click(submit);
    expect(screen.getByText('IP 白名单包含无效的 IP 或 CIDR')).toBeVisible();

    expect(
      fetchMock.mock.calls.filter(
        ([input, init]) =>
          requestUrl(input as RequestInfo | URL).includes('/api/token/') &&
          requestMethod(init as RequestInit | undefined) === 'POST',
      ),
    ).toHaveLength(0);
  });

  it.each([
    { quotaPerUnit: 3, spendingLimit: '0.1', caseName: 'fractional' },
    {
      quotaPerUnit: Number.MAX_SAFE_INTEGER,
      spendingLimit: '2',
      caseName: 'unsafe',
    },
  ])(
    'rejects a $caseName converted internal quota',
    async ({ quotaPerUnit, spendingLimit }) => {
      const fetchMock = vi.fn(
        async (input: RequestInfo | URL, init?: RequestInit) => {
          const url = requestUrl(input);
          const method = requestMethod(init);

          if (url.endsWith('/api/auth/refresh')) {
            return authResponse();
          }
          if (url.endsWith('/api/status')) {
            return statusResponse(quotaPerUnit);
          }
          if (url.includes('/api/token/') && method === 'GET') {
            return tokenPageResponse([]);
          }
          if (url.endsWith('/api/pricing')) {
            return pricingResponse();
          }
          if (url.endsWith('/api/token/') && method === 'POST') {
            return jsonResponse({
              success: true,
              data: {
                id: 9,
                key: plaintextKey,
                key_prefix: 'sk-zt-subunit',
              },
            });
          }
          return jsonResponse({ success: false, message: 'unexpected' }, 500);
        },
      );
      vi.stubGlobal('fetch', fetchMock);
      renderKeys();

      fireEvent.change(await screen.findByLabelText('密钥名称'), {
        target: { value: 'quota-boundary' },
      });
      fireEvent.click(screen.getByLabelText('不限制密钥额度'));
      fireEvent.change(screen.getByLabelText('额度上限'), {
        target: { value: spendingLimit },
      });
      fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));

      expect(
        screen.getByText('额度必须转换为至少 1 个安全整数内部单位'),
      ).toBeVisible();
      expect(
        fetchMock.mock.calls.filter(
          ([input, init]) =>
            requestUrl(input as RequestInfo | URL).endsWith('/api/token/') &&
            requestMethod(init as RequestInit | undefined) === 'POST',
        ),
      ).toHaveLength(0);
    },
  );

  it('normalizes creation, blocks accidental dismissal, then forgets plaintext', async () => {
    let tokenListReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse(320_000);
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.includes('/api/token/') && method === 'GET') {
          tokenListReads += 1;
          return tokenPageResponse(tokenListReads === 1 ? [] : [enabledToken]);
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return jsonResponse({
            success: true,
            data: {
              id: 7,
              key: plaintextKey,
              key_prefix: 'sk-zt-abcd',
            },
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    const consoleSpies = [
      vi.spyOn(console, 'log').mockImplementation(() => undefined),
      vi.spyOn(console, 'warn').mockImplementation(() => undefined),
      vi.spyOn(console, 'error').mockImplementation(() => undefined),
    ];
    const storageSpy = vi.spyOn(Storage.prototype, 'setItem');
    const analytics = { track: vi.fn() };
    vi.stubGlobal('analytics', analytics);
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await screen.findByRole('heading', { name: 'API 密钥' });
    const restrictModels = await screen.findByLabelText('限制可用模型');
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: '  production key  ' },
    });
    fireEvent.click(restrictModels);
    fireEvent.click(await screen.findByLabelText('gpt-4o'));
    fireEvent.click(screen.getByLabelText('claude-3-5-sonnet'));
    fireEvent.click(screen.getByLabelText('不限制密钥额度'));
    fireEvent.change(screen.getByLabelText('额度上限'), {
      target: { value: '10.25' },
    });
    fireEvent.change(screen.getByLabelText('IP 白名单'), {
      target: {
        value: ' 2001:db8::1 \n192.0.2.10, 192.0.2.10/32',
      },
    });
    let openWhenSecretEnteredDOM: boolean | undefined;
    const secretObserver = new MutationObserver(() => {
      const secret = document.querySelector('.key-dialog__secret');
      if (
        secret?.textContent === plaintextKey &&
        openWhenSecretEnteredDOM === undefined
      ) {
        openWhenSecretEnteredDOM =
          secret.closest<HTMLDialogElement>('dialog')?.open ?? false;
        secretObserver.disconnect();
      }
    });
    secretObserver.observe(document.body, { childList: true, subtree: true });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));

    await waitFor(() => expect(openWhenSecretEnteredDOM).toBeDefined());
    expect(openWhenSecretEnteredDOM).toBe(true);
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    const createCall = fetchMock.mock.calls.find(
      ([input, init]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/api/token/') &&
        requestMethod(init as RequestInit | undefined) === 'POST',
    );
    expect(JSON.parse(String(createCall?.[1]?.body))).toEqual({
      name: 'production key',
      status: 1,
      expired_time: -1,
      unlimited_quota: false,
      remain_quota: 3_280_000,
      model_limits_enabled: true,
      model_limits: 'claude-3-5-sonnet,gpt-4o',
      allow_ips: '192.0.2.10/32\n2001:db8::1/128',
    });

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(screen.getByText(plaintextKey)).toBeVisible();

    const dialog = screen.getByRole('dialog', { name: '保存新的 API Key' });
    const backdrop = dialog.parentElement;
    expect(backdrop).not.toBeNull();
    fireEvent.mouseDown(backdrop as HTMLElement);
    fireEvent.click(backdrop as HTMLElement);
    expect(screen.getByText(plaintextKey)).toBeVisible();
    expect(
      within(dialog).queryByRole('button', { name: /关闭|取消|Close/i }),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('link', { name: '概览' }));
    expect(screen.getByText(plaintextKey)).toBeVisible();

    await waitFor(() => expect(tokenListReads).toBe(2));
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    expect(screen.queryByText(plaintextKey)).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: /查看完整 Key/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByText('sk-zt-abcd...')).toBeInTheDocument();

    expect(storageContents(localStorage)).not.toContain(plaintextKey);
    expect(storageContents(sessionStorage)).not.toContain(plaintextKey);
    expect(window.location.href).not.toContain(plaintextKey);
    expect(document.body.textContent).not.toContain(plaintextKey);
    expect(JSON.stringify(storageSpy.mock.calls)).not.toContain(plaintextKey);
    expect(JSON.stringify(analytics.track.mock.calls)).not.toContain(plaintextKey);
    for (const spy of consoleSpies) {
      expect(JSON.stringify(spy.mock.calls)).not.toContain(plaintextKey);
    }
  });

  it('traps focus and retains plaintext through logout until acknowledgement', async () => {
    vi.stubGlobal('fetch', oneTimeKeyFetchMock());
    renderKeys();

    expect(await createKeyWithDefaults()).toBeVisible();
    const dialog = screen.getByRole('dialog', { name: '保存新的 API Key' });
    const copyButton = within(dialog).getByRole('button', { name: '复制 Key' });
    const acknowledgeButton = within(dialog).getByRole('button', {
      name: '我已安全保存',
    });
    const logoutButton = screen.getByRole('button', { name: '退出登录' });

    await waitFor(() => expect(acknowledgeButton).toHaveFocus());
    fireEvent.keyDown(dialog, { key: 'Tab' });
    expect(copyButton).toHaveFocus();
    expect(logoutButton).not.toHaveFocus();

    fireEvent.click(logoutButton);
    expect(
      await screen.findByRole('heading', { name: '登录 ZTAPI' }),
    ).toBeInTheDocument();
    expect(screen.getByText(plaintextKey)).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    expect(screen.queryByText(plaintextKey)).not.toBeInTheDocument();
  });

  it('retains plaintext through forced session loss until acknowledgement', async () => {
    vi.stubGlobal('fetch', oneTimeKeyFetchMock());
    renderKeys();

    expect(await createKeyWithDefaults('session-loss-key')).toBeVisible();
    act(() => {
      clearAuthSession();
    });

    expect(
      await screen.findByRole('heading', { name: '登录 ZTAPI' }),
    ).toBeInTheDocument();
    expect(screen.getByText(plaintextKey)).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    expect(screen.queryByText(plaintextKey)).not.toBeInTheDocument();
  });

  it('uses normalized status and revoke API calls without offering reveal', async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.includes('/api/token/') && method === 'GET') {
          return tokenPageResponse([enabledToken, disabledToken]);
        }
        if (url.includes('/api/token/?status_only=true') && method === 'PUT') {
          const body = JSON.parse(String(init?.body)) as { id: number; status: number };
          const source = body.id === enabledToken.id ? enabledToken : disabledToken;
          return jsonResponse({
            success: true,
            data: { ...source, status: body.status },
          });
        }
        if (url.endsWith('/api/token/7') && method === 'DELETE') {
          return jsonResponse({ success: true });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    const enabledRow = (await screen.findByText('enabled-key')).closest('tr');
    const disabledRow = screen.getByText('disabled-key').closest('tr');
    expect(enabledRow).not.toBeNull();
    expect(disabledRow).not.toBeNull();

    fireEvent.click(
      within(enabledRow as HTMLElement).getByRole('button', { name: '禁用' }),
    );
    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([input, init]) =>
          requestUrl(input as RequestInfo | URL).includes(
            '/api/token/?status_only=true',
          ) && requestMethod(init as RequestInit | undefined) === 'PUT',
      );
      expect(JSON.parse(String(call?.[1]?.body))).toEqual({ id: 7, status: 2 });
    });

    fireEvent.click(
      within(disabledRow as HTMLElement).getByRole('button', { name: '启用' }),
    );
    await waitFor(() => {
      const putBodies = fetchMock.mock.calls
        .filter(
          ([input, init]) =>
            requestUrl(input as RequestInfo | URL).includes(
              '/api/token/?status_only=true',
            ) && requestMethod(init as RequestInit | undefined) === 'PUT',
        )
        .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
      expect(putBodies).toContainEqual({ id: 8, status: 1 });
    });

    fireEvent.click(
      within(enabledRow as HTMLElement).getByRole('button', { name: '撤销' }),
    );
    fireEvent.click(
      within(enabledRow as HTMLElement).getByRole('button', {
        name: '确认撤销',
      }),
    );
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(
          ([input, init]) =>
            requestUrl(input as RequestInfo | URL).endsWith('/api/token/7') &&
            requestMethod(init as RequestInit | undefined) === 'DELETE',
        ),
      ).toBe(true);
    });

    expect(
      screen.queryByRole('button', { name: /查看完整 Key/ }),
    ).not.toBeInTheDocument();
  });

  it('navigates key pages and can revoke a key beyond the first page', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          return page === '2'
            ? tokenPageResponse([archivedToken], {
                page: 2,
                pageSize: 100,
                total: 101,
              })
            : tokenPageResponse([enabledToken], {
                page: 1,
                pageSize: 100,
                total: 101,
              });
        }
        if (url.endsWith('/api/token/109') && method === 'DELETE') {
          return jsonResponse({ success: true });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    expect(await screen.findByText('enabled-key')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));

    const archivedRow = (await screen.findByText('archived-key')).closest('tr');
    expect(archivedRow).not.toBeNull();
    expect(screen.getByText('第 2 / 2 页')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();

    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', { name: '撤销' }),
    );
    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', {
        name: '确认撤销',
      }),
    );
    await waitFor(() => {
      expect(screen.queryByText('archived-key')).not.toBeInTheDocument();
    });
  });

  it('does not let a stale create refresh overwrite a newer key page', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const pendingAuth = deferredResponse();
    const staleRefresh = deferredResponse();
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return jsonResponse({
            success: true,
            data: {
              id: 110,
              key: plaintextKey,
              key_prefix: 'sk-zt-new1',
            },
          });
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            return tokenPageResponse([archivedToken], {
              page: 2,
              pageSize: 100,
              total: 101,
            });
          }
          pageOneReads += 1;
          if (pageOneReads === 2) {
            return staleRefresh.promise;
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 101,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'refresh-race-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    await waitFor(() => expect(pageOneReads).toBe(2));
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));

    expect(await screen.findByText('archived-key')).toBeVisible();
    staleRefresh.resolve(
      tokenPageResponse([enabledToken], {
        page: 1,
        pageSize: 100,
        total: 101,
      }),
    );

    await act(async () => {
      await staleRefresh.promise;
    });
    expect(screen.getByText('第 2 / 2 页')).toBeVisible();
    expect(screen.getByText('archived-key')).toBeVisible();
    expect(screen.queryByText('enabled-key')).not.toBeInTheDocument();
  });

  it('finishes the active page load when create completes after navigation starts', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const pendingAuth = deferredResponse();
    const pendingCreate = deferredResponse();
    const pendingPageTwo = deferredResponse();
    let pageTwoReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return pendingCreate.promise;
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            pageTwoReads += 1;
            return pendingPageTwo.promise;
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 101,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'pending-create-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(pageTwoReads).toBe(1));

    pendingCreate.resolve(
      jsonResponse({
        success: true,
        data: {
          id: 110,
          key: plaintextKey,
          key_prefix: 'sk-zt-new2',
        },
      }),
    );
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));

    pendingPageTwo.resolve(
      tokenPageResponse([archivedToken], {
        page: 2,
        pageSize: 100,
        total: 101,
      }),
    );
    await act(async () => {
      await pendingPageTwo.promise;
    });

    expect(screen.queryByText('正在加载密钥...')).not.toBeInTheDocument();
    expect(screen.getByText('第 2 / 2 页')).toBeVisible();
    expect(screen.getByText('archived-key')).toBeVisible();
  });

  it('does not decrement the current page when an older-page revoke finishes', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const pendingAuth = deferredResponse();
    const pendingDelete = deferredResponse();
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/109') && method === 'DELETE') {
          return pendingDelete.promise;
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '0') {
            return tokenPageResponse(
              [{ ...enabledToken, id: 0, name: 'wrong-page' }],
              { page: 0, pageSize: 100, total: 101 },
            );
          }
          return page === '2'
            ? tokenPageResponse([archivedToken], {
                page: 2,
                pageSize: 100,
                total: 101,
              })
            : tokenPageResponse([enabledToken], {
                page: 1,
                pageSize: 100,
                total: 101,
              });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    expect(screen.getByText('enabled-key')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    const archivedRow = (await screen.findByText('archived-key')).closest('tr');
    expect(archivedRow).not.toBeNull();
    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', { name: '撤销' }),
    );
    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', {
        name: '确认撤销',
      }),
    );
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    expect(await screen.findByText('enabled-key')).toBeVisible();

    pendingDelete.resolve(jsonResponse({ success: true }));
    await act(async () => {
      await pendingDelete.promise;
    });

    expect(screen.getByText('第 1 / 2 页')).toBeVisible();
    expect(screen.getByText('enabled-key')).toBeVisible();
    expect(screen.queryByText('wrong-page')).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input]) => {
        const url = requestUrl(input as RequestInfo | URL);
        return new URL(url, 'http://ztapi.test').searchParams.get('p') === '0';
      }),
    ).toBe(false);
  });

  it('finishes the active page load when revoke completes before navigation load', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const pendingAuth = deferredResponse();
    const pendingDelete = deferredResponse();
    const pendingPageOne = deferredResponse();
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/109') && method === 'DELETE') {
          return pendingDelete.promise;
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            return tokenPageResponse([archivedToken], {
              page: 2,
              pageSize: 100,
              total: 101,
            });
          }
          pageOneReads += 1;
          if (pageOneReads === 2) {
            return pendingPageOne.promise;
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 101,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    expect(screen.getByText('enabled-key')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    const archivedRow = (await screen.findByText('archived-key')).closest('tr');
    expect(archivedRow).not.toBeNull();
    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', { name: '撤销' }),
    );
    fireEvent.click(
      within(archivedRow as HTMLElement).getByRole('button', {
        name: '确认撤销',
      }),
    );
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await waitFor(() => expect(pageOneReads).toBe(2));

    pendingDelete.resolve(jsonResponse({ success: true }));
    await act(async () => {
      await pendingDelete.promise;
    });

    pendingPageOne.resolve(
      tokenPageResponse([enabledToken], {
        page: 1,
        pageSize: 100,
        total: 100,
      }),
    );
    await act(async () => {
      await pendingPageOne.promise;
    });

    expect(screen.queryByText('正在加载密钥...')).not.toBeInTheDocument();
    expect(screen.getByText('第 1 / 1 页')).toBeVisible();
    expect(screen.getByText('enabled-key')).toBeVisible();
  });

  it('commits a returning foreground page before a later create revalidation', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const foregroundToken = {
      ...enabledToken,
      name: 'foreground-key',
    };
    const revalidatedToken = {
      ...enabledToken,
      name: 'revalidated-key',
    };
    const pendingAuth = deferredResponse();
    const pendingCreate = deferredResponse();
    const pendingForeground = deferredResponse();
    const pendingRevalidation = deferredResponse();
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return pendingCreate.promise;
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            return tokenPageResponse([archivedToken], {
              page: 2,
              pageSize: 100,
              total: 101,
            });
          }
          pageOneReads += 1;
          if (pageOneReads === 2) {
            return pendingForeground.promise;
          }
          if (pageOneReads === 3) {
            return pendingRevalidation.promise;
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 101,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'returning-create-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('archived-key')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await waitFor(() => expect(pageOneReads).toBe(2));

    pendingCreate.resolve(
      jsonResponse({
        success: true,
        data: {
          id: 110,
          key: plaintextKey,
          key_prefix: 'sk-zt-new3',
        },
      }),
    );
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    await waitFor(() => expect(pageOneReads).toBe(3));

    pendingForeground.resolve(
      tokenPageResponse([foregroundToken], {
        page: 1,
        pageSize: 100,
        total: 101,
      }),
    );
    await act(async () => {
      await pendingForeground.promise;
    });

    expect(screen.queryByText('正在加载密钥...')).not.toBeInTheDocument();
    expect(screen.getByText('foreground-key')).toBeVisible();

    pendingRevalidation.resolve(
      tokenPageResponse([revalidatedToken], {
        page: 1,
        pageSize: 100,
        total: 101,
      }),
    );
    await act(async () => {
      await pendingRevalidation.promise;
    });

    expect(screen.getByText('revalidated-key')).toBeVisible();
    expect(screen.queryByText('foreground-key')).not.toBeInTheDocument();
  });

  it.each(['mutation-first', 'foreground-first'] as const)(
    'reconciles a status mutation after page 1 -> 2 -> 1 (%s)',
    async (completionOrder) => {
      const archivedToken = {
        ...enabledToken,
        id: 109,
        name: 'archived-key',
        key_prefix: 'sk-zt-old1...',
      };
      const pendingAuth = deferredResponse();
      const pendingStatusUpdate = deferredResponse();
      const pendingForeground = deferredResponse();
      const pendingRevalidation = deferredResponse();
      let pageOneReads = 0;
      const fetchMock = vi.fn(
        async (input: RequestInfo | URL, init?: RequestInit) => {
          const url = requestUrl(input);
          const method = requestMethod(init);

          if (url.endsWith('/api/auth/refresh')) {
            return pendingAuth.promise;
          }
          if (url.endsWith('/api/status')) {
            return statusResponse();
          }
          if (url.endsWith('/api/pricing')) {
            return pricingResponse();
          }
          if (
            url.includes('/api/token/?status_only=true') &&
            method === 'PUT'
          ) {
            return pendingStatusUpdate.promise;
          }
          if (url.includes('/api/token/') && method === 'GET') {
            const page = new URL(url, 'http://ztapi.test').searchParams.get(
              'p',
            );
            if (page === '2') {
              return tokenPageResponse([archivedToken], {
                page: 2,
                pageSize: 100,
                total: 101,
              });
            }
            pageOneReads += 1;
            if (pageOneReads === 2) {
              return pendingForeground.promise;
            }
            if (pageOneReads === 3) {
              return pendingRevalidation.promise;
            }
            return tokenPageResponse([enabledToken], {
              page: 1,
              pageSize: 100,
              total: 101,
            });
          }
          return jsonResponse({ success: false, message: 'unexpected' }, 500);
        },
      );
      vi.stubGlobal('fetch', fetchMock);
      renderKeys();

      await act(async () => {
        pendingAuth.resolve(authResponse());
        await pendingAuth.promise;
      });
      const enabledRow = screen.getByText('enabled-key').closest('tr');
      expect(enabledRow).not.toBeNull();
      fireEvent.click(
        within(enabledRow as HTMLElement).getByRole('button', {
          name: '禁用',
        }),
      );
      fireEvent.click(screen.getByRole('button', { name: '下一页' }));
      expect(await screen.findByText('archived-key')).toBeVisible();
      fireEvent.click(screen.getByRole('button', { name: '上一页' }));
      await waitFor(() => expect(pageOneReads).toBe(2));

      if (completionOrder === 'mutation-first') {
        pendingStatusUpdate.resolve(
          jsonResponse({
            success: true,
            data: disabledToken,
          }),
        );
        await act(async () => {
          await pendingStatusUpdate.promise;
        });
      }

      pendingForeground.resolve(
        tokenPageResponse([enabledToken], {
          page: 1,
          pageSize: 100,
          total: 101,
        }),
      );
      await act(async () => {
        await pendingForeground.promise;
      });

      if (completionOrder === 'foreground-first') {
        pendingStatusUpdate.resolve(
          jsonResponse({
            success: true,
            data: disabledToken,
          }),
        );
        await act(async () => {
          await pendingStatusUpdate.promise;
        });
      }

      await waitFor(() => expect(pageOneReads).toBe(3));
      pendingRevalidation.resolve(
        tokenPageResponse([disabledToken], {
          page: 1,
          pageSize: 100,
          total: 101,
        }),
      );
      await act(async () => {
        await pendingRevalidation.promise;
      });

      const updatedRow = screen.getByText('disabled-key').closest('tr');
      expect(updatedRow).not.toBeNull();
      expect(
        within(updatedRow as HTMLElement).getByText('已禁用'),
      ).toBeVisible();
    },
  );

  it('does not resurrect a revoked token from an older background GET', async () => {
    const pendingAuth = deferredResponse();
    const staleCreateRevalidation = deferredResponse();
    const pendingDelete = deferredResponse();
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return jsonResponse({
            success: true,
            data: {
              id: 110,
              key: plaintextKey,
              key_prefix: 'sk-zt-new4',
            },
          });
        }
        if (url.endsWith('/api/token/7') && method === 'DELETE') {
          return pendingDelete.promise;
        }
        if (url.includes('/api/token/') && method === 'GET') {
          pageOneReads += 1;
          if (pageOneReads === 2) {
            return staleCreateRevalidation.promise;
          }
          if (pageOneReads === 3) {
            return tokenPageResponse([], {
              page: 1,
              pageSize: 100,
              total: 0,
            });
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 1,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'stale-revoke-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    await waitFor(() => expect(pageOneReads).toBe(2));

    const enabledRow = screen.getByText('enabled-key').closest('tr');
    expect(enabledRow).not.toBeNull();
    fireEvent.click(
      within(enabledRow as HTMLElement).getByRole('button', { name: '撤销' }),
    );
    fireEvent.click(
      within(enabledRow as HTMLElement).getByRole('button', {
        name: '确认撤销',
      }),
    );
    pendingDelete.resolve(jsonResponse({ success: true }));
    await act(async () => {
      await pendingDelete.promise;
    });
    expect(screen.queryByText('enabled-key')).not.toBeInTheDocument();

    staleCreateRevalidation.resolve(
      tokenPageResponse([enabledToken], {
        page: 1,
        pageSize: 100,
        total: 1,
      }),
    );
    await act(async () => {
      await staleCreateRevalidation.promise;
    });

    expect(screen.queryByText('enabled-key')).not.toBeInTheDocument();
    expect(screen.getByText('尚未创建 API 密钥。')).toBeVisible();
  });

  it('keeps a returning visit in error after an old background response', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const staleToken = {
      ...enabledToken,
      name: 'old-visit-key',
    };
    const pendingAuth = deferredResponse();
    const oldBackground = deferredResponse();
    const returningForeground = deferredResponse();
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return pendingAuth.promise;
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return jsonResponse({
            success: true,
            data: {
              id: 111,
              key: plaintextKey,
              key_prefix: 'sk-zt-new5',
            },
          });
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            return tokenPageResponse([archivedToken], {
              page: 2,
              pageSize: 100,
              total: 101,
            });
          }
          pageOneReads += 1;
          if (pageOneReads === 2) {
            return oldBackground.promise;
          }
          if (pageOneReads === 3) {
            return returningForeground.promise;
          }
          return tokenPageResponse([enabledToken], {
            page: 1,
            pageSize: 100,
            total: 101,
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    await act(async () => {
      pendingAuth.resolve(authResponse());
      await pendingAuth.promise;
    });
    fireEvent.change(screen.getByLabelText('密钥名称'), {
      target: { value: 'visit-error-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    await waitFor(() => expect(pageOneReads).toBe(2));

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('archived-key')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await waitFor(() => expect(pageOneReads).toBe(3));

    returningForeground.resolve(
      jsonResponse({ success: false, message: 'unavailable' }, 503),
    );
    expect(
      await screen.findByText('密钥数据加载失败，请刷新后重试。'),
    ).toBeVisible();

    oldBackground.resolve(
      tokenPageResponse([staleToken], {
        page: 1,
        pageSize: 100,
        total: 101,
      }),
    );
    await act(async () => {
      await oldBackground.promise;
    });

    expect(
      screen.getByText('密钥数据加载失败，请刷新后重试。'),
    ).toBeVisible();
    expect(screen.queryByText('old-visit-key')).not.toBeInTheDocument();
  });

  it('aborts every token GET across repeated unmounts', async () => {
    const tokenSignals: Array<AbortSignal | null | undefined> = [];
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.includes('/api/token/')) {
          const signal = init?.signal;
          tokenSignals.push(signal);
          return new Promise<Response>((_resolve, reject) => {
            signal?.addEventListener(
              'abort',
              () => reject(new DOMException('Aborted', 'AbortError')),
              { once: true },
            );
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    for (let visit = 0; visit < 3; visit += 1) {
      const view = renderKeys();
      await waitFor(() => expect(tokenSignals).toHaveLength(visit + 1));
      view.unmount();
      expect(tokenSignals[visit]).toBeInstanceOf(AbortSignal);
      expect(tokenSignals[visit]?.aborted).toBe(true);
      clearAuthSession();
    }
  });

  it('aborts the old page visit without aborting the returning request', async () => {
    const archivedToken = {
      ...enabledToken,
      id: 109,
      name: 'archived-key',
      key_prefix: 'sk-zt-old1...',
    };
    const pageOneSignals: Array<AbortSignal | null | undefined> = [];
    let pageOneReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = requestMethod(init);

        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.endsWith('/api/token/') && method === 'POST') {
          return jsonResponse({
            success: true,
            data: {
              id: 112,
              key: plaintextKey,
              key_prefix: 'sk-zt-new6',
            },
          });
        }
        if (url.includes('/api/token/') && method === 'GET') {
          const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
          if (page === '2') {
            return tokenPageResponse([archivedToken], {
              page: 2,
              pageSize: 100,
              total: 101,
            });
          }
          pageOneReads += 1;
          if (pageOneReads === 1) {
            return tokenPageResponse([enabledToken], {
              page: 1,
              pageSize: 100,
              total: 101,
            });
          }
          const signal = init?.signal;
          pageOneSignals.push(signal);
          return new Promise<Response>((_resolve, reject) => {
            signal?.addEventListener(
              'abort',
              () => reject(new DOMException('Aborted', 'AbortError')),
              { once: true },
            );
          });
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    renderKeys();

    fireEvent.change(await screen.findByLabelText('密钥名称'), {
      target: { value: 'abort-navigation-key' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建 API Key' }));
    expect(await screen.findByText(plaintextKey)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '我已安全保存' }));
    await waitFor(() => expect(pageOneSignals).toHaveLength(1));

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(pageOneSignals[0]).toBeInstanceOf(AbortSignal);
    expect(pageOneSignals[0]?.aborted).toBe(true);
    expect(await screen.findByText('archived-key')).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await waitFor(() => expect(pageOneSignals).toHaveLength(2));
    expect(pageOneSignals[1]).toBeInstanceOf(AbortSignal);
    expect(pageOneSignals[1]?.aborted).toBe(false);
  });

  it('does not commit an aborted StrictMode visit', async () => {
    const firstVisit = deferredResponse();
    const tokenSignals: Array<AbortSignal | null | undefined> = [];
    let tokenReads = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        if (url.endsWith('/api/auth/refresh')) {
          return authResponse();
        }
        if (url.endsWith('/api/status')) {
          return statusResponse();
        }
        if (url.endsWith('/api/pricing')) {
          return pricingResponse();
        }
        if (url.includes('/api/token/')) {
          tokenReads += 1;
          tokenSignals.push(init?.signal);
          if (tokenReads === 1) {
            return firstVisit.promise;
          }
          return tokenPageResponse([
            { ...enabledToken, name: 'strict-current-key' },
          ]);
        }
        return jsonResponse({ success: false, message: 'unexpected' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    render(
      <StrictMode>
        <AppProviders>
          <RouterProvider router={createZTAPIRouter(['/console/keys'])} />
        </AppProviders>
      </StrictMode>,
    );

    expect(await screen.findByText('strict-current-key')).toBeVisible();
    expect(tokenSignals[0]).toBeInstanceOf(AbortSignal);
    expect(tokenSignals[0]?.aborted).toBe(true);

    firstVisit.resolve(
      tokenPageResponse([{ ...enabledToken, name: 'strict-stale-key' }]),
    );
    await act(async () => {
      await firstVisit.promise;
    });

    expect(screen.getByText('strict-current-key')).toBeVisible();
    expect(screen.queryByText('strict-stale-key')).not.toBeInTheDocument();
  });
});
