import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../..', import.meta.url));
const manifestPath = `${root}/server/model/ztapi_quotation_v1.json`;
const pricingFixturePath =
  `${root}/server/model/testdata/ztapi_public_pricing_baseline_v1.json`;
const quotationSHA256 =
  '3671d5d915b222177b584b19c777712f4f9ebbd6497c514cf8fd580115cb56b3';
const pricingFixtureSHA256 =
  '02554b4ae5674586aeadab82c22ef607ba30ca93335e62f95a00aea762360997';
const mediaLabels = [
  'gp-image-2',
  'gm25-fl-IMAGE',
  'seedance-2.0',
  'Seedance 2.0 Fast',
  'Seedance 2.0 Mini',
];

function sha256(content) {
  return createHash('sha256').update(content).digest('hex');
}

test('freezes the quotation and current publication baseline', () => {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  const pricingFixtureRaw = readFileSync(pricingFixturePath);
  const pricingFixture = JSON.parse(pricingFixtureRaw.toString('utf8'));

  assert.equal(manifest.sha256, quotationSHA256);
  assert.equal(manifest.entries.length, 46);
  assert.equal(sha256(pricingFixtureRaw), pricingFixtureSHA256);
  assert.equal(pricingFixture.length, 39);
  assert.equal(
    new Set(pricingFixture.map(({ pricing_version: version }) => version)).size,
    39,
  );
  assert.deepEqual(
    ['zt-glm-5.2', 'zt-gpt-5.6-terra'].filter(
      (modelName) => !pricingFixture.some(({ model_name: current }) => current === modelName),
    ),
    [],
  );
  const publishedModalities = pricingFixture.map((record) =>
    manifest.entries.find(
      ({ public_name: publicName }) => publicName === record.model_name,
    )?.modality,
  );
  assert.equal(publishedModalities.filter((value) => value === 'text').length, 37);
  assert.equal(
    publishedModalities.filter((value) => value === 'embedding').length,
    2,
  );
  for (const record of pricingFixture) {
    assert.match(record.pricing_version, /^ztapi-snapshot-\d+$/);
    assert.equal(
      manifest.entries.some(
        ({ public_name: publicName }) => publicName === record.model_name,
      ),
      true,
      record.model_name,
    );
  }
});

test('maps both evidenced image rows while video rows remain pending', () => {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  const media = manifest.entries
    .filter(({ modality }) => modality === 'image' || modality === 'video')
    .map(({ label, status }) => ({ label, status }));

  assert.deepEqual(
    media.map(({ label }) => label),
    mediaLabels,
  );
  assert.deepEqual(media.map(({ label, status }) => `${label}\0${status}`).sort(), [
    'gp-image-2\0mapped',
    'gm25-fl-IMAGE\0mapped',
    'seedance-2.0\0mapping_pending',
    'Seedance 2.0 Fast\0mapping_pending',
    'Seedance 2.0 Mini\0mapping_pending',
  ].sort());
  const image = manifest.entries.find(({ label }) => label === 'gp-image-2');
  assert.deepEqual(
    {
      source_model: image.source_model,
      public_name: image.public_name,
      protocol: image.protocol,
      provider_family: image.provider_family,
    },
    {
      source_model: 'gpt-image-2',
      public_name: 'zt-gp-image-2',
      protocol: 'openai_compatible',
      provider_family: 'openai',
    },
  );
  const geminiImage = manifest.entries.find(({ label }) => label === 'gm25-fl-IMAGE');
  assert.deepEqual(
    {
      source_model: geminiImage.source_model,
      public_name: geminiImage.public_name,
      protocol: geminiImage.protocol,
      provider_family: geminiImage.provider_family,
    },
    {
      source_model: 'gemini-2.5-flash-image',
      public_name: 'zt-gemini-2.5-flash-image',
      protocol: 'openai_compatible',
      provider_family: 'google',
    },
  );
});
