import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

test('settled replay CI owns an isolated MySQL database and cannot silently skip', () => {
  const workflow = YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml', 'utf8'));
  const job = workflow.jobs['usdt-settled-replay'];
  assert.ok(job, 'a dedicated replay job is required');
  assert.equal(job.services.mysql.image, 'mysql:8.4');
  assert.equal(job.services.mysql.env.MYSQL_DATABASE, 'ztapi_usdt_replay_test');
  assert.match(job.services.mysql.options, /mysqladmin ping/);
  assert.notEqual(job['continue-on-error'], true);
  assert.ok(job.steps.some(step => step.run?.includes('TestUSDTSettledReplay(SyntheticFixture|ConfigurationGate)')),
    'CI must also exercise the fixture and required-configuration failure checks');
  const step = job.steps.find(step => step.run?.includes('TestUSDTAlreadySettledReplayMySQL'));
  assert.ok(step, 'the replay regression must be selected explicitly');
  assert.equal(step['working-directory'], 'server');
  assert.equal(String(step.env.ZTAPI_REQUIRE_USDT_REPLAY_TEST), 'true');
  assert.match(step.env.ZTAPI_USDT_REPLAY_MYSQL_TEST_DSN,
    /@tcp\(127\.0\.0\.1:3306\)\/ztapi_usdt_replay_test\?/);
  assert.match(step.run, /go test .*\.\/model .*\^TestUSDTAlreadySettledReplayMySQL\$/);
  assert.match(step.run, /-count=1/);
  assert.match(step.run, /-timeout[= ]\S+/);
  assert.doesNotMatch(step.run, /\|\|\s*true/);
  assert.notEqual(step['continue-on-error'], true);
  const source = readFileSync('server/model/usdt_settled_replay_mysql_test.go', 'utf8');
  assert.match(source, /func TestUSDTAlreadySettledReplayMySQL\(t \*testing.T\)/);
});

test('settled replay fixture is synthetic and has no external customer-data dependency', () => {
  const source = readFileSync('server/model/usdt_settled_replay_mysql_test.go', 'utf8');
  assert.doesNotMatch(source, /os\.ReadFile|ZTAPI_USDT_REPLAY_FIXTURE/);
  assert.match(source, /syntheticUSDTSettledReplayFixture/);
});

test('phase1 does not advertise a nonexistent or substitute partial acceptance runner', () => {
  const { scripts } = JSON.parse(readFileSync('package.json', 'utf8'));
  assert.equal(scripts['test:phase1'], undefined,
    'no equivalent end-to-end phase1 runner exists; retire the stale alias');
  assert.equal(scripts['test:contracts'], 'node --test tests/contracts/*.test.mjs');
  assert.equal(scripts['test:runtime'], 'node --test tests/runtime/*.test.mjs');
  for (const path of ['tests/repository-layout.test.mjs', 'tests/runtime/production-edge.test.mjs',
    'tests/e2e/usdt-topup.spec.ts']) assert.ok(existsSync(path), path);
});
