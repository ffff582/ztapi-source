const skipped = /^(?:\.env(?:\..*)?|\.envrc|\.direnv|\.git|\.ssh|\.aws|\.azure|\.docker|\.kube|\.config|\.codex|\.runtime-tmp|\.ztapi-write-.*|node_modules|\.venv|venv|dist|target|\.npmrc|\.netrc|\.pypirc|auth\.json|credentials\.json|id_(?:rsa|ecdsa|dsa|ed25519).*|(?:con|nul|prn|aux|com[1-9]|lpt[1-9])(?:\..*)?|.*\.(?:pem|key|p12|pfx|jks|keystore))$/i;
const maxFile = 2 * 1024 * 1024;

function projectName(value) {
  if (typeof value !== 'string' || Buffer.byteLength(value) > 500 || /[\\:\x00-\x1f]/.test(value))
    throw Error('PROJECT_PATH_REJECTED');
  const parts = value.split('/');
  if (parts.length > 32 || parts.some(part => !part || /[. ]$/.test(part) || skipped.test(part)))
    throw Error('PROJECT_PATH_REJECTED');
  return value;
}

export function validateProjectTestInput({ name, files }) {
  if (!projectName(name).endsWith('.test.mjs') || !Array.isArray(files) || files.length > 3000)
    throw Error('PROJECT_TEST_INVALID');
  const folded = new Set();
  let found = false, total = 0;
  const clean = files.map(record => {
    const path = projectName(record?.path);
    const key = path.normalize('NFC').toLowerCase();
    if (folded.has(key) || typeof record.data !== 'string' || record.data.length > Math.ceil(maxFile / 3) * 4)
      throw Error('PROJECT_TEST_INVALID');
    folded.add(key);
    const data = Buffer.from(record.data, 'base64');
    if (data.length > maxFile || data.toString('base64') !== record.data || (total += data.length) > 32 * 1024 * 1024)
      throw Error('PROJECT_TEST_INVALID');
    if (path === name) found = true;
    return { path, data };
  });
  for (const path of folded) {
    for (let i = path.indexOf('/'); i !== -1; i = path.indexOf('/', i + 1)) {
      if (folded.has(path.slice(0, i))) throw Error('PROJECT_PATH_COLLISION');
    }
  }
  if (!found) throw Error('PROJECT_TEST_INVALID');
  return { name, files: clean };
}
