import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdtemp, rm } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

assert.equal(process.platform, 'darwin');
const [bundle, playwright, mode] = process.argv.slice(2);
assert.ok(mode === undefined || mode === 'keychain' || mode === 'keychain-async');
const { _electron: electron } = createRequire(import.meta.url)(resolve(playwright));
const home = await mkdtemp(join(homedir(), 'ztapi-qa-driver-'));
let stage = 'launch';
let app;
const watchdog = setTimeout(() => {
  console.error(`QA_DRIVER_TIMEOUT stage=${stage}`);
  process.exit(124);
}, 60_000);
function enter(next) {
  stage = next;
  console.log(`QA_DRIVER_STAGE ${stage}`);
}
try {
  enter('launch');
  app = await electron.launch({ executablePath: join(bundle, 'Contents/MacOS/Electron'),
    args: ['--ztapi-qa-driver'], env: { ...process.env, HOME: home }, chromiumSandbox: true, timeout: 20_000 });
  enter('window');
  const page = await app.firstWindow({ timeout: 10_000 });
  enter('renderer');
  await page.getByRole('button', { name: 'Fixture', exact: true }).waitFor({ timeout: 10_000 });
  assert.equal(await page.evaluate(() => typeof require), 'undefined');
  enter('main');
  assert.equal(await app.evaluate(({ app: host, BrowserWindow }) =>
    !host.commandLine.hasSwitch('no-sandbox') && BrowserWindow.getAllWindows()[0]?.webContents.getLastWebPreferences().sandbox), true);
  if (mode === 'keychain') {
    enter('keychain-availability');
    assert.equal(await app.evaluate(({ safeStorage }) => safeStorage.isEncryptionAvailable()), true);
    enter('keychain-round-trip');
    assert.equal(await app.evaluate(({ safeStorage }) => {
      const input = 'synthetic workbench session check';
      const encrypted = safeStorage.encryptString(input);
      return !encrypted.includes(input) && safeStorage.decryptString(encrypted) === input;
    }), true);
  }
  if (mode === 'keychain-async') {
    enter('keychain-async-start');
    await app.evaluate(({ safeStorage }) => {
      globalThis.qaEncryption = safeStorage.isAsyncEncryptionAvailable().then(async available => {
        if (!available) return 'unavailable';
        const input = 'synthetic workbench session check';
        const encrypted = await safeStorage.encryptStringAsync(input);
        const decrypted = await safeStorage.decryptStringAsync(encrypted);
        return !encrypted.includes(input) && decrypted.result === input ? 'round-trip' : 'wrong-result';
      }).catch(() => 'provider-error');
    });
    enter('keychain-async-window-responsive');
    await page.getByRole('button', { name: 'Fixture', exact: true }).click({ timeout: 5000 });
    assert.equal(await app.evaluate(() => 42), 42);
    enter('keychain-async-settlement');
    const result = await app.evaluate(() => Promise.race([globalThis.qaEncryption,
      new Promise(resolve => setTimeout(() => resolve('still-pending'), 7000))]));
    console.log(`QA_KEYCHAIN_ASYNC_RESULT ${result}`);
    assert.equal(result, 'round-trip');
  }
  enter('close');
  await app.close();
  app = undefined;
  enter('passed');
} catch (error) {
  console.error(`QA_DRIVER_FAILED stage=${stage}: ${error.name}`);
  process.exitCode = 1;
} finally {
  try {
    if (app) await app.close();
    await rm(home, { recursive: true, force: true });
  } finally {
    clearTimeout(watchdog);
  }
}
