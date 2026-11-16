// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
export function referralBrowserLaunchOptions(platform = process.platform, env = process.env) {
  const channel = env.ZTAPI_REFERRAL_BROWSER_CHANNEL?.trim()
    || (platform === 'win32' ? 'chrome' : 'bundled');
  return channel === 'bundled' ? { headless: true } : { headless: true, channel };
}
