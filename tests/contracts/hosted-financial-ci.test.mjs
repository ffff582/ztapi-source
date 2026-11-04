import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

test('public financial CI uses standard hosted runners without paid artifact storage', () => {
  const workflow = YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml', 'utf8'));
  const selector = '${{ github.repository == \'ffff582/ztapi-source\' && \'ubuntu-24.04\' || fromJSON(\'["self-hosted","ztapi-local"]\') }}';
  assert.equal(workflow.permissions.contents, 'read');
  assert.equal(Object.keys(workflow.jobs).length, 5);
  for (const job of Object.values(workflow.jobs)) assert.equal(job['runs-on'], selector);
  assert.equal(workflow.env.ZTAPI_CI_ALLOW_IMAGE_PULL, '${{ github.repository == \'ffff582/ztapi-source\' }}');
  const media = workflow.jobs['media-commercial-gates'];
  const upload = media.steps.find((step) => step.uses?.startsWith('actions/upload-artifact@'));
  assert.equal(upload.if, "failure() && github.repository != 'ffff582/ztapi-source'");
  for (const key of ['Run production runtime configuration tests', 'Run responsive media browser acceptance']) {
    const step = media.steps.find((item) => item.name === key);
    assert.ok(step && !step.if);
  }
  const helper = readFileSync('tools/ci-local-service.sh', 'utf8');
  assert.match(helper, /ZTAPI_CI_ALLOW_IMAGE_PULL/);
  assert.match(helper, /docker pull "\$1"/);
  assert.match(helper, /--pull=never/);
});
