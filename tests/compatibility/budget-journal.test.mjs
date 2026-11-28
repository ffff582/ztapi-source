import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';

let journalModule = {};
try { journalModule = await import('./budget-journal.mjs'); } catch (error) {
  if (error.code !== 'ERR_MODULE_NOT_FOUND' || error.url !== new URL('./budget-journal.mjs', import.meta.url).href) throw error;
}
const moduleURL = new URL('./budget-journal.mjs', import.meta.url).href;
const open = options => {
  assert.equal(typeof journalModule.openBudgetJournal, 'function', 'missing openBudgetJournal export');
  return journalModule.openBudgetJournal(options);
};

function removeTemporary(directory) {
  const resolved = fs.realpathSync(directory);
  assert.equal(resolved, path.resolve(directory));
  assert.equal(path.dirname(resolved), fs.realpathSync(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('ztapi-budget-journal-'));
  fs.rmSync(resolved, { recursive: true, force: true });
}

function fixture(t, limits = {}) {
  assert.equal(typeof journalModule.openBudgetJournal, 'function', 'missing openBudgetJournal export');
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'ztapi-budget-journal-'));
  const options = { directory, batchId: 'xcloud-sync-20261128', totalLimitUsd: '10', perModelLimitUsd: '1', ...limits };
  const handles = [];
  t.after(() => {
    for (const handle of handles) { try { handle.close(); } catch {} }
    removeTemporary(directory);
  });
  return {
    directory, options,
    open: (overrides = {}) => { const handle = open({ ...options, ...overrides }); handles.push(handle); return handle; },
    file: name => path.join(directory, name),
  };
}
const reserve = (journal, attemptId = 'attempt-1', sourceModel = 'gpt-6.1-sol', capU = '1', group = 'gpt-plus') =>
  journal.reserveAttempt({ attemptId, sourceModel, capU, group });
const settle = (journal, chargedU, supplierCashU = null, supplierEvidenceRef = null, attemptId = 'attempt-1') =>
  journal.settleAttempt({ attemptId, chargedU, supplierCashU, supplierEvidenceRef });
const contents = directory => Object.fromEntries(fs.readdirSync(directory).sort().map(name => [name, fs.readFileSync(path.join(directory, name), 'utf8')]));
const events = directory => fs.readdirSync(directory).filter(name => /^event-\d+\.json$/.test(name)).sort();
function rewriteLastEvent(f, mutateEvent, mutateHead) {
  const last = f.file(events(f.directory).at(-1));
  const event = JSON.parse(fs.readFileSync(last)), head = JSON.parse(fs.readFileSync(f.file('head.json')));
  mutateEvent(event);
  const raw = JSON.stringify(event) + '\n';
  fs.writeFileSync(last, raw);
  head.eventSha256 = createHash('sha256').update(raw).digest('hex');
  mutateHead(head);
  fs.writeFileSync(f.file('head.json'), JSON.stringify(head) + '\n');
}
const child = (code, options) => spawnSync(process.execPath, ['--input-type=module', '-e',
  `import fs from 'node:fs'; import {openBudgetJournal} from ${JSON.stringify(moduleURL)};
   const options = ${JSON.stringify(options)}; ${code}`], { encoding: 'utf8', timeout: 15000, windowsHide: true });

test('real Xcloud domestic group survives persistence without changing model budget', t => {
  const f = fixture(t), journal = f.open();
  const group = '\u56fd\u4ea7\u5206\u7ec4';
  reserve(journal, 'domestic-1', 'glm-5.3', '0.4', group);
  reserve(journal, 'domestic-2', 'glm-5.3', '0.6', group);
  assert.throws(() => reserve(journal, 'domestic-3', 'glm-5.3', '0.1', group), /budget/);
  assert.equal(journal.snapshot().occupiedU, '1');
  journal.close();
  assert.equal(f.open().snapshot().attempts[0].group, group);
});

test('opening creates a persistent empty batch and snapshots cannot mutate its accounting', t => {
  const f = fixture(t), journal = f.open(), state = journal.snapshot();
  assert.equal(state.batchId, f.options.batchId);
  assert.equal(state.totalLimitUsd, '10');
  assert.equal(state.perModelLimitUsd, '1');
  assert.equal(state.occupiedU, '0');
  assert.deepEqual(state.attempts, []);
  state.attempts.push({ attemptId: 'fake' });
  assert.deepEqual(journal.snapshot().attempts, []);
  assert.ok(fs.existsSync(f.file('head.json')));
  assert.ok(events(f.directory).length > 0);
});

