const fs = require('node:fs/promises');
const path = require('node:path');
const { app } = require('electron');
const { startWorkbench } = require('./shell.cjs');

if (process.platform !== 'darwin') throw Error('MAC_ENGINE_SANDBOX_NOT_READY');
const profileDirectory = path.join(app.getPath('appData'), 'ZTAPI Workbench macOS Beta');
const state = path.join(profileDirectory, 'engine-state');
let connection;
const connect = () => connection ??= (async () => {
  await fs.mkdir(state, { recursive: true, mode: 0o700 });
  if (await fs.realpath(state) !== state) throw Error('PROFILE_PATH_INVALID');
  const { createMacConnection } = await import('../src/macos-open-code.mjs');
  return createMacConnection(process.resourcesPath, state);
})().catch(error => { connection = undefined; throw error; });

startWorkbench({
  profileDirectory,
  accountFactory: async () => {
    const { createFirstPartyWorkbenchAccount } = await import('../src/workbench-account.mjs');
    return createFirstPartyWorkbenchAccount();
  },
  projectFactory: async options => (await connect()).projectFactory(options),
  projectGatewayFactory: async (...args) => (await connect()).projectGatewayFactory(...args),
  claudeTaskRunner: async options => (await connect()).claudeTaskRunner(options),
});
