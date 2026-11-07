export function referralBrowserLaunchOptions(platform, channelOverride) {
  const channel = channelOverride?.trim().toLowerCase() || (platform === 'win32' ? 'chrome' : 'bundled');
  if (channel === 'bundled' || channel === 'chromium') return { headless: true };
  if (channel === 'chrome' || channel === 'msedge') return { channel, headless: true };
  throw new Error('ZTAPI_REFERRAL_BROWSER_CHANNEL must be chrome, msedge, bundled or chromium.');
}
