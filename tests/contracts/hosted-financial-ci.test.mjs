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
	const tools = media.steps.find((step) => step.name === 'Run hosted tool billing and receipt regressions');
	assert.ok(tools && !tools.if);
	assert.match(tools.run, /go test \.\/relay\/\.\.\. \.\/controller \.\/service \.\/types \.\/hostedsearch/);
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

test('financial CI requires the Workbench authorization and settlement regressions', () => {
  const workflow = YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml', 'utf8'));
  const step = workflow.jobs['media-commercial-gates'].steps.find(item => item.name === 'Run Workbench authorization and settlement regressions');
  assert.ok(step && !step.if && !step['continue-on-error']);
  assert.equal(step['working-directory'], 'server');
  assert.match(step.run, /go test \.\/model \.\/service/);
  assert.match(step.run, /go test \.\/router/);
  for (const suite of ['TestZTAPIWorkbench', 'TestWorkbenchReleaseDisabled', 'WithWebFallback', 'FirstPartyRouter', 'FablePilotHardCap', 'NativeRelayLoopback', 'AcceptanceSelectedChannelRejectsDrift']) {
    assert.ok(step.run.includes(suite));
  }
});
