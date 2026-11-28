import * as adminSession from '../../auth/admin-session';
import { clearAdminSession, setAdminSession } from '../../auth/admin-session';
import { supportAdminApi } from './support-api';

const respond = (data, status = 200) =>
  new Response(
    JSON.stringify({
      success: status < 400,
      data,
      message: 'secret internal body',
    }),
    { status },
  );
beforeEach(() =>
  setAdminSession({
    access_token: 'staff-token',
    expires_in: 900,
    user: { id: 9, username: 'staff', role: 10 },
  }),
);
afterEach(() => {
  clearAdminSession();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
it('reads private attachments through the existing JWT admin request helper', async () => {
  const attachment = { mime_type: 'image/png', data_base64: 'iVBORw0KGgo=' };
  const read = vi
    .spyOn(adminSession, 'adminRequest')
    .mockResolvedValue(attachment);
  expect(await supportAdminApi.attachment(31)).toEqual(attachment);
  expect(read).toHaveBeenCalledWith({
    url: '/api/admin/support/attachments/31',
  });
});
it('uses exact support paths, cursor and immutable JSON payloads', async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation(() => Promise.resolve(respond({ items: [] })));
  vi.stubGlobal('fetch', fetchMock);
  await supportAdminApi.list('in_progress', 32);
  await supportAdminApi.detail(8);
  await supportAdminApi.reply(8, {
    content: '已核实',
    idempotency_key: 'reply-key',
  });
  await supportAdminApi.status(8, {
    status: 'resolved',
    version: 2,
    idempotency_key: 'status-key',
  });
  expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
    '/api/admin/support/tickets?status=in_progress&after_id=32&limit=20',
    '/api/admin/support/tickets/8',
    '/api/admin/support/tickets/8/replies',
    '/api/admin/support/tickets/8/status',
  ]);
  const init = fetchMock.mock.calls[3][1];
  expect(init.credentials).toBe('include');
  expect(new Headers(init.headers).get('Authorization')).toBe(
    'Bearer staff-token',
  );
  expect(new Headers(init.headers).get('Content-Type')).toBe(
    'application/json',
  );
  expect(JSON.parse(init.body)).toEqual({
    status: 'resolved',
    version: 2,
    idempotency_key: 'status-key',
  });
});
it.each([401, 403, 409, 500])(
  'preserves HTTP %i and never retries or reveals raw errors',
  async (status) => {
    const fetchMock = vi.fn().mockResolvedValue(respond(null, status));
    vi.stubGlobal('fetch', fetchMock);
    await expect(
      supportAdminApi.status(8, {
        status: 'resolved',
        version: 2,
        idempotency_key: 'key',
      }),
    ).rejects.toMatchObject({ status });
    expect(fetchMock).toHaveBeenCalledOnce();
  },
);
it('does not deliver a late ticket response to another account', async () => {
  let finish;
  vi.stubGlobal(
    'fetch',
    vi.fn(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    ),
  );
  const pending = supportAdminApi.detail(8);
  clearAdminSession();
  finish(respond({ ticket: { id: 8 } }));
  await expect(pending).rejects.toMatchObject({ status: 401 });
});
