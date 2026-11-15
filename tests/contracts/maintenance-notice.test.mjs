import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const entry = readFileSync(new URL('../../web/console/index.html', import.meta.url), 'utf8');

test('resolved temporary maintenance notice is removed from the homepage', () => {
  assert.doesNotMatch(entry, /ztapi-maintenance-notice|ZTAPI_MAINTENANCE_NOTICE|ztapi-notice-height/);
  assert.doesNotMatch(entry, /恢复时间另行通知|2026-10-19T10:18/);
});

test('notice removal preserves the application bootstrap', () => {
  assert.match(entry, /<div id="root"><\/div>/);
  assert.match(entry, /src="\/src\/main\.tsx"/);
});
