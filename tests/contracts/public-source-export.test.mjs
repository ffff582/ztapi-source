import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

const repoRoot = path.resolve(import.meta.dirname, '../..');
const exporter = path.join(repoRoot, 'tools/public-source/export.mjs');
const isPublicSnapshot = existsSync(
  path.join(repoRoot, 'PUBLIC-SOURCE-MANIFEST.json'),
);
const privateHistoryOnly = {
  skip: isPublicSnapshot
    ? 'exporter contracts require the private release repository history'
    : false,
};
const retiredProductionWalletSHA256 =
  '1fef8797df6e45adc007100f3624a5c0c4c6dc645720fcd8f33f3d4c1a04d94d';

function git(...args) {
  return execFileSync('git', args, { cwd: repoRoot, encoding: 'utf8' }).trim();
}

function exportSnapshot(commit, env = process.env) {
  const output = mkdtempSync(path.join(tmpdir(), 'ztapi-public-source-'));
  const result = spawnSync(
    process.execPath,
    [exporter, '--commit', commit, '--output', output],
    { cwd: repoRoot, encoding: 'utf8', env },
  );
  return { output, result };
}

test('exports identical bytes regardless of the caller line-ending settings', privateHistoryOnly, () => {
  const commit = git('rev-parse', 'HEAD');
  const gitConfigEnv = (autocrlf, eol) => ({
    ...process.env,
    GIT_CONFIG_COUNT: '2',
    GIT_CONFIG_KEY_0: 'core.autocrlf',
    GIT_CONFIG_VALUE_0: autocrlf,
    GIT_CONFIG_KEY_1: 'core.eol',
    GIT_CONFIG_VALUE_1: eol,
  });
  const withWindowsEndings = exportSnapshot(
    commit,
    gitConfigEnv('true', 'crlf'),
  );
  const withUnixEndings = exportSnapshot(
    commit,
    gitConfigEnv('false', 'lf'),
  );

  try {
    assert.equal(
      withWindowsEndings.result.status,
      0,
      withWindowsEndings.result.stderr || withWindowsEndings.result.stdout,
    );
    assert.equal(
      withUnixEndings.result.status,
      0,
      withUnixEndings.result.stderr || withUnixEndings.result.stdout,
    );
    assert.equal(
      readFileSync(
        path.join(withWindowsEndings.output, 'PUBLIC-SOURCE-MANIFEST.json'),
        'utf8',
      ),
      readFileSync(
        path.join(withUnixEndings.output, 'PUBLIC-SOURCE-MANIFEST.json'),
        'utf8',
      ),
    );
  } finally {
    rmSync(withWindowsEndings.output, { recursive: true, force: true });
    rmSync(withUnixEndings.output, { recursive: true, force: true });
  }
});

