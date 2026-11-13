import assert from 'node:assert/strict';
import test from 'node:test';
import { referralBrowserLaunchOptions } from './referral-browser-launch-options.mjs';

test('Windows keeps the installed Chrome default', () => {
  assert.deepEqual(referralBrowserLaunchOptions('win32', {}), { headless: true, channel: 'chrome' });
});

test('Linux defaults to bundled Playwright Chromium without a Chrome channel', () => {
  const options = referralBrowserLaunchOptions('linux', {});
  assert.deepEqual(options, { headless: true });
  assert.equal(Object.hasOwn(options, 'channel'), false);
});

test('macOS also defaults to bundled Playwright Chromium', () => {
  assert.deepEqual(referralBrowserLaunchOptions('darwin', {}), { headless: true });
});

test('an explicit channel overrides the Linux default', () => {
  assert.deepEqual(referralBrowserLaunchOptions('linux', { ZTAPI_REFERRAL_BROWSER_CHANNEL: ' chrome ' }), { headless: true, channel: 'chrome' });
});

test('an explicit Chromium channel overrides the Windows default', () => {
  assert.deepEqual(referralBrowserLaunchOptions('win32', { ZTAPI_REFERRAL_BROWSER_CHANNEL: 'chromium' }), { headless: true, channel: 'chromium' });
});

test('bundled explicitly omits the channel on Windows and Linux', () => {
  for (const platform of ['win32', 'linux']) {
    assert.deepEqual(referralBrowserLaunchOptions(platform, { ZTAPI_REFERRAL_BROWSER_CHANNEL: 'bundled' }), { headless: true });
  }
});

test('a blank override preserves the platform default', () => {
  assert.deepEqual(referralBrowserLaunchOptions('win32', { ZTAPI_REFERRAL_BROWSER_CHANNEL: '  ' }), { headless: true, channel: 'chrome' });
  assert.deepEqual(referralBrowserLaunchOptions('linux', { ZTAPI_REFERRAL_BROWSER_CHANNEL: '' }), { headless: true });
});