test('restart replays exact decimal reservations without restoring spent capacity', t => {
  const f = fixture(t), first = f.open();
  reserve(first, 'a', 'gpt-6.1-sol', '0.99999999');
  reserve(first, 'b', 'gpt-6.1-sol', '0.00000001');
  const before = first.snapshot();
  first.close();
  const restarted = f.open();
  assert.deepEqual(restarted.snapshot(), before);
  assert.equal(before.occupiedU, '1');
  assert.throws(() => reserve(restarted, 'c', 'gpt-6.1-sol', '0.00000001'));
});

test('sourceModel shares its cumulative cap across groups and retry attempt IDs', t => {
  const journal = fixture(t).open();
  reserve(journal, 'a', 'claude-opus-5-5', '0.6', 'claude-max');
  reserve(journal, 'retry-a', 'claude-opus-5-5', '0.4', 'new-vip');
  assert.throws(() => reserve(journal, 'b', 'claude-opus-5-5', '0.00000001', 'test'));
  assert.equal(reserve(journal, 'c', 'claude-haiku-5-5', '0.1', 'test').occupiedU, '1.1');
});

test('global cap counts distinct models and pending reservations', t => {
  const journal = fixture(t).open();
  for (let i = 0; i < 10; i++) {
    reserve(journal, `a${i}`, `source-${i}`);
    settle(journal, '0', null, null, `a${i}`);
  }
  assert.equal(journal.snapshot().occupiedU, '10');
  assert.throws(() => reserve(journal, 'overflow', 'other', '0.00000001'));
});

test('identical attempt IDs are idempotent but cannot change model, cap spelling or group', t => {
  const f = fixture(t), journal = f.open();
  const state = reserve(journal, 'a', 'gpt-6.1-sol', '0.50');
  const before = contents(f.directory);
  assert.deepEqual(reserve(journal, 'a', 'gpt-6.1-sol', '0.50'), state);
  assert.deepEqual(contents(f.directory), before);
  for (const [model, cap, group] of [['other', '0.50', 'gpt-plus'], ['gpt-6.1-sol', '0.5', 'gpt-plus'], ['gpt-6.1-sol', '0.50', 'new-vip']]) {
    assert.throws(() => reserve(journal, 'a', model, cap, group));
  }
  assert.deepEqual(contents(f.directory), before);
});

test('missing supplier cash retains the full hold across restart', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  assert.equal(settle(journal, '0.1').occupiedU, '1');
  journal.close();
  const restarted = f.open();
  assert.equal(restarted.snapshot().attempts[0].status, 'pending');
  assert.throws(() => reserve(restarted, 'retry', 'gpt-6.1-sol', '0.00000001'));
});

test('cash confirmation including explicit zero requires a durable supplier receipt reference', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  const before = contents(f.directory);
  for (const evidence of [null, undefined, '', ' ', '\nreceipt', ' receipt', 123]) {
    assert.throws(() => settle(journal, '0', '0', evidence));
  }
  assert.throws(() => settle(journal, '0', null, 'xcloud:bill-1'));
  assert.deepEqual(contents(f.directory), before);
  assert.equal(settle(journal, '0', '0', 'xcloud:bill-zero').occupiedU, '0');
});

test('confirmed occupation is the maximum of station charge and supplier cash', t => {
  const journal = fixture(t).open();
  reserve(journal, 'a', 'model-a');
  reserve(journal, 'b', 'model-b');
  reserve(journal, 'c', 'model-c');
  settle(journal, '0.2', '0.7', 'xcloud:bill-a', 'a');
  settle(journal, '0.8', '0.3', 'xcloud:bill-b', 'b');
  const state = settle(journal, '0.00000001', '0', 'xcloud:bill-c', 'c');
  assert.equal(state.occupiedU, '1.50000001');
  assert.deepEqual(state.attempts.map(row => row.occupiedU), ['0.7', '0.8', '0.00000001']);
});

