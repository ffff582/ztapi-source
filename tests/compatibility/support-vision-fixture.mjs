import { createHash } from 'node:crypto';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';

export const defaultVisionPath = path.resolve(
  process.env.SUPPORT_VISION_FIXTURE ||
    'C:/Users/Administrator/Documents/翻译软件/outputs/ztapi-commercial-upgrade-20261030/support-20261102/vision-fixture.png',
);

export async function generateVisionFixture(
  browser,
  destination = defaultVisionPath,
) {
  const context = await browser.newContext({
    viewport: { width: 640, height: 360 },
    deviceScaleFactor: 1,
    colorScheme: 'light',
    serviceWorkers: 'block',
  });
  await context.route('**/*', (route) => route.abort());
  const page = await context.newPage();
  try {
    await page.setContent(
      '<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Synthetic HTTP 503 fixture</title><style>html,body{margin:0;width:100%;height:100%;background:#fff;color:#111;font-family:Arial,sans-serif}main{display:flex;min-height:360px;align-items:center;justify-content:center;flex-direction:column;gap:24px}h1{margin:0;font-size:72px;line-height:1;font-weight:700;letter-spacing:0}p{margin:0;font-size:26px;line-height:1.4;letter-spacing:0;color:#444}</style></head><body><main><h1>HTTP 503</h1><p>UPSTREAM_UNAVAILABLE</p></main></body></html>',
    );
    await mkdir(path.dirname(destination), { recursive: true });
    await page.screenshot({
      path: destination,
      type: 'png',
      fullPage: false,
      animations: 'disabled',
    });
    const bytes = await readFile(destination);
    return {
      path: destination,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      bytes: bytes.length,
      width: 640,
      height: 360,
      customer_data: false,
      paid_image_generation: false,
    };
  } finally {
    await context.close();
  }
}

if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    console.log(
      JSON.stringify(
        await generateVisionFixture(
          browser,
          process.argv[2] || defaultVisionPath,
        ),
        null,
        2,
      ),
    );
  } finally {
    await browser.close();
  }
}
