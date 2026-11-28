import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

test('financial CI mandates new immutable Xcloud catalog and persistent budget regressions', () => {
  const workflow = readFileSync('.github/workflows/ztapi-financial-ci.yml', 'utf8');
  assert.match(workflow, /name: Verify 20261128 Xcloud catalog publication and finance/);
  assert.match(workflow, /go test \.\/model \.\/controller \.\/finance \.\/relay\/helper \.\/relay\/common -run '[^'\n]*20261128[^'\n]*FinanceReaderLoadsBothImmutableXcloudGenerations[^'\n]*' -count=1 -timeout=5m/);
  assert.match(workflow, /node --test tests\/compatibility\/budget-journal\.test\.mjs tests\/contracts\/xcloud-catalog-20261128-ci\.test\.mjs/);
  assert.match(workflow, /name: Verify isolated Xcloud temporary credential operator/);
  assert.match(workflow, /go test \.\/cmd\/ztapi-xcloud-acceptance-20261128 -count=1 -timeout=3m/);
});
