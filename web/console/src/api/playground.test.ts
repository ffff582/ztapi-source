import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from './client';
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
