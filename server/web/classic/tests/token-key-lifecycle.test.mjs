import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import test from 'node:test';

import { getCreatedTokenCredential } from '../src/helpers/tokenCreation.js';

const sourceRoot = new URL('../src/', import.meta.url);

async function readSourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const location = new URL(entry.name, directory);
    if (entry.isDirectory()) {
      files.push(
        ...(await readSourceFiles(new URL(`${entry.name}/`, directory))),
      );
    } else if (/\.(js|jsx)$/.test(entry.name)) {
      files.push({
        location,
        source: await readFile(location, 'utf8'),
      });
    }
  }
  return files;
}

test('captures the one-time plaintext from a successful create response', () => {
  const response = {
    data: {
      success: true,
      data: {
        id: 123,
        key: 'sk-zt-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG',
        key_prefix: 'sk-zt-abcde',
      },
    },
  };

  assert.deepEqual(getCreatedTokenCredential(response, 'development'), {
    id: 123,
    name: 'development',
    key: 'sk-zt-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG',
    keyPrefix: 'sk-zt-abcde',
  });
});

test('does not fabricate a credential when create data omits plaintext', () => {
  assert.equal(
    getCreatedTokenCredential(
      {
        data: {
          success: true,
          data: {
            id: 123,
            key_prefix: 'sk-zt-abcde',
          },
        },
      },
      'development',
    ),
    null,
  );
});

test('classic token UI has no post-creation key retrieval path', async () => {
  const sourceFiles = await readSourceFiles(sourceRoot);
  const forbidden = [
    '/api/token/${tokenId}/key',
    '/api/token/batch/keys',
    'fetchTokenKey',
    'fetchTokenKeysBatch',
    'copyTokenKey',
    'copyTokenConnectionString',
    'toggleTokenVisibility',
    'resolvedTokenKeys',
    'loadingTokenKeys',
    'batchCopyTokens',
  ];

  for (const { location, source } of sourceFiles) {
    for (const value of forbidden) {
      assert.equal(
        source.includes(value),
        false,
        `${location.pathname} still contains removed reveal behavior ${value}`,
      );
    }
  }
});

test('classic create flow renders plaintext from the create response once', async () => {
  const modalSource = await readFile(
    new URL(
      '../src/components/table/tokens/modals/EditTokenModal.jsx',
      import.meta.url,
    ),
    'utf8',
  );
  assert.match(modalSource, /getCreatedTokenCredential\(res,/);
  assert.match(modalSource, /createdCredentials/);
  assert.match(modalSource, /credential\.key/);

  const columnSource = await readFile(
    new URL(
      '../src/components/table/tokens/TokensColumnDefs.jsx',
      import.meta.url,
    ),
    'utf8',
  );
  assert.match(columnSource, /record\.key_prefix/);
  assert.doesNotMatch(columnSource, /record\.key\b/);
});

test('legacy embedded chat routes render a stable unavailable state', async () => {
  const [chatSource, chat2LinkSource] = await Promise.all([
    readFile(new URL('../src/pages/Chat/index.jsx', import.meta.url), 'utf8'),
    readFile(
      new URL('../src/pages/Chat2Link/index.jsx', import.meta.url),
      'utf8',
    ),
  ]);
  for (const [route, source] of [
    ['Chat', chatSource],
    ['Chat2Link', chat2LinkSource],
  ]) {
    assert.match(
      source,
      /LegacyEmbeddedChatUnavailable/,
      `${route} must render the explicit legacy-chat unavailable state`,
    );
    assert.doesNotMatch(
      source,
      /useTokenKeys/,
      `${route} must not depend on unavailable stored keys`,
    );
    assert.doesNotMatch(
      source,
      /<Spin\b/,
      `${route} must not present a permanent loading spinner`,
    );
    assert.doesNotMatch(
      source,
      /window\.location\.href/,
      `${route} must not attempt a key-dependent redirect`,
    );
  }

  const unavailableSource = await readFile(
    new URL(
      '../src/components/chat/LegacyEmbeddedChatUnavailable.jsx',
      import.meta.url,
    ),
    'utf8',
  );

  assert.match(unavailableSource, /cannot automatically read API keys/);
  assert.match(
    unavailableSource,
    /Create or use an API key, then configure it in an external client\./,
  );
  assert.match(unavailableSource, /href='\/console\/token'/);
});

test('token batch delete error handling imports showError', async () => {
  const actionsSource = await readFile(
    new URL(
      '../src/components/table/tokens/TokensActions.jsx',
      import.meta.url,
    ),
    'utf8',
  );

  assert.match(
    actionsSource,
    /import\s*\{\s*showError\s*\}\s*from\s*'\.\.\/\.\.\/\.\.\/helpers';/,
  );
  assert.match(actionsSource, /showError\(t\(/);
});