test('pending cash can advance once without rewriting the original charge or receipt', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  settle(journal, '0.20');
  assert.throws(() => settle(journal, '0.1', '0.3', 'xcloud:bill-1'));
  const state = settle(journal, '0.20', '0.3', 'xcloud:bill-1');
  const before = contents(f.directory);
  assert.deepEqual(settle(journal, '0.20', '0.3', 'xcloud:bill-1'), state);
  for (const args of [['0.20', null, null], ['0.2', '0.3', 'xcloud:bill-1'], ['0.20', '0.3', 'xcloud:other'], ['0', '0', 'xcloud:bill-1']]) {
    assert.throws(() => settle(journal, ...args));
  }
  assert.deepEqual(contents(f.directory), before);
  journal.close();
  const reopened = f.open();
  assert.deepEqual(reopened.snapshot(), state);
  assert.equal(reserve(reopened, 'retry', 'gpt-6.1-sol', '0.7').occupiedU, '1');
});

test('one supplier receipt cannot settle different attempts', t => {
  const journal = fixture(t).open();
  reserve(journal, 'a', 'model-a');
  reserve(journal, 'b', 'model-b');
  settle(journal, '0', '0.1', 'xcloud:bill-1', 'a');
  assert.throws(() => settle(journal, '0', '0.1', 'XCLOUD:BILL-1', 'b'));
  assert.equal(journal.snapshot().occupiedU, '1.1');
});

test('over-cap or malformed settlements retain the original hold and append nothing', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  const before = contents(f.directory);
  for (const [charge, cash] of [['1.00000001', '0'], ['0', '1.00000001'], ['1e-8', '0'], ['0', 0]]) {
    assert.throws(() => settle(journal, charge, cash, 'xcloud:bill-1'));
  }
  assert.throws(() => settle(journal, '0', '0', 'xcloud:bill-1', 'missing'));
  assert.deepEqual(contents(f.directory), before);
});

test('invalid inputs cannot create holds, use a public alias field or bypass decimals', t => {
  const f = fixture(t), journal = f.open(), before = contents(f.directory);
  for (const values of [
    { attemptId: '', sourceModel: 'm', capU: '1' },
    { attemptId: 'a', model: 'm', capU: '1' },
    { attemptId: 'a', sourceModel: 'm@new-vip', capU: '1' },
    { attemptId: 'a', sourceModel: ' m', capU: '1' },
    { attemptId: 'a', sourceModel: 'm', capU: 0.1 },
    { attemptId: 'a', sourceModel: 'm', capU: '0.000000001' },
    { attemptId: 'a', sourceModel: 'm', capU: '-1' },
    { attemptId: 'a', sourceModel: 'm', capU: '1', group: ' bad' },
  ]) assert.throws(() => journal.reserveAttempt(values));
  assert.deepEqual(contents(f.directory), before);
});

test('wrong batch and limit configuration cannot reopen or reset an existing journal', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  journal.close();
  const before = contents(f.directory);
  for (const overrides of [{ batchId: 'another-batch' }, { totalLimitUsd: '9' }, { totalLimitUsd: '10.0' }, { perModelLimitUsd: '0.9' }]) {
    assert.throws(() => f.open(overrides));
    assert.deepEqual(contents(f.directory), before);
  }
  assert.equal(f.open().snapshot().occupiedU, '1');
});

test('invalid initial limits, batch IDs and relative directories fail before writing', t => {
  const f = fixture(t);
  for (const overrides of [
    { totalLimitUsd: '10.00000001' }, { perModelLimitUsd: '1.00000001' },
    { totalLimitUsd: '1e1' }, { perModelLimitUsd: 1 }, { batchId: '../other' },
    { batchId: '' }, { directory: '.' }, { totalLimitUsd: null },
  ]) {
    assert.throws(() => f.open(overrides));
    assert.deepEqual(fs.readdirSync(f.directory), []);
  }
  const journal = f.open({ totalLimitUsd: '0.2', perModelLimitUsd: '0.2' });
  reserve(journal, 'a', 'm', '0.2');
  assert.throws(() => reserve(journal, 'b', 'n', '0.00000001'));
});

