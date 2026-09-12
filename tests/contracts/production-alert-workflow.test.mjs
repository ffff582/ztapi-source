import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

const workflowPath = '.github/workflows/ztapi-production-alert-acceptance.yml';

test('production Telegram acceptance uses the deployed outbox path without exposing secrets', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const workflow = YAML.parse(source);
  const job = workflow.jobs.verify_production_alert;

  assert.ok(workflow.on.workflow_dispatch);
  assert.equal(job.environment, 'production');
  assert.equal(workflow.permissions.contents, 'read');
  assert.match(source, /secrets\.ZTAPI_ADMIN_USERNAME/);
  assert.match(source, /secrets\.ZTAPI_ADMIN_PASSWORD/);
  assert.match(source, /https:\/\/admin\.ztapi\.vip\/api\/auth\/login/);
  assert.match(
    source,
    /https:\/\/admin\.ztapi\.vip\/api\/models\/ztapi\/health\/test-alert/,
  );
  assert.match(source, /\.data\.access_token/);
  assert.match(source, /\.data\.user\.id/);
  assert.match(source, /Authorization: Bearer \$admin_access_token/);
  assert.match(source, /New-API-User: \$admin_user_id/);
  assert.match(source, /for attempt in \$\(seq 1 40\)/);
  assert.match(source, /sleep 5/);
  assert.match(source, /delivery_receipt \| fromjson/);
  assert.match(source, /telegram_message_id/);
  assert.match(source, /telegram_delivery_timestamp/);
  assert.match(source, /actions\/upload-artifact@/);
  assert.doesNotMatch(source, /api\.telegram\.org/);
  assert.doesNotMatch(source, /ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN/);
  assert.doesNotMatch(source, /ZTAPI_HEALTH_TELEGRAM_CHAT_ID/);
  assert.doesNotMatch(source, /\b[0-9]{8,12}:[A-Za-z0-9_-]{20,}\b/);
  assert.doesNotMatch(source, /chat_id/);
  assert.doesNotMatch(source, /set -x/);
});
