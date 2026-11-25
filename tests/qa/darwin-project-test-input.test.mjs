import assert from 'node:assert/strict';
import test from 'node:test';
import { validateProjectTestInput } from '../../desktop/macos/project-test-input.mjs';

const file = (path, content = '') => ({ path, data: Buffer.from(content).toString('base64') });
const good = { name: 'src/calc.test.mjs', files: [file('src/calc.test.mjs'), file('src/calc.mjs', 'addition')] };

test('admits only a bounded, explicit test in the staged snapshot', () => {
  const result = validateProjectTestInput(good);
  assert.equal(result.name, 'src/calc.test.mjs');
  assert.equal(result.files[1].data.toString(), 'addition');
  for (const name of ['../calc.test.mjs', '/private/calc.test.mjs', 'src/calc.mjs', 'src/missing.test.mjs']) {
    assert.throws(() => validateProjectTestInput({ ...good, name }));
  }
});

test('excludes secrets, duplicate APFS names, path collisions, and malformed bytes', () => {
  for (const extra of [file('src/CALC.test.mjs'), file('src/.env'), file('src/.git/config'), file('src/calc.mjs/content'),
    { path: 'src/calc2.mjs', data: '*' }, file('src/package.pem'), file('a\\..\\secret')]) {
    assert.throws(() => validateProjectTestInput({ ...good, files: [...good.files, extra] }));
  }
});
