import { afterEach, expect, it, vi } from 'vitest';
import { AuthClientError, authErrorMessage, clearAuthSession, register } from './client';

afterEach(() => {
  clearAuthSession();
  vi.unstubAllGlobals();
});

it('classifies only the documented captcha rejection as invalid captcha', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
    JSON.stringify({ success: false, message: 'invalid captcha' }),
    { status: 400, headers: { 'Content-Type': 'application/json' } },
  )));
  await expect(register({ username: 'qa_captcha', password: 'synthetic-passphrase', captcha_id: 'one', captcha_code: 'bad' }))
    .rejects.toMatchObject({ kind: 'invalid_captcha' });
});

it('provides actionable captcha copy instead of a server outage message', () => {
  expect(authErrorMessage(new AuthClientError('invalid_captcha')))
    .toBe('验证码错误或已过期，请输入新的验证码');
});

it.each([
  [503, 'invalid captcha'],
  [400, 'invalid captcha: internal password=private-value'],
  [500, 'database failure: internal-secret'],
])('keeps unrecognized server failures private (%s)', async (status, message) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
    JSON.stringify({ success: false, message }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )));
  try {
    await register({ username: 'qa_captcha', password: 'synthetic-passphrase' });
    throw new Error('registration unexpectedly succeeded');
  } catch (error) {
    expect(error).toBeInstanceOf(AuthClientError);
    expect(authErrorMessage(error)).toBe('服务暂不可用，请稍后重试');
  }
});
