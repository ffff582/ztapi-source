export const SOURCE_REPOSITORY = 'https://github.com/ffff582/ztapi-source';

export const EXCLUDED_PATHS = [
  '.git',
  '.playwright-cli',
  '.superpowers',
  '.tmp',
  'audit-evidence',
  'docs/operations',
  'docs/superpowers',
  'test-results',
];

export function isExcluded(relativePath) {
  const normalized = relativePath.replaceAll('\\', '/').replace(/^\.\//, '');
  return EXCLUDED_PATHS.some(
    (excluded) => normalized === excluded || normalized.startsWith(`${excluded}/`),
  );
}
