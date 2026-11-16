import { beforeEach, describe, expect, it, vi } from 'vitest';
import { captureInvitation, clearInvitation } from './invitation';

beforeEach(() => sessionStorage.clear());

describe('registration invitation', () => {
  it('retains only the invite code through refresh and captcha retries', () => {
    expect(captureInvitation('?invite=zt-ref-abc&password=secret&key=sk-secret')).toBe('zt-ref-abc');
    expect(sessionStorage.length).toBe(1);
    expect(sessionStorage.getItem('invite_code')).toBe('zt-ref-abc');
    expect(captureInvitation('')).toBe('zt-ref-abc');
    expect(sessionStorage.getItem('invite_code')).not.toContain('secret');
    clearInvitation();
    expect(captureInvitation('')).toBeUndefined();
  });

  it.each(['?invite=', '?invite=%3Cscript%3E', `?invite=${'x'.repeat(129)}`, '?invite=a&invite=b'])('rejects invalid or ambiguous invitation %s without breaking registration', (search) => {
    expect(captureInvitation(search)).toBeUndefined();
    expect(sessionStorage.length).toBe(0);
  });

  it('works when session storage is unavailable', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(captureInvitation('?invite=zt-ref-abc')).toBe('zt-ref-abc');
  });
});