test('another instance and another process cannot acquire the exclusive lock', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  const before = contents(f.directory);
  assert.throws(() => f.open());
  const result = child('try { openBudgetJournal(options); process.exitCode=2; } catch { console.log("locked"); }', f.options);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), 'locked');
  assert.deepEqual(contents(f.directory), before);
});

test('normal close only releases its own lock and closes every method', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  journal.close();
  journal.close();
  assert.equal(fs.existsSync(f.file('journal.lock')), false);
  assert.throws(() => journal.snapshot());
  assert.throws(() => reserve(journal));
  assert.throws(() => settle(journal, '0'));
  assert.equal(f.open().snapshot().occupiedU, '1');
});

test('a process exit leaves its lock and reservation intact instead of auto-breaking the lock', t => {
  const f = fixture(t);
  const result = child('const j=openBudgetJournal(options); j.reserveAttempt({attemptId:"a",sourceModel:"m",capU:"1"}); process.exit(0);', f.options);
  assert.equal(result.status, 0, result.stderr);
  const before = contents(f.directory);
  assert.throws(() => f.open());
  assert.deepEqual(contents(f.directory), before);
  assert.equal(JSON.parse(fs.readFileSync(f.file('head.json'))).occupiedU, '1');
});

test('replaced lock ownership poisons the handle without removing the replacement', t => {
  const f = fixture(t), journal = f.open();
  fs.writeFileSync(f.file('journal.lock'), '{"owner":"someone-else"}\n');
  const before = contents(f.directory);
  assert.throws(() => reserve(journal));
  assert.throws(() => journal.close());
  assert.deepEqual(contents(f.directory), before);
});

test('close retains its lock when disk history becomes damaged before shutdown', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  fs.writeFileSync(f.file('head.json'), '{');
  const before = contents(f.directory);
  assert.throws(() => journal.close());
  assert.deepEqual(contents(f.directory), before);
  assert.throws(() => f.open());
});

test('replay enforces per-model limits even when the last event and head hashes are rewritten together', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal, 'a', 'm', '0.6');
  reserve(journal, 'b', 'm', '0.4');
  journal.close();
  rewriteLastEvent(f, event => { event.operation.value.capU = '0.5'; }, head => {
    head.occupiedU = '1.1'; head.attempts[1].capU = '0.5'; head.attempts[1].occupiedU = '0.5';
  });
  assert.throws(() => f.open(), /reservation exceeds budget/);
});

test('replay rejects receipt-free settlement even with a matching rewritten head hash', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  settle(journal, '0', '0', 'xcloud:bill-zero');
  journal.close();
  rewriteLastEvent(f, event => { event.operation.value.supplierEvidenceRef = null; }, head => {
    head.attempts[0].supplierEvidenceRef = null;
  });
  assert.throws(() => f.open(), /supplier receipt reference required/);
});

for (const corruption of ['head-total', 'head-truncated', 'event-truncated', 'duplicate-json-member', 'missing-event', 'missing-head', 'orphan-event', 'partial-head', 'foreign-file']) {
  test(`replay fails closed on ${corruption} and never repairs or removes old files`, t => {
    const f = fixture(t), journal = f.open();
    reserve(journal, 'a', 'm', '0.6');
    reserve(journal, 'b', 'm', '0.4');
    journal.close();
    const names = events(f.directory), last = f.file(names.at(-1));
    if (corruption === 'head-total') {
      const head = JSON.parse(fs.readFileSync(f.file('head.json'))); head.occupiedU = '0';
      fs.writeFileSync(f.file('head.json'), JSON.stringify(head) + '\n');
    } else if (corruption === 'head-truncated') fs.writeFileSync(f.file('head.json'), '{');
    else if (corruption === 'event-truncated') fs.writeFileSync(last, '{');
    else if (corruption === 'duplicate-json-member') {
      const raw = fs.readFileSync(last, 'utf8');
      fs.writeFileSync(last, '{"batchId":"fake",' + raw.slice(1));
    } else if (corruption === 'missing-event') fs.unlinkSync(f.file(names[1]));
    else if (corruption === 'missing-head') fs.unlinkSync(f.file('head.json'));
    else if (corruption === 'orphan-event') fs.copyFileSync(last, f.file('event-999999999999.json'));
    else if (corruption === 'partial-head') fs.writeFileSync(f.file('head.interrupted.tmp'), '{');
    else fs.writeFileSync(f.file('unrelated.txt'), 'do not overwrite');
    const before = contents(f.directory);
    assert.throws(() => f.open());
    assert.deepEqual(contents(f.directory), before);
  });
}

