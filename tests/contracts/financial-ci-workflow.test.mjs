import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

const workflowPath = '.github/workflows/ztapi-financial-ci.yml';

test('financial CI executes the MySQL balance and USDT integration suites explicitly', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const workflow = YAML.parse(source);
  const job = workflow.jobs['financial-integrity'];

  assert.equal(workflow.permissions.contents, 'read');
  assert.equal(job['runs-on'], '${{ github.repository == \'ffff582/ztapi-source\' && \'ubuntu-24.04\' || fromJSON(\'["self-hosted","ztapi-local"]\') }}');
  assert.ok(job.steps.some((step) => step.run?.includes('start mysql ztapi_test_root_2026 ztapi_test')));
  assert.match(readFileSync('tools/ci-local-service.sh', 'utf8'), /mysqladmin ping/);
  assert.match(source, /ZTAPI_BALANCE_LEDGER_MYSQL_TEST_DSN/);
  assert.match(source, /ZTAPI_USDT_MYSQL_TEST_DSN/);
  assert.match(source, /ZTAPI_REQUIRE_MYSQL_INTEGRATION:\s*true/);
  assert.match(source, /ztapi_test/);
  assert.match(source, /TestBalanceLedgerMySQLIntegration/);
  assert.match(source, /TestUSDT\.\*MySQL/);
	assert.match(source, /go test \.\/service -run '\^TestWalletFundingLedgerMySQLIntegration\$'/);
  assert.match(source, /-count=1/);
  assert.doesNotMatch(source, /ZTAPI_BALANCE_LEDGER_MYSQL_TEST_CONFIRM/);
});

test('financial CI requires the Workbench migration on a fresh isolated MySQL database', () => {
  const workflow = YAML.parse(readFileSync(workflowPath, 'utf8'));
  const steps = workflow.jobs['financial-integrity'].steps;
  const index = steps.findIndex(step => step.name === 'Run Workbench MySQL upgrade and concurrency');
  assert.ok(index > steps.findIndex(step => step.name === 'Run MySQL financial integrity tests'));
  const step = steps[index];
  assert.equal(step['working-directory'], 'server');
  assert.equal(step.if, undefined);
  assert.notEqual(step['continue-on-error'], true);
  assert.deepEqual(step.env, { ZTAPI_R36_TEST_MYSQL_ADDR: '127.0.0.1:3306', ZTAPI_R36_TEST_MYSQL_UPGRADE_FIXTURE: '1' });
  assert.match(step.run, /ci-local-service\.sh stop mysql[\s\S]*ci-local-service\.sh start mysql "\$ZTAPI_R36_TEST_MYSQL_PASSWORD" r36_acceptance[\s\S]*go test \.\/model -run '\^TestZTAPIWorkbenchDatabaseConcurrency\$\/\^mysql\$'/);
  assert.ok(steps.slice(index + 1).some(step => step.if === 'always()' && step.run?.includes('ci-local-service.sh stop mysql')));
});
