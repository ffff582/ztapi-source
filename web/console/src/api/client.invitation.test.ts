import { afterEach, expect, it, vi } from 'vitest';
import { authErrorMessage, clearAuthSession, register } from './client';
afterEach(() => { clearAuthSession(); vi.unstubAllGlobals(); });

it('classifies invalid invitation separately from a captcha failure', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: false, message: 'invalid invitation code' }), { status: 400 })));
  let failure: unknown;
  try { await register({ username: 'offline', password: 'memory-only-password', invite_code: 'invalid-code' }); } catch (error) { failure = error; }
  expect(failure).toMatchObject({ kind: 'invalid_invitation' });
  expect(authErrorMessage(failure)).toBe('邀请码无效，请核对或清除邀请码后普通注册。');
});

it('classifies temporary invitation unavailability on 409 with a removable-invite recovery message', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: false, message: 'invitation temporarily unavailable' }), { status: 409 })));
  let failure: unknown;
  try { await register({ username: 'offline', password: 'memory-only-password', invite_code: 'temporarily-unavailable' }); } catch (error) { failure = error; }
  expect(failure).toMatchObject({ kind: 'invitation_unavailable' });
  expect(authErrorMessage(failure)).toBe('推广活动暂不可用，可移除邀请码后继续注册');
});
