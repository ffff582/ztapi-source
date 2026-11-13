import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
async function fingerprint(file) {
  const bytes = await readFile(path.join(repo, file));
  return {
    path: file,
    bytes: bytes.length,
    sha256: sha256(bytes),
    test: /\.test\./.test(file),
  };
}
if (process.argv[2] === '--check') {
  const manifest = JSON.parse(await readFile(process.argv[3], 'utf8'));
  for (const file of manifest.files)
    assert.equal(
      (await fingerprint(file.path)).sha256,
      file.sha256,
      `frozen UI changed: ${file.path}`,
    );
  console.log(
    JSON.stringify({
      unchanged: true,
      files: manifest.files.length,
      aggregate_sha256: manifest.aggregate_sha256,
    }),
  );
} else {
  const output = path.resolve(process.argv[2]);
  const files = [
    'web/console/src/api/support.ts',
    'web/console/src/api/support.test.ts',
    'web/console/src/app/router.tsx',
    'web/console/src/app/router.test.tsx',
    'web/console/src/components/layout/ConsoleShell.tsx',
    'web/console/src/components/layout/ConsoleShell.test.tsx',
    'web/console/src/components/layout/WorkbenchShell.tsx',
    'web/console/src/components/layout/WorkbenchShell.test.tsx',
    'web/console/src/components/layout/PublicHeader.tsx',
    'web/console/src/components/layout/PublicHeader.test.tsx',
    'web/console/src/components/layout/public-header.css',
    'server/web/classic/src/ztapi/AdminApp.jsx',
    'server/web/classic/src/ztapi/layout/AdminShell.jsx',
    'server/web/classic/src/ztapi/layout/AdminShell.test.jsx',
  ];
  for (const folder of [
    'web/console/src/features/support',
    'server/web/classic/src/ztapi/features/support',
  ]) {
    for (const entry of await readdir(path.join(repo, folder), {
      withFileTypes: true,
    })) {
      assert.ok(entry.isFile());
      files.push(`${folder}/${entry.name}`);
    }
  }
  files.sort();
  const fingerprints = [];
  for (const file of files) fingerprints.push(await fingerprint(file));
  const resultsPath = path.join(output, 'results.json');
  const results = await readFile(resultsPath);
  assert.equal(JSON.parse(results).pass, true);
  const manifest = {
    generated_at: new Date().toISOString(),
    repo,
    production_ui_stable: true,
    scope:
      'Only support UI, authorized router/navigation and central public header overlay; no R3 replacement',
    aggregate_sha256: sha256(
      fingerprints.map((file) => `${file.path}\0${file.sha256}\n`).join(''),
    ),
    browser_results: resultsPath,
    browser_results_sha256: sha256(results),
    files: fingerprints,
  };
  const destination = path.join(output, 'ui-freeze-manifest.json');
  await writeFile(destination, JSON.stringify(manifest, null, 2));
  console.log(
    JSON.stringify(
      {
        path: destination,
        files: fingerprints.length,
        production_files: fingerprints.filter((file) => !file.test).length,
        aggregate_sha256: manifest.aggregate_sha256,
      },
      null,
      2,
    ),
  );
}
