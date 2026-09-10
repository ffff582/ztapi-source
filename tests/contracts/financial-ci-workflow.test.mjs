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
  assert.equal(job.services.mysql.image, 'mysql:8.4');
  assert.equal(job.services.mysql.env.MYSQL_DATABASE, 'ztapi_test');
  assert.match(job.services.mysql.options, /mysqladmin ping/);
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
