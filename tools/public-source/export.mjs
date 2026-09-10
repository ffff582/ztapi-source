import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { EXCLUDED_PATHS, SOURCE_REPOSITORY } from './policy.mjs';

const toolDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(toolDir, '../..');
const templatesDir = path.join(toolDir, 'templates');

function fail(message) {
  process.stderr.write(`${message}\n`);
  process.exit(1);
}

function parseArgs(argv) {
  const result = {};
  for (let index = 0; index < argv.length; index += 2) {
    const flag = argv[index];
    const value = argv[index + 1];
    if (!['--commit', '--output'].includes(flag) || !value) {
      fail('usage: export.mjs --commit <40-character-sha> --output <empty-directory>');
    }
    result[flag.slice(2)] = value;
  }
  if (!result.commit || !result.output) {
    fail('usage: export.mjs --commit <40-character-sha> --output <empty-directory>');
  }
  return result;
}

function verifyCommit(commit) {
  if (!/^[a-f0-9]{40}$/.test(commit)) {
    fail('release must be an exact 40-character commit');
  }
  try {
    execFileSync('git', ['cat-file', '-e', `${commit}^{commit}`], {
      cwd: repoRoot,
      stdio: 'ignore',
    });
  } catch {
    fail(`release commit does not exist: ${commit}`);
  }
}

function prepareOutput(output) {
  mkdirSync(output, { recursive: true });
  if (readdirSync(output).length !== 0) {
    fail('output directory must be empty');
  }
}

function renderTemplate(name, values) {
  let content = readFileSync(path.join(templatesDir, name), 'utf8');
  for (const [key, value] of Object.entries(values)) {
    content = content.replaceAll(`{{${key}}}`, value);
  }
  return content;
}

function listFiles(root, current = root) {
  const result = [];
  for (const entry of readdirSync(current, { withFileTypes: true })) {
    const absolute = path.join(current, entry.name);
    if (entry.isDirectory()) {
      result.push(...listFiles(root, absolute));
    } else if (entry.isFile()) {
      const relative = path.relative(root, absolute).replaceAll('\\', '/');
      if (relative !== 'PUBLIC-SOURCE-MANIFEST.json') result.push(relative);
    }
  }
  return result.sort();
}

function createManifest(output, commit, sourceTag) {
  const files = listFiles(output).map((relativePath) => {
    const absolute = path.join(output, ...relativePath.split('/'));
    const content = readFileSync(absolute);
    return {
      path: relativePath,
      sha256: createHash('sha256').update(content).digest('hex'),
      size: statSync(absolute).size,
    };
  });
  return {
    schema_version: 1,
    release_commit: commit,
    source_repository: SOURCE_REPOSITORY,
    source_tag: sourceTag,
    files,
  };
}

const args = parseArgs(process.argv.slice(2));
verifyCommit(args.commit);
const output = path.resolve(args.output);
prepareOutput(output);

const scratch = mkdtempSync(path.join(tmpdir(), 'ztapi-source-export-'));
const archive = path.join(scratch, 'source.tar');

try {
  execFileSync('git', ['archive', '--format=tar', '--output', archive, args.commit], {
    cwd: repoRoot,
    stdio: 'inherit',
  });
  execFileSync('tar', ['-xf', archive, '-C', output], { stdio: 'inherit' });

  for (const excluded of EXCLUDED_PATHS) {
    rmSync(path.join(output, ...excluded.split('/')), { recursive: true, force: true });
  }

  const sourceTag = `production-${args.commit}`;
  const releaseDate = execFileSync(
    'git',
    ['show', '-s', '--format=%cI', args.commit],
    { cwd: repoRoot, encoding: 'utf8' },
  ).trim().slice(0, 10);
  const values = {
    RELEASE_COMMIT: args.commit,
    RELEASE_DATE: releaseDate,
    SOURCE_REPOSITORY,
    SOURCE_TAG: sourceTag,
  };
  for (const name of ['README.md', 'MODIFICATIONS.md', 'SOURCE-OFFER.md']) {
    writeFileSync(path.join(output, name), renderTemplate(name, values), 'utf8');
  }

  for (const name of ['LICENSE', 'NOTICE', 'THIRD-PARTY-LICENSES.md']) {
    copyFileSync(path.join(output, 'server', name), path.join(output, name));
  }

  const manifest = createManifest(output, args.commit, sourceTag);
  writeFileSync(
    path.join(output, 'PUBLIC-SOURCE-MANIFEST.json'),
    `${JSON.stringify(manifest, null, 2)}\n`,
    'utf8',
  );
  process.stdout.write(`${JSON.stringify({ output, ...manifest, files: manifest.files.length })}\n`);
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
