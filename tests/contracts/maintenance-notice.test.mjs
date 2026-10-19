import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const entry = readFileSync(new URL('../../web/console/index.html', import.meta.url), 'utf8');

test('homepage maintenance extension does not promise an expired recovery time', () => {
  assert.match(entry, /id="ztapi-maintenance-notice"/);
  assert.match(entry, /role="status"/);
  assert.match(entry, /维护延长/);
  assert.match(entry, /恢复时间另行通知/);
  assert.doesNotMatch(entry, /2026-10-19T10:18|10 月 19 日上午 10:18/);
});

test('temporary notice is homepage-only, persistent, and independently removable', () => {
  assert.equal(entry.split('<!-- ZTAPI_MAINTENANCE_NOTICE_START -->').length - 1, 1);
  assert.equal(entry.split('<!-- ZTAPI_MAINTENANCE_NOTICE_END -->').length - 1, 1);
  const notice = entry.split('<!-- ZTAPI_MAINTENANCE_NOTICE_START -->')[1]?.split('<!-- ZTAPI_MAINTENANCE_NOTICE_END -->')[0] ?? '';
  assert.match(notice, /body:has\(\.public-home\)/);
  assert.doesNotMatch(notice, /<script|setTimeout|localStorage|<button/i);
  assert.match(entry, /<div id="root"><\/div>/);
  assert.match(entry, /src="\/src\/main\.tsx"/);
});
