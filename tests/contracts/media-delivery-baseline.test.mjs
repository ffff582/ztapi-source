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
  '176a925a50e729714653b9a010289a5bff64ae7edcaa4bb3b01411785f9610bb';
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
  assert.equal(pricingFixture.length, 37);
  assert.equal(
    new Set(pricingFixture.map(({ pricing_version: version }) => version)).size,
    37,
  );
  const publishedModalities = pricingFixture.map((record) =>
    manifest.entries.find(
      ({ public_name: publicName }) => publicName === record.model_name,
    )?.modality,
  );
  assert.equal(publishedModalities.filter((value) => value === 'text').length, 35);
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

test('keeps all five quoted media rows blocked', () => {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  const media = manifest.entries
    .filter(({ modality }) => modality === 'image' || modality === 'video')
    .map(({ label, status }) => ({ label, status }));

  assert.deepEqual(
    media.map(({ label }) => label),
    mediaLabels,
  );
  assert.deepEqual(
    media
      .map(({ label, status }) => `${label}\0${status}`)
      .sort(),
    mediaLabels.map((label) => `${label}\0mapping_pending`).sort(),
  );
});
