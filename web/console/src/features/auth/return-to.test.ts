import { describe, expect, it } from 'vitest';
import { consoleReturnTo } from './return-to';

describe('post-login return URL', () => {
  it('retains the protected referral destination including query', () => {
    expect(consoleReturnTo('?redirect=%2Fconsole%2Freferrals%3Fpage%3D2')).toBe('/console/referrals?page=2');
  });
  it.each(['https://evil.example', '//evil.example', '/\\evil.example', '/console-evil', '/register', 'javascript:alert(1)'])('rejects unsafe or non-console destination %s', (value) => {
    expect(consoleReturnTo(`?redirect=${encodeURIComponent(value)}`)).toBe('/console');
  });
});
