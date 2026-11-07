import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

test('referral lifecycle and both real frontend component harnesses are mandatory',()=>{
 const workflow=YAML.parse(readFileSync('.github/workflows/ztapi-financial-ci.yml','utf8'));
 const steps=workflow.jobs['media-commercial-gates'].steps;
 const lifecycle=steps.find(s=>s.name==='Run referral financial lifecycle');
 assert.ok(lifecycle&&!lifecycle.if&&!lifecycle['continue-on-error']);
 for(const pkg of ['./common','./model','./service','./controller','./finance'])assert.ok(lifecycle.run.includes(pkg));
 assert.ok(lifecycle.run.includes('TestZTAPIReferral|TestReferral'));
 const browser=steps.find(s=>s.name==='Run referral user and finance browser acceptance');
 assert.ok(browser&&!browser.if&&!browser['continue-on-error']);
 assert.ok(browser.run.includes('web/console/src/features/referrals/ReferralPage.browser.mjs'));
 assert.ok(browser.run.includes('server/web/classic/src/ztapi/features/finance/ReferralWithdrawalsPage.browser.test.mjs'));
 assert.ok(steps.indexOf(browser)>steps.findIndex(s=>s.name==='Install Chromium'));
 const mysql=steps.find(s=>String(s.run).includes("'^TestZTAPICommercialMySQL'"));
 assert.ok(mysql&&!mysql.if&&!mysql['continue-on-error']);
});

test('referral migration is explicit and cannot bootstrap the business database',()=>{
 const source=readFileSync('server/cmd/ztapi-referral-migrate/main.go','utf8');
 assert.ok(source.includes('--apply-new-referral-tables'));
 assert.ok(source.includes('model.MigrateZTAPIReferral(db)'));
 assert.ok(!source.includes('model.InitDB(')&&!source.includes('model.InitOptionMap('));
 const routes=readFileSync('server/router/api-router.go','utf8');
 const campaign=routes.split('\n').find(s=>s.includes('POST("/referrals/campaign"'));
 assert.ok(campaign.includes('middleware.ZTAPIUserAuth()')&&campaign.includes('middleware.RootAuth()'));
 const payout=routes.split('\n').find(s=>s.includes('POST("/referrals/withdrawals/:id/:action"'));
 assert.ok(payout.includes('middleware.ZTAPIUserAuth()')&&payout.includes('PermissionReferralWrite'));
});
