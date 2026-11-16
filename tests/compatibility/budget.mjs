const SCALE = 100000000n;

function amount(value) {
  if (typeof value !== 'string' || value.trim() !== value ||
      !/^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,8})?$/.test(value)) {
    throw new Error('budget: invalid decimal string');
  }
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole) * SCALE + BigInt(fraction.padEnd(8, '0'));
}

function decimal(value) {
  const fraction = (value % SCALE).toString().padStart(8, '0').replace(/0+$/, '');
  return `${value / SCALE}${fraction ? `.${fraction}` : ''}`;
}

function identifier(value) {
  if (typeof value !== 'string' || !value || /\s/.test(value)) throw new Error('budget: invalid identifier');
}

function limits(totalU, perModelU) {
  const total = amount(totalU), perModel = amount(perModelU);
  if (total > 10n * SCALE || perModel > SCALE) throw new Error('budget: exceeds approved limits');
  return { total, perModel };
}

function used(attempts, model) {
  return attempts.reduce((sum, attempt) => sum +
    (model === undefined || attempt.model === model ? amount(attempt.occupiedU) : 0n), 0n);
}

function snapshot(totalU, perModelU, entries) {
  const attempts = Object.freeze(entries.map((entry) => Object.freeze({ ...entry })));
  return Object.freeze({ totalU, perModelU, occupiedU: decimal(used(attempts)), attempts });
}

export function createBudgetLedger({ totalU = '10', perModelU = '1' } = {}) {
  limits(totalU, perModelU);
  return snapshot(totalU, perModelU, []);
}

export function reserveAttempt(ledger, { id, model, capU }) {
  identifier(id);
  identifier(model);
  const cap = amount(capU), { total, perModel } = limits(ledger.totalU, ledger.perModelU);
  const previous = ledger.attempts.find((attempt) => attempt.id === id);
  // Payload spelling is part of idempotency; do not normalize it before comparison.
  if (previous) {
    if (previous.model !== model || previous.capU !== capU) throw new Error('budget: reservation conflict');
    return ledger;
  }
  if (used(ledger.attempts) + cap > total || used(ledger.attempts, model) + cap > perModel) {
    throw new Error('budget: reservation exceeds budget');
  }
  return snapshot(ledger.totalU, ledger.perModelU, [...ledger.attempts,
    { id, model, capU, chargedU: null, supplierCashU: null, occupiedU: decimal(cap), status: 'reserved' }]);
}

export function settleAttempt(ledger, { id, chargedU, supplierCashU = null }) {
  identifier(id);
  limits(ledger.totalU, ledger.perModelU);
  const charged = amount(chargedU), cash = supplierCashU === null ? null : amount(supplierCashU);
  const previous = ledger.attempts.find((attempt) => attempt.id === id);
  if (!previous) throw new Error('budget: unknown attempt');
  const cap = amount(previous.capU);
  if (charged > cap || (cash !== null && cash > cap)) throw new Error('budget: settlement exceeds cap');
  // The first charge is immutable; pending cash may only advance from unknown to confirmed.
  if ((previous.chargedU !== null && previous.chargedU !== chargedU) ||
      (previous.supplierCashU !== null && previous.supplierCashU !== supplierCashU)) {
    throw new Error('budget: settlement conflict');
  }
  if (previous.chargedU === chargedU && previous.supplierCashU === supplierCashU) return ledger;
  const occupied = cash === null ? cap : (charged > cash ? charged : cash);
  const replacement = { ...previous, chargedU, supplierCashU, occupiedU: decimal(occupied),
    status: cash === null ? 'pending' : 'settled' };
  return snapshot(ledger.totalU, ledger.perModelU,
    ledger.attempts.map((attempt) => attempt.id === id ? replacement : attempt));
}