test('exports a buildable public snapshot from an exact commit', privateHistoryOnly, () => {
  const commit = git('rev-parse', 'HEAD');
  const { output, result } = exportSnapshot(commit);

  try {
    assert.equal(result.status, 0, result.stderr || result.stdout);

    for (const relativePath of [
      'package.json',
      'pnpm-lock.yaml',
      'server/go.mod',
      'server/LICENSE',
      'server/NOTICE',
      'server/THIRD-PARTY-LICENSES.md',
      'LICENSE',
      'NOTICE',
      'THIRD-PARTY-LICENSES.md',
      'server/Dockerfile.ztapi',
      'deploy/docker/docker-compose.prod.yml',
      'deploy/nginx/ztapi.conf',
      '.github/workflows/ztapi-financial-ci.yml',
      '.github/workflows/ztapi-deploy.yml',
      'README.md',
      'MODIFICATIONS.md',
      'SOURCE-OFFER.md',
      'PUBLIC-SOURCE-MANIFEST.json',
    ]) {
      assert.equal(existsSync(path.join(output, relativePath)), true, relativePath);
    }

    for (const relativePath of [
      '.git',
      '.superpowers',
      'docs/operations',
      'docs/superpowers',
      'test-results',
      '.env',
    ]) {
      assert.equal(existsSync(path.join(output, relativePath)), false, relativePath);
    }

    const offer = readFileSync(path.join(output, 'SOURCE-OFFER.md'), 'utf8');
    assert.match(offer, new RegExp(commit));
    assert.match(offer, new RegExp(`production-${commit}`));
    assert.match(offer, /github\.com\/ffff582\/ztapi-source/);

    const readme = readFileSync(path.join(output, 'README.md'), 'utf8');
    const stubIndex = readme.indexOf('mkdir -p web/default/dist web/classic/dist');
    const goBuildIndex = readme.indexOf('go build ./...');
    assert.notEqual(stubIndex, -1, 'README must create embedded web stubs');
    assert.notEqual(goBuildIndex, -1, 'README must document the Go build');
    assert.ok(stubIndex < goBuildIndex, 'embedded web stubs must precede go build');
    assert.match(readme, /Bun 1\.3\.14/);
    assert.match(readme, /docker build[\s\S]*deploy\/nginx\/Dockerfile/);

    const compose = readFileSync(
      path.join(output, 'deploy/docker/docker-compose.prod.yml'),
      'utf8',
    );
    assert.match(
      compose,
      /USDT_TRC20_RECEIVING_ADDRESS:\s*\$\{ZTAPI_USDT_RECEIVING_ADDRESS:\?[^}]+\}/,
      'public source must require the deployment-provided wallet address',
    );
    assert.doesNotMatch(
      compose,
      /USDT_TRC20_RECEIVING_ADDRESS:\s*["']?T[1-9A-HJ-NP-Za-km-z]{33}["']?/,
      'public source must not contain a production wallet literal',
    );

    const manifest = JSON.parse(
      readFileSync(path.join(output, 'PUBLIC-SOURCE-MANIFEST.json'), 'utf8'),
    );
    assert.equal(manifest.schema_version, 1);
    assert.equal(manifest.release_commit, commit);
    assert.equal(manifest.source_repository, 'https://github.com/ffff582/ztapi-source');
    assert.equal(manifest.source_tag, `production-${commit}`);
    assert.ok(manifest.files.length > 100);
    assert.deepEqual(
      manifest.files.map((file) => file.path),
      [...manifest.files.map((file) => file.path)].sort(),
    );
    assert.ok(
      manifest.files.every(
        (file) =>
          /^[a-f0-9]{64}$/.test(file.sha256) &&
          Number.isInteger(file.size) &&
          file.size >= 0,
      ),
    );
    for (const file of manifest.files) {
      const content = readFileSync(path.join(output, ...file.path.split('/')));
      for (const match of content.toString('utf8').matchAll(/T[1-9A-HJ-NP-Za-km-z]{33}/g)) {
        const digest = createHash('sha256').update(match[0]).digest('hex');
        assert.notEqual(
          digest,
          retiredProductionWalletSHA256,
          `retired production wallet leaked through ${file.path}`,
        );
      }
    }
    assert.equal(
      manifest.files.some((file) => file.path === 'PUBLIC-SOURCE-MANIFEST.json'),
      false,
    );
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});

test('rejects invalid or nonexistent release commits', privateHistoryOnly, () => {
  for (const commit of ['HEAD', 'not-a-commit', '0'.repeat(40)]) {
    const { output, result } = exportSnapshot(commit);
    try {
      assert.notEqual(result.status, 0, commit);
      assert.match(result.stderr, /exact 40-character commit|does not exist/);
    } finally {
      rmSync(output, { recursive: true, force: true });
    }
  }
});

test('derives publication metadata from the immutable commit timestamp', privateHistoryOnly, () => {
  const commits = git(
    'log',
    '--format=%H',
    '--',
    'tools/public-source/templates/SOURCE-OFFER.md',
  ).split(/\r?\n/);
  const commit = commits.at(-1);
  const expectedDate = git('show', '-s', '--format=%cI', commit).slice(0, 10);
  const { output, result } = exportSnapshot(commit);

  try {
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const offer = readFileSync(path.join(output, 'SOURCE-OFFER.md'), 'utf8');
    const modifications = readFileSync(
      path.join(output, 'MODIFICATIONS.md'),
      'utf8',
    );
    assert.match(offer, new RegExp('Publication date: `' + expectedDate + '`'));
    assert.match(modifications, new RegExp('prepared on `' + expectedDate + '`'));
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});

test('renders publication templates from the requested release commit', privateHistoryOnly, () => {
  const commits = git(
    'log',
    '--format=%H',
    '--',
    'tools/public-source/templates/README.md',
  ).split(/\r?\n/);
  const commit = commits.at(-1);
  const releaseDate = git('show', '-s', '--format=%cI', commit).slice(0, 10);
  const sourceTag = `production-${commit}`;
  const sourceRepository = 'https://github.com/ffff582/ztapi-source';
  const releaseTemplate = execFileSync(
    'git',
    ['show', `${commit}:tools/public-source/templates/README.md`],
    { cwd: repoRoot, encoding: 'utf8' },
  );
  const expected = releaseTemplate
    .replaceAll('{{RELEASE_COMMIT}}', commit)
    .replaceAll('{{RELEASE_DATE}}', releaseDate)
    .replaceAll('{{SOURCE_REPOSITORY}}', sourceRepository)
    .replaceAll('{{SOURCE_TAG}}', sourceTag);
  const { output, result } = exportSnapshot(commit);

  try {
    assert.equal(result.status, 0, result.stderr || result.stdout);
    assert.equal(readFileSync(path.join(output, 'README.md'), 'utf8'), expected);
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});

test('refuses to overwrite a non-empty output directory', privateHistoryOnly, () => {
  const output = mkdtempSync(path.join(tmpdir(), 'ztapi-public-source-'));
  const marker = path.join(output, 'keep.txt');
  execFileSync(process.execPath, ['-e', `require('fs').writeFileSync(${JSON.stringify(marker)}, 'keep')`]);

  try {
    const result = spawnSync(
      process.execPath,
      [exporter, '--commit', git('rev-parse', 'HEAD'), '--output', output],
      { cwd: repoRoot, encoding: 'utf8' },
    );
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /output directory must be empty/);
    assert.equal(readFileSync(marker, 'utf8'), 'keep');
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});
