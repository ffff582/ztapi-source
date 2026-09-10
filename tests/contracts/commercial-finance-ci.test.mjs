import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

test('commercial finance CI runs mandatory isolated MySQL lifecycle coverage', () => {
  const workflow = YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml', 'utf8'));
  const job = workflow.jobs['commercial-finance'];
  assert.ok(job, 'commercial settlement, attempt billing and supplier refund need a dedicated MySQL job');
  assert.equal(job.services.mysql.image, 'mysql:8.4');
  assert.equal(job.services.mysql.env.MYSQL_DATABASE, 'ztapi_commercial_test');
  assert.notEqual(job['continue-on-error'], true);
  const step = job.steps.find((step) => step.run?.includes('TestZTAPICommercialMySQL'));
  assert.ok(step, 'select the actual commercial lifecycle suite');
  assert.equal(step['working-directory'], 'server');
  assert.equal(String(step.env.ZTAPI_REQUIRE_COMMERCIAL_MYSQL), 'true');
  assert.match(step.env.ZTAPI_COMMERCIAL_MYSQL_TEST_DSN, /@tcp\(127\.0\.0\.1:3306\)\/ztapi_commercial_test\?/);
  assert.match(step.run, /go test .*\.\/model .*\^TestZTAPICommercialMySQL/);
  assert.match(step.run, /-count=1/);
  assert.match(step.run, /-timeout[= ]\S+/);
  assert.doesNotMatch(step.run, /\|\|\s*true/);
  assert.notEqual(step['continue-on-error'], true);
  const source = readFileSync('server/model/ztapi_commercial_finance_mysql_test.go', 'utf8');
  for (const env of Object.keys(step.env)) assert.ok(source.includes(env), `test must consume ${env}`);
  for (const requiredCase of [
    'TestZTAPICommercialMySQLImageIntegration',
    'TestZTAPICommercialMySQLMediaTaskIntegration',
  ]) {
    assert.match(
      source,
      new RegExp(`func ${requiredCase}\\(`),
      `${requiredCase} must remain inside the mandatory commercial MySQL prefix`,
    );
  }
});
