import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import YAML from 'yaml';

test('P5 runtime secrets survive source release replacement and are not required for legacy deployments',()=>{
 const compose=YAML.parse(readFileSync('deploy/docker/docker-compose.prod.yml','utf8'));
 assert.deepEqual(compose.services.server.env_file,[{path:'/etc/ztapi/health.env',required:false}]);
 const environment=compose.services.server.environment;
 for(const key of ['ZTAPI_HEALTH_ENABLED','ZTAPI_HEALTH_PROBE_KEY','ZTAPI_HEALTH_ALERT_WEBHOOK_URL','ZTAPI_HEALTH_SYNTHETIC_PROBES_ENABLED']) assert.equal(environment[key],undefined,'compose must not override protected runtime configuration');
});

test('P5 CI runs the real MySQL test with mandatory isolated database coverage',()=>{
 const workflow=YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml','utf8'));
 const job=workflow.jobs['health-integrity'];
 assert.ok(job,'a separate MySQL health job must exist');
 assert.equal(job.services.mysql.image,'mysql:8.4');
 assert.equal(job.services.mysql.env.MYSQL_DATABASE,'ztapi_health_test');
 const step=job.steps.find(step=>step.env?.ZTAPI_HEALTH_MYSQL_TEST_DSN);
 assert.ok(step,'health integration DSN must be explicit');
 assert.equal(String(step.env.ZTAPI_REQUIRE_HEALTH_MYSQL_INTEGRATION),'true');
 assert.match(step.env.ZTAPI_HEALTH_MYSQL_TEST_DSN,/@tcp\(127\.0\.0\.1:3306\)\/ztapi_health_test\?/);
 assert.match(step.run,/TestZTAPIHealthMySQLDurableConcurrentTrip/);
 assert.match(step.run,/-count=1/);
 const go=readFileSync('server/model/ztapi_health_mysql_test.go','utf8');
 assert.match(go,/ZTAPI_REQUIRE_HEALTH_MYSQL_INTEGRATION/);
 assert.match(go,/t\.Fatal\("mandatory MySQL health integration DSN is missing"\)/);
});
