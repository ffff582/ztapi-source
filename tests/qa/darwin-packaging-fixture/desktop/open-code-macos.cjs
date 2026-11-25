const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { app, BrowserWindow } = require('electron');

app.enableSandbox();
app.whenReady().then(async () => {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.homedir(), 'ztapi-dmg-qa-')));
  const window = new BrowserWindow({ show: false, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  try {
    await window.loadURL('data:text/html,%3Ctitle%3EZTAPI%20QA%3C%2Ftitle%3E');
    assert.equal(window.webContents.getTitle(), 'ZTAPI QA');
    assert.equal(await window.webContents.executeJavaScript('typeof require'), 'undefined');
    const { invokeMacEngine } = await import('./macos/engine-host.mjs');
    const { createMacProjectPorts } = await import('./macos/project-ports.mjs');
    const { MacProjectTask } = await import('./macos/project-task.mjs');
    const native = path.join(process.resourcesPath, 'ztapi-native');
    const host = path.join(native, 'ZTAPI Engine.app', 'Contents', 'MacOS', 'ZTAPIOpenCode');
    const worker = path.join(native, 'ZTAPI Tests.app', 'Contents', 'MacOS', 'ZTAPITestWorker');
    assert.deepEqual(await invokeMacEngine({ host, state: root, bundleId: 'com.ztapi.workbench.engine',
      mode: 'probe', timeoutMs: 15000 }), { summary: '', frames: 0 });
    const target = path.join(root, 'project');
    await fs.mkdir(target);
    await fs.writeFile(path.join(target, 'fixture.test.mjs'), 'import test from "node:test"; test("fixture", () => {});');
    const ports = await createMacProjectPorts({ broker: path.join(native, 'ZTAPIProjectBroker'), worker,
      state: root, diff: () => '' });
    const task = new MacProjectTask({ source: target, accountId: 1, confirm: () => false }, ports);
    assert.deepEqual(task.list(), ['fixture.test.mjs']);
    assert.equal((await task.runTest('fixture.test.mjs')).exitCode, 0);
    console.log('ZTAPI_DMG_DESKTOP_FIXTURE_OK');
  } finally {
    window.destroy();
    await fs.rm(root, { recursive: true, force: true });
  }
  app.exit(0);
}).catch(error => { console.error(error); app.exit(1); });
