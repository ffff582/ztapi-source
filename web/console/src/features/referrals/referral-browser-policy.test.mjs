import { describe, expect, it } from 'vitest';
import { referralBrowserLaunchOptions } from './referral-browser-policy.mjs';

describe('referral browser platform policy', () => {
  it('uses installed Chrome by default on Windows', () => {
    expect(referralBrowserLaunchOptions('win32')).toEqual({ channel: 'chrome', headless: true });
  });
  it.each(['linux', 'darwin'])('uses bundled Chromium without a channel on %s', (platform) => {
    expect(referralBrowserLaunchOptions(platform)).toEqual({ headless: true });
  });
  it.each(['bundled', 'chromium'])('can force bundled Chromium on Windows with override %s', (channel) => {
    expect(referralBrowserLaunchOptions('win32', channel)).toEqual({ headless: true });
  });
  it.each(['chrome', 'msedge'])('can explicitly select channel %s on Linux', (channel) => {
    expect(referralBrowserLaunchOptions('linux', channel)).toEqual({ channel, headless: true });
  });
  it('rejects unsupported environment overrides instead of silently selecting another browser', () => {
    expect(() => referralBrowserLaunchOptions('linux', 'remote-browser')).toThrow('ZTAPI_REFERRAL_BROWSER_CHANNEL');
  });
});
