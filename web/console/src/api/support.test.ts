import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from './client';
import { supportApi, SupportError } from './support';
import { apiClient } from './client';

const session = {
  access_token: 'test-session',
  expires_in: 900,
  user: { id: 7, username: 'alice', role: 1, group: 'default' },
};
const response = (data: unknown, status = 200) =>
  new Response(
    JSON.stringify({
      success: status < 400,
      data,
      message: 'private upstream body',
    }),
    { status },
  );
beforeEach(() => setAuthSession(session));
afterEach(() => {
  clearAuthSession();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('support HTTP contract', () => {
  it('recognizes only the stable image-storage-full error code, never arbitrary upstream text', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({
              success: false,
              message: 'support_image_storage_full',
            }),
            { status: 413 },
          ),
        ),
    );
    await expect(
      supportApi.sendMessage('c1', {
        content: '截图',
        idempotency_key: 'fixed',
      }),
    ).rejects.toMatchObject({ status: 413, code: 'image_storage_full' });
  });
  it('reads private attachments through the existing JWT API client', async () => {
    const attachment = { mime_type: 'image/png', data_base64: 'iVBORw0KGgo=' };
    const read = vi.spyOn(apiClient, 'get').mockResolvedValue(attachment);
    expect(await supportApi.attachment(31)).toEqual(attachment);
    expect(read).toHaveBeenCalledWith('/support/attachments/31');
  });
  it('uses authenticated same-origin JSON and the exact stable mutation payload', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(response({ pending: true, call_id: 'call-1' }, 202));
    vi.stubGlobal('fetch', fetchMock);
    const payload = {
      content: '诊断',
      request_id: 'req-1',
      idempotency_key: 'fixed-key',
    };
    expect(await supportApi.sendMessage('a/b', payload)).toEqual({
      pending: true,
      call_id: 'call-1',
    });
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/support/conversations/a%2Fb/messages');
    expect(init.credentials).toBe('include');
    expect(new Headers(init.headers).get('Authorization')).toBe(
      'Bearer test-session',
    );
    expect(new Headers(init.headers).get('Content-Type')).toBe(
      'application/json',
    );
    expect(JSON.parse(init.body)).toEqual(payload);
  });

  it.each([401, 409, 429, 500])(
    'does not refresh or replay a mutation on HTTP %i or expose a raw error',
    async (status) => {
      const fetchMock = vi.fn().mockResolvedValue(response(null, status));
      vi.stubGlobal('fetch', fetchMock);
      await expect(
        supportApi.sendMessage('c1', {
          content: '你好',
          idempotency_key: 'fixed',
        }),
      ).rejects.toMatchObject({ status });
      expect(fetchMock).toHaveBeenCalledOnce();
      await expect(supportApi.config()).rejects.not.toThrow(
        'private upstream body',
      );
    },
  );

  it('never retries a disconnected request', async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValue(new TypeError('network failed with secret'));
    vi.stubGlobal('fetch', fetchMock);
    await expect(supportApi.createConversation('fixed')).rejects.toBeInstanceOf(
      SupportError,
    );
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('rejects late responses after the account changes', async () => {
    let finish!: (value: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const pending = supportApi.conversations();
    setAuthSession({ ...session, user: { ...session.user, id: 8 } });
    finish(response({ items: [{ id: 'private-other-account' }] }));
    await expect(pending).rejects.toMatchObject({ status: 401 });
  });

  it('uses owner-scoped read and ticket endpoints without client-selected user IDs', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(() => Promise.resolve(response({ items: [] })));
    vi.stubGlobal('fetch', fetchMock);
    await supportApi.config();
    await supportApi.conversations();
    await supportApi.conversation('c1');
    await supportApi.requests();
    await supportApi.diagnosis('request/a');
    await supportApi.createTicket({
      conversation_id: 'c1',
      subject: '失败',
      content: '请检查',
      idempotency_key: 'ticket-key',
    });
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/api/support/config',
      '/api/support/conversations',
      '/api/support/conversations/c1',
      '/api/support/requests',
      '/api/support/diagnostics/request%2Fa',
      '/api/support/tickets',
    ]);
  });
});
