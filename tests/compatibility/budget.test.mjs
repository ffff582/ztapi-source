import assert from 'node:assert/strict';
import { test } from 'node:test';

let budget = {};
try { budget = await import('./budget.mjs'); } catch (error) {
  if (error.code !== 'ERR_MODULE_NOT_FOUND' || error.url !== new URL('./budget.mjs', import.meta.url).href) throw error;
}
const api = (name) => (...args) => {
  assert.equal(typeof budget[name], 'function', `missing budget export: ${name}`);
  return budget[name](...args);
};
const create = api('createBudgetLedger');
const reserve = api('reserveAttempt');
const settle = api('settleAttempt');
const hold = (ledger, id = 'a', model = 'm', capU = '1') => reserve(ledger, { id, model, capU });
const charge = (ledger, chargedU, supplierCashU, id = 'a') => settle(ledger, { id, chargedU, supplierCashU });

test('defaults retain approved limits and return JSON-safe string amounts', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(create())), { totalU: '10', perModelU: '1', occupiedU: '0', attempts: [] });
});
test('approved total and per-model limits can only be lowered', () => {
  assert.equal(hold(create({ totalU: '0.2', perModelU: '0.2' }), 'a', 'm', '0.2').occupiedU, '0.2');
  for (const options of [{ totalU: '10.00000001' }, { perModelU: '1.00000001' }]) assert.throws(() => create(options));
});
test('amounts reject coercion, whitespace, signs, exponentials and excess precision at every entry point', () => {
  const invalid = ['', ' ', '1\n', '\t1', '-0', '-1', '+1', '.1', '1.', '01', '1e-8', 'NaN', 'Infinity',
    '0.000000001', '1,000', '0x1', 0, 0.1, NaN, Infinity, -Infinity, Number.MAX_SAFE_INTEGER + 1, 1n, null, undefined];
  const ledger = hold(create());
  for (const value of invalid) {
    if (value !== undefined) {
      assert.throws(() => create({ totalU: value }));
      assert.throws(() => create({ perModelU: value }));
    }
    assert.throws(() => reserve(create(), { id: 'x', model: 'm', capU: value }));
    assert.throws(() => charge(ledger, value, '0'));
    if (value != null) assert.throws(() => charge(ledger, '0', value));
  }
});
test('eight-place arithmetic reaches the exact model boundary without floating-point drift', () => {
  const ledger = hold(hold(create(), 'a', 'm', '0.99999999'), 'b', 'm', '0.00000001');
  assert.equal(ledger.occupiedU, '1');
  assert.throws(() => hold(ledger, 'c', 'm', '0.00000001'));
});
test('all attempts for the same model share one cumulative cap', () => {
  const ledger = hold(hold(create(), 'a', 'm', '0.6'), 'b', 'm', '0.4');
  assert.throws(() => hold(ledger, 'c', 'm', '0.1'));
  assert.equal(hold(ledger, 'c', 'other', '0.1').occupiedU, '1.1');
});
test('ten model budgets fit the global cap and the next smallest hold fails', () => {
  let ledger = create();
  for (let i = 0; i < 10; i++) ledger = hold(ledger, String(i), `m${i}`);
  assert.equal(ledger.occupiedU, '10');
  assert.throws(() => hold(ledger, 'overflow', 'new-model', '0.00000001'));
});
test('a lowered total cap applies across distinct models', () => {
  const ledger = hold(create({ totalU: '1.5' }));
  assert.equal(hold(ledger, 'b', 'n', '0.5').occupiedU, '1.5');
  assert.throws(() => hold(ledger, 'b', 'n', '0.50000001'));
});
test('reserve and settle leave earlier ledger snapshots untouched', () => {
  const initial = create(), reserved = hold(initial);
  const before = JSON.stringify(reserved), settled = charge(reserved, '0.2', '0.3');
  assert.equal(initial.occupiedU, '0');
  assert.equal(JSON.stringify(reserved), before);
  assert.notEqual(settled, reserved);
  assert.equal(settled.occupiedU, '0.3');
  assert.ok(Object.isFrozen(settled.attempts[0]));
});
test('duplicate reservations are exact idempotent, including after settlement', () => {
  const ledger = hold(create(), 'a', 'm', '0.50');
  assert.equal(hold(ledger, 'a', 'm', '0.50'), ledger);
  for (const [model, cap] of [['n', '0.50'], ['m', '0.5'], ['m', '0.4']]) assert.throws(() => hold(ledger, 'a', model, cap));
  const settled = charge(ledger, '0.2', '0.3');
  assert.equal(hold(settled, 'a', 'm', '0.50'), settled);
});
test('missing or null supplier cash retains the entire hold and blocks another same-model attempt', () => {
  for (const supplierCashU of [undefined, null]) {
    const ledger = charge(hold(create()), '0', supplierCashU);
    assert.equal(ledger.occupiedU, '1');
    assert.equal(ledger.attempts[0].supplierCashU, null);
    assert.equal(ledger.attempts[0].status, 'pending');
    assert.throws(() => hold(ledger, 'b', 'm', '0.00000001'));
    assert.equal(charge(ledger, '0', null), ledger);
  }
});
test('unconfirmed holds also count against the global budget', () => {
  const ledger = charge(hold(create({ totalU: '1' })), '0', null);
  assert.throws(() => hold(ledger, 'b', 'another', '0.00000001'));
});
test('supplier confirmation can resolve a pending settlement without changing its recorded charge', () => {
  const pending = charge(hold(create()), '0.2', null);
  assert.throws(() => charge(pending, '0.1', '0.3'));
  const confirmed = charge(pending, '0.2', '0.3');
  assert.equal(confirmed.occupiedU, '0.3');
  assert.equal(confirmed.attempts[0].status, 'settled');
  assert.equal(pending.occupiedU, '1');
  assert.equal(hold(confirmed, 'b', 'm', '0.7').occupiedU, '1');
});
test('confirmed occupation is max of charge and supplier cash, never their sum', () => {
  for (const [charged, cash, expected] of [['0.2', '0.7', '0.7'], ['0.8', '0.3', '0.8'], ['0.4', '0.4', '0.4']]) {
    assert.equal(charge(hold(create()), charged, cash).occupiedU, expected);
  }
});
test('confirmed settlements are exactly idempotent and cannot be rewritten or downgraded', () => {
  const ledger = charge(hold(create()), '0.20', '0.3');
  assert.equal(charge(ledger, '0.20', '0.3'), ledger);
  for (const [charged, cash] of [['0.2', '0.3'], ['0.20', '0.30'], ['0.1', '0.3'], ['0.20', null], ['0.20', '0.4']]) {
    assert.throws(() => charge(ledger, charged, cash));
  }
});
test('refunds cannot automatically replenish an already occupied model budget', () => {
  const ledger = charge(hold(create()), '0.8', '0.9');
  assert.throws(() => charge(ledger, '0', '0'));
  assert.equal(ledger.occupiedU, '0.9');
  assert.throws(() => hold(ledger, 'b', 'm', '0.10000001'));
});
test('either settlement amount above cap is rejected atomically, retaining the original hold', () => {
  const ledger = hold(create()), before = JSON.stringify(ledger);
  for (const [charged, cash] of [['1.00000001', null], ['0', '1.00000001'], ['1.00000001', '0']]) {
    assert.throws(() => charge(ledger, charged, cash));
    assert.equal(JSON.stringify(ledger), before);
    assert.throws(() => hold(ledger, 'b', 'm', '0.00000001'));
  }
});
test('a rejected late supplier confirmation retains the pending hold', () => {
  const ledger = charge(hold(create()), '0.2', null), before = JSON.stringify(ledger);
  assert.throws(() => charge(ledger, '0.2', '1.00000001'));
  assert.equal(JSON.stringify(ledger), before);
});
test('zero caps and explicitly confirmed zero cost work without treating unknown cash as zero', () => {
  assert.equal(hold(create({ totalU: '0', perModelU: '0' }), 'a', 'm', '0').occupiedU, '0');
  assert.equal(charge(hold(create()), '0', '0').occupiedU, '0');
});
test('unknown attempt IDs and malformed identifiers are rejected; object-key names remain safe', () => {
  assert.throws(() => charge(create(), '0', '0'));
  for (const value of ['', ' ', ' m', 'm\n', null, 1, undefined]) {
    assert.throws(() => reserve(create(), { id: value, model: 'm', capU: '1' }));
    assert.throws(() => reserve(create(), { id: 'a', model: value, capU: '1' }));
    assert.throws(() => settle(hold(create()), { id: value, chargedU: '0', supplierCashU: '0' }));
  }
  assert.equal(charge(hold(create(), '__proto__', 'constructor'), '0.2', '0.3', '__proto__').occupiedU, '0.3');
});
test('JSON round-trips retain eight-place accounting and settled model aggregates', () => {
  const reserved = hold(hold(create(), 'a', 'm', '0.00000001'), 'b', 'm', '0.99999999');
  const ledger = charge(JSON.parse(JSON.stringify(reserved)), '0.00000001', '0.00000001');
  assert.equal(ledger.occupiedU, '1');
  assert.throws(() => hold(ledger, 'c', 'm', '0.00000001'));
});
