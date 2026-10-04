import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

const workflowPath = '.github/workflows/ztapi-financial-ci.yml';

function loadMediaGateJob() {
  const workflow = YAML.parse(readFileSync(workflowPath, 'utf8'));
  const job = workflow.jobs['media-commercial-gates'];
  assert.ok(job, 'media commercial delivery needs a mandatory real MySQL, real Redis and browser gate');
  return job;
}

test('media commercial CI uses isolated cached MySQL and password-protected Redis', () => {
  const job = loadMediaGateJob();
  assert.deepEqual(job['runs-on'], ['self-hosted', 'ztapi-local']);
  const start = job.steps.find((step) => step.run?.includes('start mysql ztapi_media_ci_only ztapi_commercial_test'));
  assert.ok(start, 'the real MySQL service must use its isolated database');
  assert.match(start.run, /start redis ztapi_media_ci_only/);
  const helper = readFileSync('tools/ci-local-service.sh', 'utf8');
  assert.match(helper, /image=mysql:8\.4/);
  assert.match(helper, /image=redis:7\.4-alpine/);
  assert.match(helper, /docker run --pull=never/);
  assert.match(helper, /redis-server --requirepass/);
  assert.ok(job.steps.some((step) => step.if === 'always()' && step.run?.includes('stop redis')));
  assert.notEqual(job['continue-on-error'], true);
});

test('media commercial CI makes database and Redis integration suites non-skippable', () => {
  const job = loadMediaGateJob();
  const commands = job.steps
    .filter((step) => step.run)
    .map((step) => step.run)
    .join('\n');
  const mysqlStep = job.steps.find((step) => step.run?.includes('TestZTAPICommercialMySQL'));
  const redisStep = job.steps.find((step) => step.run?.includes('TestZTAPIRealRedisAuthAdmissionIsolation'));

  assert.ok(mysqlStep, 'run the real MySQL commercial media race suite');
  assert.equal(String(mysqlStep.env.ZTAPI_REQUIRE_COMMERCIAL_MYSQL), 'true');
  assert.match(mysqlStep.env.ZTAPI_COMMERCIAL_MYSQL_TEST_DSN, /\/ztapi_commercial_test\?/);
  assert.ok(redisStep, 'run the real password-protected Redis isolation suite');
  assert.equal(String(redisStep.env.ZTAPI_REQUIRE_REDIS_INTEGRATION), 'true');
  assert.match(redisStep.env.ZTAPI_REDIS_TEST_URL, /^redis:\/\/:ztapi_media_ci_only@127\.0\.0\.1:6379\/13$/);
  assert.match(commands, /go test \.\/model .*\^TestZTAPICommercialMySQL/);
  assert.match(commands, /go test \.\/router .*\^TestZTAPIRealRedisAuthAdmissionIsolation/);
  assert.doesNotMatch(commands, /\|\|\s*true/);
  for (const step of job.steps) assert.notEqual(step['continue-on-error'], true);
});

test('media commercial CI runs offline UI suites and the responsive browser acceptance', () => {
  const job = loadMediaGateJob();
  const commands = job.steps
    .filter((step) => step.run)
    .map((step) => step.run)
    .join('\n');

  assert.match(commands, /pnpm --filter @ztapi\/console test -- --run/);
  assert.match(commands, /pnpm --dir server\/web\/classic test:ztapi:run/);
  assert.ok(
    job.steps.some((step) => step.uses === 'oven-sh/setup-bun@v2'),
    'classic UI acceptance must install with the same Bun toolchain used by the production image',
  );
  assert.match(commands, /bun install --frozen-lockfile --filter react-template/);
  const prepareEmbed = commands.indexOf('mkdir -p web/default/dist web/classic/dist');
  const offlineGo = commands.indexOf("go test ./... -run 'ZTAPI.*(Media|Image|Video)'");
  assert.ok(prepareEmbed >= 0 && offlineGo > prepareEmbed, 'Go embed placeholders must exist before a clean-root package test');
  assert.match(commands, /pnpm lint:web/);
  assert.match(commands, /npm run test:runtime/);
	assert.match(commands, /node --test tests\/production\/ztapi-reasoning-matrix\.test\.mjs/);
	assert.match(commands, /node --test tests\/production\/ztapi-codex-protocol\.test\.mjs/);
  assert.match(commands, /pnpm exec playwright test tests\/e2e\/media-models-and-guide\.spec\.ts/);
  assert.match(commands, /playwright install chromium/);
  assert.doesNotMatch(commands, /playwright install --with-deps chromium/);
  assert.doesNotMatch(commands, /yunxinapi|api[_-]?key|upstream[_-]?key/i);
});
