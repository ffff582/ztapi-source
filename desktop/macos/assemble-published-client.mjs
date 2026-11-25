import { createHash } from 'node:crypto';
import { lstat, mkdir, readFile, realpath, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const manifestSha = 'fef54bb6c0d3cdfccd0e1fbf70a6a85fdbd9042751a059da7d1b06297e06d79f';
const digest = data => createHash('sha256').update(data).digest('hex');
const baseline = () => Error('MAC_CLIENT_BASELINE_REJECTED');
const drift = () => Error('MAC_CLIENT_SOURCE_DRIFT');

export function replaceOnce(text, before, after) {
  const start = text.indexOf(before);
  if (start < 0 || text.indexOf(before, start + 1) !== -1) throw drift();
  return text.slice(0, start) + after + text.slice(start + before.length);
}

async function file(root, name, sha) {
  const target = join(root, ...name.split('/'));
  const stat = await lstat(target);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 2 * 1024 * 1024 ||
      await realpath(target) !== target) throw baseline();
  const bytes = await readFile(target);
  if (digest(bytes) !== sha) throw baseline();
  return bytes;
}

function applyOverlay(files, name, patches) {
  let code = files.get(name)?.toString('utf8').replaceAll('\r\n', '\n');
  if (typeof code !== 'string') throw drift();
  for (const [before, after] of patches) code = replaceOnce(code, before, after);
  files.set(name, Buffer.from(code));
}

export async function assemblePublishedClient(input, output) {
  const source = await realpath(input);
  const published = JSON.parse((await file(source, 'MANIFEST.json', manifestSha)).toString('utf8'));
  if (published.release !== true || published.kind !== 'ztapi-opencode-windows-platform-beta' ||
      published.channel !== 'platform-beta') throw baseline();
  const files = new Map();
  for (const [name, sha] of Object.entries(published.files)) {
    const allowed = /^app\/(desktop\/[a-z0-9-]+\.(?:cjs|js|css|html)|src\/(?:[a-z0-9-]+\.mjs|review-diff-vendor\/(?:diff\.min\.js|LICENSE)))$/;
    if (allowed.test(name)) files.set(name.slice(4), await file(source, name, sha));
  }
  if (files.size !== 29) throw baseline();
  applyOverlay(files, 'desktop/shell.cjs', [
    ['claudeTaskRunner, profileDirectory, acceptance', 'claudeTaskRunner, projectFactory, profileDirectory, acceptance'],
    ['const { NativeProjectTask } = await import(', 'const { NativeProjectTask } = projectFactory ? {} : await import('],
    ['function openProject(source, restore)', 'async function openProject(source, restore)'],
    ['return new NativeProjectTask({ source, state: projectState, accountId, restore, confirm:', 'const options = { source, state: projectState, accountId, restore, confirm:'],
    ['return result.response === 1;\n      } });', 'return result.response === 1;\n      } };\n      return projectFactory ? projectFactory(options) : new NativeProjectTask(options);'],
    ['const chosen = openProject(savedSession.project.root, savedSession.project);', 'const chosen = await openProject(savedSession.project.root, savedSession.project);'],
    ['const chosen = openProject(source);', 'const chosen = await openProject(source);'],
  ]);
  applyOverlay(files, 'src/desktop-project-runner.mjs', [
    ['function runOpenCodeProject({ launch, gateway', 'function runOpenCodeProject({ launch, invokeEngine = invokeSandbox, gateway'],
    ['const invoke = (input, options) => invokeSandbox(', 'const invoke = (input, options) => invokeEngine('],
  ]);
  applyOverlay(files, 'desktop/project-diagnostics.cjs', [
    ['  CLAUDE_SANDBOX_NOT_READY\n', '  CLAUDE_SANDBOX_NOT_READY\n  MAC_ENGINE_SANDBOX_NOT_READY MAC_ENGINE_OUTPUT_INVALID MAC_ENGINE_RUN_FAILED\n  MAC_ENGINE_PROJECT_TOOL_FAILED MAC_ENGINE_PROJECT_RPC_REJECTED MAC_ENGINE_MODEL_REJECTED\n'],
  ]);
  applyOverlay(files, 'desktop/ui.js', [
    ["select.type = 'checkbox';", "select.type = 'radio';\n    select.name = 'review-file';"],
    ['  REVIEW_FILE_LIMIT:', "  MAC_ENGINE_SANDBOX_NOT_READY: '\u004d\u0061\u0063 隔离引擎未就绪，本轮未发送任务。',\n  MAC_ENGINE_RUN_FAILED: '隔离引擎已停止，不会自动重试或写回。',\n  REVIEW_FILE_LIMIT:"],
    ['一次最多批准 100 个文件。', '这个 Mac 内测版一次只能批准一个文件。'],
  ]);
  for (const [name, original] of [
    ['desktop/open-code-macos.cjs', 'desktop-client-entry.cjs'],
    ['src/macos-open-code.mjs', 'desktop-client-connection.mjs'],
  ]) files.set(name, await readFile(new URL(original, import.meta.url)));

  files.set('package.json', Buffer.from(JSON.stringify({
    name: 'ztapi-workbench-macos-beta', version: '0.30.0-beta.1', private: true, main: 'desktop/open-code-macos.cjs',
  }, null, 2) + '\n'));
  await mkdir(output);
  for (const [name, data] of files) {
    const target = join(output, ...name.split('/'));
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, data, { flag: 'wx' });
  }
  return files.size;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 4) throw Error('usage: assemble-published-client.mjs PAYLOAD NEW_APP');
  console.log(`Reconstructed ${await assemblePublishedClient(process.argv[2], process.argv[3])} verified client files`);
}
