import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, refreshSession, setAuthSession } from './client';
import { PlaygroundClientError, playgroundClient } from './playground';

function response(body: unknown, status = 200, requestID = '') {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'Content-Type': 'application/json',
      ...(requestID === '' ? {} : { 'X-Request-ID': requestID }),
    },
  });
}

function session(token = 'session-token') {
  return {
    access_token: token,
    expires_in: 900,
    user: { id: 7, username: 'alice', role: 1, group: 'default' },
  };
}

afterEach(() => {
  clearAuthSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('playgroundClient.chat', () => {
  it.each(['chat', 'imageEdit'] as const)('retries %s with unchanged input after another request refreshed the same login', async (method) => {
    setAuthSession(session('expired-token'));
    let finish!: (value: Response) => void;
    let requests = 0;
    const fetchMock = vi.fn(async (...args: [RequestInfo | URL, RequestInit?]) => {
      const input = args[0];
      if (input.toString() === '/api/auth/refresh') return response({ success: true, data: session('fresh-token') });
      if (++requests === 1) return new Promise<Response>((resolve) => { finish = resolve; });
      return method === 'chat'
        ? response({ choices: [{ message: { content: 'Remembered' }, finish_reason: 'stop' }] })
        : response({ data: [{ url: '/edited.png' }] });
    });
    vi.stubGlobal('fetch', fetchMock);
    const file = new File(['synthetic reference'], 'reference.png', { type: 'image/png' });
    const result = method === 'chat'
      ? playgroundClient.chat({ model: 'zt-model', prompt: 'Follow-up', history: [{ role: 'user', content: 'Prior turn' }, { role: 'assistant', content: 'Prior reply' }] })
      : playgroundClient.imageEdit([file], { model: 'zt-image', prompt: 'Edit reference', n: 2 });
    const settled = result.catch((error: unknown) => error);
    await refreshSession();
    finish(response({}, 401));
    expect(await settled).toMatchObject(method === 'chat' ? { text: 'Remembered' } : { images: [{ url: '/edited.png' }] });
    const calls = fetchMock.mock.calls.filter(([input]) => input.toString() !== '/api/auth/refresh');
    expect(calls).toHaveLength(2);
    const first = calls[0][1] as RequestInit;
    const retry = calls[1][1] as RequestInit;
    expect(new Headers(retry.headers).get('Authorization')).toBe('Bearer fresh-token');
    expect(retry.body).toBe(first.body);
    if (method === 'chat') {
      expect(JSON.parse(String(retry.body)).messages).toEqual([
        { role: 'system', content: '你是一个简洁、可靠的助手。' },
        { role: 'user', content: 'Prior turn' },
        { role: 'assistant', content: 'Prior reply' },
        { role: 'user', content: 'Follow-up' },
      ]);
    } else {
      expect((retry.body as FormData).getAll('image')).toEqual([file]);
      expect((retry.body as FormData).get('prompt')).toBe('Edit reference');
      expect((retry.body as FormData).get('n')).toBe('2');
      expect(new Headers(retry.headers).has('Content-Type')).toBe(false);
    }
    expect(fetchMock.mock.calls.filter(([input]) => input.toString() === '/api/auth/refresh')).toHaveLength(1);
  });

  it.each(['chat', 'imageEdit'] as const)('does not retry %s more than once when the replacement JWT is also rejected', async (method) => {
    setAuthSession(session('expired-token'));
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => input.toString() === '/api/auth/refresh'
      ? response({ success: true, data: session('fresh-token') }) : response({}, 401));
    vi.stubGlobal('fetch', fetchMock);
    const result = method === 'chat'
      ? playgroundClient.chat({ model: 'zt-model', prompt: 'Synthetic prompt' })
      : playgroundClient.imageEdit([new File(['reference'], 'reference.png', { type: 'image/png' })], { model: 'zt-image', prompt: 'Synthetic edit' });
    await expect(result).rejects.toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls.filter(([input]) => input.toString() === '/api/auth/refresh')).toHaveLength(1);
  });

  it.each(['same-account login', 'same-object login', 'account round trip'] as const)('does not replay chat or media after a %s', async (boundary) => {
    setAuthSession(session());
    const original = getAuthSession()!;
    const finishes: Array<(value: Response) => void> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/auth/refresh') return response({ success: true, data: session('unexpected-refresh') });
      if (finishes.length >= 2) return response({}, 401);
      return new Promise<Response>((resolve) => { finishes.push(resolve); });
    });
    vi.stubGlobal('fetch', fetchMock);
    const chat = playgroundClient.chat({ model: 'zt-model', prompt: 'Private draft' }).catch((error: unknown) => error);
    const media = playgroundClient.createVideo({ model: 'zt-video', prompt: 'Private video' }).catch((error: unknown) => error);
    if (boundary === 'account round trip') setAuthSession({ ...session('bob'), user: { ...original.user, id: 10 } });
    clearAuthSession();
    setAuthSession(boundary === 'same-object login' ? original : session('new-login'));
    finishes.forEach((finish) => finish(response({}, 401)));
    expect(await chat).toMatchObject({ kind: 'unauthorized' });
    expect(await media).toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('does not replay a request when the same account logs in while refresh is pending', async () => {
    setAuthSession(session('expired-token'));
    let finishRefresh!: (value: Response) => void;
    let started!: () => void;
    const refreshing = new Promise<void>((resolve) => { started = resolve; });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/auth/refresh') {
        started();
        return new Promise<Response>((resolve) => { finishRefresh = resolve; });
      }
      return response({}, 401);
    });
    vi.stubGlobal('fetch', fetchMock);
    const result = playgroundClient.chat({ model: 'zt-model', prompt: 'Private draft' }).catch((error: unknown) => error);
    await refreshing;
    clearAuthSession();
    setAuthSession(session('new-login'));
    finishRefresh(response({ success: true, data: session('stale-refresh') }));
    expect(await result).toMatchObject({ kind: 'unauthorized' });
    expect(getAuthSession()?.access_token).toBe('new-login');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it.each(['chat', 'imageGeneration'] as const)('does not retry %s under another signed-in account after a late 401', async (method) => {
    setAuthSession(session());
    let resolveFirst!: (value: Response) => void;
    const fetchMock = vi.fn(async () => {
      if (fetchMock.mock.calls.length === 1) return new Promise<Response>((resolve) => { resolveFirst = resolve; });
      if (fetchMock.mock.calls.length === 2) return response({ success: true, data: { ...session('bob'), user: { ...session().user, id: 10 } } });
      return response({ choices: [{ message: { content: 'Wrong account' }, finish_reason: 'stop' }], data: [{ url: '/synthetic.png' }] });
    });
    vi.stubGlobal('fetch', fetchMock);
    const result = playgroundClient[method]({ model: 'zt-model', prompt: 'Alice private question' }).catch((error: unknown) => error);
    setAuthSession({ ...session('bob'), user: { ...session().user, id: 10 } });
    resolveFirst(response({ error: { code: 'unauthorized' } }, 401));
    expect(await result).toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not replay old history when the account changes while refresh is pending', async () => {
    setAuthSession(session());
    let resolveRefresh!: (value: Response) => void;
    let markRefreshing!: () => void;
    const refreshing = new Promise<void>((resolve) => { markRefreshing = resolve; });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/auth/refresh') {
        markRefreshing();
        return new Promise<Response>((resolve) => { resolveRefresh = resolve; });
      }
      if (fetchMock.mock.calls.length === 1) return response({ error: { code: 'unauthorized' } }, 401);
      return response({ choices: [{ message: { content: 'Wrong account' }, finish_reason: 'stop' }] });
    });
    vi.stubGlobal('fetch', fetchMock);
    const result = playgroundClient.chat({ model: 'zt-model', prompt: 'Follow-up', history: [{ role: 'user', content: 'Alice private context' }] }).catch((error: unknown) => error);
    await refreshing;
    setAuthSession({ ...session('bob'), user: { ...session().user, id: 10 } });
    resolveRefresh(response({ success: true, data: session('alice-refreshed') }));
    expect(await result).toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('sends ordered conversation history followed by the current user turn exactly once', async () => {
    setAuthSession(session());
    const fetchMock = vi.fn().mockResolvedValue(response({
      choices: [{ message: { content: 'Remembered' }, finish_reason: 'stop' }],
    }));
    vi.stubGlobal('fetch', fetchMock);
    const history = [
      { role: 'user' as const, content: 'My project is Atlas.' },
      { role: 'assistant' as const, content: 'I will remember Atlas.' },
    ];

    await playgroundClient.chat({ model: 'zt-model', prompt: 'What is its name?', history });

    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body)).messages).toEqual([
      { role: 'system', content: '你是一个简洁、可靠的助手。' },
      ...history,
      { role: 'user', content: 'What is its name?' },
    ]);
    expect(history).toHaveLength(2);
  });

  it('sends the signed-in identity to the billable playground and parses the result', async () => {
    setAuthSession(session());
    const fetchMock = vi.fn().mockResolvedValue(response({
      id: 'chatcmpl-upstream',
      choices: [{ message: { role: 'assistant', content: '你好' }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 },
    }, 200, 'ztapi-request-1'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(playgroundClient.chat({
      model: 'zt-gpt-5.6-sol',
      prompt: '介绍一下你自己',
    })).resolves.toEqual({
      text: '你好',
      response_id: 'chatcmpl-upstream',
      request_id: 'ztapi-request-1',
      finish_reason: 'stop',
      usage: { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 },
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/pg/chat/completions');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer session-token');
    expect(headers.get('New-Api-User')).toBe('7');
    expect(JSON.parse(String(init.body))).toEqual({
      model: 'zt-gpt-5.6-sol',
      messages: [
        { role: 'system', content: '你是一个简洁、可靠的助手。' },
        { role: 'user', content: '介绍一下你自己' },
      ],
      stream: false,
      max_tokens: 256,
    });
  });

  it('refreshes one expired session and retries once', async () => {
    setAuthSession(session('expired-token'));
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ success: false, message: 'unauthorized' }, 401))
      .mockResolvedValueOnce(response({ success: true, data: session('fresh-token') }))
      .mockResolvedValueOnce(response({
        id: 'chatcmpl-retry',
        choices: [{ message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }],
      }, 200, 'ztapi-request-retry'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(playgroundClient.chat({ model: 'zt-gpt-5.6-sol', prompt: '你好' }))
      .resolves.toMatchObject({ text: 'OK', request_id: 'ztapi-request-retry' });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[1][0]).toBe('/api/auth/refresh');
    expect(new Headers(fetchMock.mock.calls[2][1]?.headers).get('Authorization'))
      .toBe('Bearer fresh-token');
  });

  it.each([
    [403, 'insufficient_user_quota', 'insufficient_balance'],
    [404, 'model_not_found', 'model_unavailable'],
    [429, 'rate_limit_exceeded', 'rate_limited'],
    [503, 'model_not_found', 'model_unavailable'],
    [500, 'server_error', 'unknown'],
  ] as const)('classifies HTTP %s without exposing the raw upstream message', async (status, code, kind) => {
    setAuthSession(session());
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({
      error: { code, message: 'raw upstream secret detail' },
    }, status)));

    const error = await playgroundClient.chat({ model: 'zt-model', prompt: 'hello' })
      .catch((value: unknown) => value);
    expect(error).toBeInstanceOf(PlaygroundClientError);
    expect(error).toMatchObject({ kind });
    expect(String(error)).not.toContain('raw upstream secret detail');
  });
});