test('an open handle revalidates disk and refuses an external rollback', t => {
  const f = fixture(t), journal = f.open();
  const earlier = contents(f.directory);
  reserve(journal);
  const later = events(f.directory).filter(name => !Object.hasOwn(earlier, name));
  fs.writeFileSync(f.file('head.json'), earlier['head.json']);
  for (const name of later) fs.unlinkSync(f.file(name));
  assert.throws(() => journal.snapshot());
  assert.throws(() => reserve(journal, 'b', 'other'));
});

test('event and published head are fsynced before reservation returns', t => {
  const f = fixture(t), journal = f.open(), flushed = new Set();
  const original = fs.fsyncSync;
  t.mock.method(fs, 'fsyncSync', fd => {
    original(fd);
    const stat = fs.fstatSync(fd);
    if (stat.isFile()) flushed.add(`${stat.dev}:${stat.ino}`);
  });
  const state = reserve(journal);
  for (const name of [events(f.directory).at(-1), 'head.json']) {
    const stat = fs.statSync(f.file(name));
    assert.ok(flushed.has(`${stat.dev}:${stat.ino}`), `${name} was not fsynced before returning`);
  }
  assert.equal(JSON.parse(fs.readFileSync(f.file('head.json'))).occupiedU, state.occupiedU);
});

test('settlement receipt evidence is fsynced in both history and head before releasing the difference', t => {
  const f = fixture(t), journal = f.open(), flushed = new Set();
  reserve(journal);
  const original = fs.fsyncSync;
  t.mock.method(fs, 'fsyncSync', fd => {
    original(fd);
    const stat = fs.fstatSync(fd);
    if (stat.isFile()) flushed.add(`${stat.dev}:${stat.ino}`);
  });
  assert.equal(settle(journal, '0.1', '0.2', 'xcloud:bill-1').occupiedU, '0.2');
  for (const name of [events(f.directory).at(-1), 'head.json']) {
    const stat = fs.statSync(f.file(name));
    assert.ok(flushed.has(`${stat.dev}:${stat.ino}`));
    assert.ok(fs.readFileSync(f.file(name), 'utf8').includes('xcloud:bill-1'));
  }
});

for (const fault of ['event-fsync', 'head-rename']) {
  test(`${fault} failure never reports success or deletes uncertain evidence`, t => {
    const f = fixture(t), journal = f.open();
    const count = events(f.directory).length;
    if (fault === 'event-fsync') t.mock.method(fs, 'fsyncSync', () => { throw new Error('injected flush failure'); });
    else t.mock.method(fs, 'renameSync', () => { throw new Error('injected rename failure'); });
    assert.throws(() => reserve(journal));
    t.mock.restoreAll();
    assert.ok(events(f.directory).length > count);
    assert.throws(() => journal.snapshot());
    assert.throws(() => reserve(journal, 'retry', 'other'));
    assert.throws(() => journal.close());
    const before = contents(f.directory);
    assert.throws(() => f.open());
    assert.deepEqual(contents(f.directory), before);
  });
}

test('hard-linked event or head is not accepted as an independently owned journal file', t => {
  const f = fixture(t), journal = f.open();
  reserve(journal);
  journal.close();
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'ztapi-budget-journal-link-'));
  t.after(() => removeTemporary(outside));
  fs.linkSync(f.file('head.json'), path.join(outside, 'head-alias.json'));
  assert.throws(() => f.open());
});

test('journal directory junctions or symlinks cannot alias another budget directory', t => {
  const f = fixture(t), outside = fs.mkdtempSync(path.join(os.tmpdir(), 'ztapi-budget-journal-link-'));
  const alias = path.join(outside, 'alias');
  t.after(() => removeTemporary(outside));
  fs.symlinkSync(f.directory, alias, process.platform === 'win32' ? 'junction' : 'dir');
  assert.throws(() => f.open({ directory: alias }));
  assert.deepEqual(fs.readdirSync(f.directory), []);
});
