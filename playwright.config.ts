import { defineConfig, devices } from '@playwright/test';

const productionBaseURL = process.env.ZTAPI_E2E_BASE_URL;
const baseURL = productionBaseURL ?? 'http://127.0.0.1:4173';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 7_000 },
  reporter: [['list']],
  use: {
    baseURL,
    channel: 'chrome',
    trace: 'retain-on-failure',
  },
  webServer: productionBaseURL === undefined
    ? [
        {
          command: 'pnpm --filter @ztapi/console build && pnpm --filter @ztapi/console exec vite preview --host 127.0.0.1 --port 4173 --strictPort',
          url: baseURL,
          reuseExistingServer: false,
          timeout: 180_000,
        },
        {
          command: 'pnpm --dir server/web/classic build && pnpm --dir server/web/classic exec rsbuild preview --host 127.0.0.1 --port 4174',
          env: { VITE_ZTAPI_ADMIN_APP: 'true' },
          url: 'http://127.0.0.1:4174',
          reuseExistingServer: false,
          timeout: 180_000,
        },
      ]
    : undefined,
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
    { name: 'laptop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } } },
    { name: 'tablet-820', use: { ...devices['Desktop Chrome'], viewport: { width: 820, height: 1180 } } },
    { name: 'mobile-430', use: { ...devices['Desktop Chrome'], viewport: { width: 430, height: 932 }, isMobile: true } },
    { name: 'mobile-390', use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 }, isMobile: true } },
    { name: 'mobile-320', use: { ...devices['Desktop Chrome'], viewport: { width: 320, height: 568 }, isMobile: true } },
  ],
});