describe('playgroundClient media methods', () => {
  it('generates images with catalog-selected options and parses image data', async () => {
    setAuthSession(session());
    const fetchMock = vi.fn().mockResolvedValue(response({
      created: 1_760_000_000,
      data: [{ url: 'https://cdn.example/image.png', revised_prompt: 'revised' }],
    }, 200, 'image-request-1'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(playgroundClient.imageGeneration({
      model: 'zt-image-1',
      prompt: '一只猫',
      size: '1024x1024',
      quality: 'standard',
      n: 1,
      response_format: 'url',
    })).resolves.toEqual({
      created: 1_760_000_000,
      request_id: 'image-request-1',
      images: [{ url: 'https://cdn.example/image.png', revised_prompt: 'revised' }],
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/pg/images/generations');
    expect(JSON.parse(String(init.body))).toEqual({
      model: 'zt-image-1', prompt: '一只猫', size: '1024x1024', quality: 'standard', n: 1, response_format: 'url',
    });
  });

  it('submits multiple reference images as ordered multipart fields without owning the boundary', async () => {
    setAuthSession(session());
    const first = new File([new Uint8Array([1, 2])], 'first.png', { type: 'image/png' });
    const second = new File([new Uint8Array([3, 4])], 'second.webp', { type: 'image/webp' });
    const fetchMock = vi.fn().mockResolvedValue(response({
      created: 1_760_000_000,
      data: [{ url: 'https://cdn.example/edited.png' }],
    }, 200, 'edit-request-1'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(playgroundClient.imageEdit([first, second], {
      model: 'zt-image-1',
      prompt: '保留第一张图的人物，使用第二张图的背景',
      size: '1024x1024',
      quality: 'standard',
      n: 10,
      response_format: 'url',
    })).resolves.toMatchObject({ request_id: 'edit-request-1', images: [{ url: 'https://cdn.example/edited.png' }] });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/pg/images/edits');
    expect(new Headers(init.headers).has('Content-Type')).toBe(false);
    const body = init.body as FormData;
    expect(body.getAll('image')).toEqual([first, second]);
    expect(body.get('model')).toBe('zt-image-1');
    expect(body.get('prompt')).toBe('保留第一张图的人物，使用第二张图的背景');
    expect(body.get('n')).toBe('10');
    expect(body.get('size')).toBe('1024x1024');
    expect(body.get('quality')).toBe('standard');
    expect(body.get('response_format')).toBe('url');
  });

  it('creates and fetches a video task through the authenticated workbench routes', async () => {
    setAuthSession(session());
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ id: 'task-1', status: 'queued' }, 200, 'video-submit-1'))
      .mockResolvedValueOnce(response({
        code: 'success',
        data: { task_id: 'task-1', status: 'succeeded', result_url: '/v1/videos/task-1/content' },
      }, 200, 'video-fetch-1'));
    vi.stubGlobal('fetch', fetchMock);

    await expect(playgroundClient.createVideo({
      model: 'zt-video-1', prompt: '一段海边日落', size: '720p', duration: 5,
    })).resolves.toMatchObject({ task_id: 'task-1', status: 'queued', request_id: 'video-submit-1' });
    await expect(playgroundClient.fetchVideo('task-1')).resolves.toEqual({
      task_id: 'task-1', status: 'succeeded', url: '/v1/videos/task-1/content', request_id: 'video-fetch-1',
    });

    expect(fetchMock.mock.calls[0][0]).toBe('/pg/video/generations');
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      model: 'zt-video-1', prompt: '一段海边日落', size: '720p', duration: 5,
    });
    expect(fetchMock.mock.calls[1][0]).toBe('/pg/video/generations/task-1');
  });

  it('rejects a media response without a usable result identity', async () => {
    setAuthSession(session());
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ data: [] })));

    const error = await playgroundClient.imageGeneration({ model: 'zt-image-1', prompt: 'test' })
      .catch((value: unknown) => value);
    expect(error).toBeInstanceOf(PlaygroundClientError);
    expect(error).toMatchObject({ kind: 'invalid_response' });
  });
});
