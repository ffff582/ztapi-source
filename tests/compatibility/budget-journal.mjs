import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { createBudgetLedger, reserveAttempt, settleAttempt } from './budget.mjs';

const encode = value => JSON.stringify(value) + '\n';
const digest = raw => createHash('sha256').update(raw).digest('hex');
const eventName = revision => `event-${String(revision).padStart(12, '0')}.json`;
const need = (condition, message) => assert.ok(condition, `budget journal: ${message}`);
const sameFile = (a, b) => a.dev === b.dev && a.ino === b.ino;

function fields(value, allowed) {
  need(value !== null && typeof value === 'object' && !Array.isArray(value), 'object required');
  need(Object.keys(value).every(key => allowed.includes(key)), 'unknown parameter');
}

function identifier(value) {
  need(typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,199}$/.test(value), 'invalid attempt ID');
}

function reservation(value) {
  fields(value, ['attemptId', 'sourceModel', 'capU', 'group']);
  identifier(value.attemptId);
  need(typeof value.sourceModel === 'string' && /^[a-z0-9][a-z0-9._-]{0,254}$/.test(value.sourceModel), 'exact sourceModel required');
  const group = value.group ?? null;
  need(group === null || typeof group === 'string' && /^[a-zA-Z0-9\u3400-\u9fff][a-zA-Z0-9._\u3400-\u9fff-]{0,127}$/.test(group), 'invalid group');
  return { attemptId: value.attemptId, sourceModel: value.sourceModel, capU: value.capU, group };
}

function settlement(value) {
  fields(value, ['attemptId', 'chargedU', 'supplierCashU', 'supplierEvidenceRef']);
  identifier(value.attemptId);
  const supplierCashU = value.supplierCashU ?? null;
  const supplierEvidenceRef = value.supplierEvidenceRef ?? null;
  if (supplierCashU === null) need(supplierEvidenceRef === null, 'unknown supplier cash cannot carry confirmation evidence');
  else need(typeof supplierEvidenceRef === 'string' && supplierEvidenceRef.length >= 3 && supplierEvidenceRef.length <= 512 &&
    supplierEvidenceRef.trim() === supplierEvidenceRef && !/[\x00-\x1f\x7f]/.test(supplierEvidenceRef), 'supplier receipt reference required');
  return { attemptId: value.attemptId, chargedU: value.chargedU, supplierCashU, supplierEvidenceRef };
}

function apply(state, operation) {
  fields(operation, ['type', 'value']);
  if (operation.type === 'reserve') {
    const value = reservation(operation.value);
    const previous = state.details.get(value.attemptId);
    if (previous) need(previous.group === value.group, 'attempt group changed');
    const ledger = reserveAttempt(state.ledger, { id: value.attemptId, model: value.sourceModel, capU: value.capU });
    if (ledger === state.ledger) return state;
    const details = new Map(state.details);
    details.set(value.attemptId, { group: value.group, supplierEvidenceRef: null });
    return { ledger, details };
  }
  need(operation.type === 'settle', 'unrecognized event operation');
  const value = settlement(operation.value), previous = state.details.get(value.attemptId);
  need(previous, 'unknown attempt');
  if (previous.supplierEvidenceRef !== null) need(previous.supplierEvidenceRef === value.supplierEvidenceRef, 'supplier evidence changed');
  if (value.supplierEvidenceRef !== null) {
    for (const [id, detail] of state.details) {
      need(id === value.attemptId || detail.supplierEvidenceRef?.toLowerCase() !== value.supplierEvidenceRef.toLowerCase(), 'supplier receipt already used');
    }
  }
  const ledger = settleAttempt(state.ledger, { id: value.attemptId, chargedU: value.chargedU, supplierCashU: value.supplierCashU });
  if (ledger === state.ledger) return state;
  const details = new Map(state.details);
  details.set(value.attemptId, { ...previous, supplierEvidenceRef: value.supplierEvidenceRef });
  return { ledger, details };
}

function projection(metadata, state, revision, eventSha256) {
  return {
    schemaVersion: 1, ...metadata, revision, eventSha256, occupiedU: state.ledger.occupiedU,
    attempts: state.ledger.attempts.map(({ id, model, ...amounts }) => ({
      attemptId: id, sourceModel: model, ...amounts, ...state.details.get(id),
    })),
  };
}

function readRegular(file) {
  const before = fs.lstatSync(file);
  need(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 && before.size <= 16 * 1024 * 1024, 'unsafe journal file');
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  try {
    const opened = fs.fstatSync(fd);
    need(sameFile(before, opened), 'file changed while opening');
    const raw = fs.readFileSync(fd, 'utf8'), after = fs.fstatSync(fd), named = fs.lstatSync(file);
    need(sameFile(opened, after) && sameFile(after, named) && after.nlink === 1 &&
      before.size === after.size && before.mtimeMs === after.mtimeMs && !named.isSymbolicLink(), 'file changed while reading');
    const value = JSON.parse(raw);
    need(encode(value) === raw, 'noncanonical or duplicate JSON fields');
    return { raw, value, stat: after };
  } finally { fs.closeSync(fd); }
}

function syncDirectory(directory) {
  // Node cannot open/FlushFileBuffers a directory on Windows. Both files are
  // fsynced there; POSIX additionally persists directory entry changes.
  if (process.platform === 'win32') return;
  const fd = fs.openSync(directory, 'r');
  try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}

function writeExclusive(file, raw) {
  const fd = fs.openSync(file, 'wx', 0o600);
  try { fs.writeFileSync(fd, raw, 'utf8'); fs.fsyncSync(fd); }
  finally { fs.closeSync(fd); }
}

/**
 * One trusted, existing, absolute directory belongs to exactly one batch.
 * All methods are synchronous. Pass real supplier source IDs, not public aliases.
 * reserveAttempt({attemptId, sourceModel, capU, group?})
 * settleAttempt({attemptId, chargedU, supplierCashU?, supplierEvidenceRef?})
 * snapshot() returns detached JSON-safe state; close() releases only our lock.
 * Limits may only be lowered from 10/1 and must reopen with identical spelling.
 * Unknown cash retains the full cap, including when the station charge is zero.
 * Receipt references are operator attestations, not authenticated supplier bills.
 * Never dispatch on an exception, auto-break locks, or delete orphaned events.
 */
export function openBudgetJournal(options) {
  fields(options, ['directory', 'batchId', 'totalLimitUsd', 'perModelLimitUsd']);
  const { directory, batchId, totalLimitUsd = '10', perModelLimitUsd = '1' } = options;
  need(typeof directory === 'string' && path.isAbsolute(directory), 'absolute directory required');
  need(typeof batchId === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$/.test(batchId), 'invalid batch ID');
  const metadata = { batchId, totalLimitUsd, perModelLimitUsd };
  const empty = () => ({ ledger: createBudgetLedger({ totalU: totalLimitUsd, perModelU: perModelLimitUsd }), details: new Map() });
  empty();
  const directoryStat = fs.lstatSync(directory);
  need(directoryStat.isDirectory() && !directoryStat.isSymbolicLink() && fs.realpathSync(directory) === path.resolve(directory), 'directory aliases forbidden');
  const lockPath = path.join(directory, 'journal.lock'), headPath = path.join(directory, 'head.json');
  const lockRaw = encode({ schemaVersion: 1, ...metadata, pid: process.pid, nonce: randomUUID() });
  const lockFd = fs.openSync(lockPath, 'wx', 0o600), lockStat = fs.fstatSync(lockFd);
  let closed = false, poisoned = false, writing = false, current;

  function owned() {
    need(!closed && !poisoned, 'journal closed or uncertain; manual review required');
    const actualDirectory = fs.lstatSync(directory);
    need(actualDirectory.isDirectory() && !actualDirectory.isSymbolicLink() && sameFile(directoryStat, actualDirectory) &&
      fs.realpathSync(directory) === path.resolve(directory), 'directory identity changed');
    const lock = readRegular(lockPath);
    need(sameFile(lock.stat, lockStat) && lock.raw === lockRaw, 'lock ownership changed');
  }

  function replay() {
    owned();
    const names = fs.readdirSync(directory).sort();
    need(names.includes('head.json'), 'missing head; never reset a partial journal');
    const eventNames = names.filter(name => /^event-\d{12}\.json$/.test(name));
    need(names.length === eventNames.length + 2 && names.includes('journal.lock'), 'unexpected or interrupted journal files');
    need(eventNames.length > 0, 'missing event history');
    let state = empty(), previousSha256 = null, head;
    for (let revision = 0; revision < eventNames.length; revision++) {
      need(eventNames[revision] === eventName(revision), 'event history gap');
      const { raw, value } = readRegular(path.join(directory, eventNames[revision]));
      const operation = revision === 0 ? { type: 'open' } : value.operation;
      const expected = { schemaVersion: 1, ...metadata, revision, previousSha256, operation };
      need(raw === encode(expected), 'batch, limits or event hash chain changed');
      if (revision > 0) {
        const next = apply(state, operation);
        need(next !== state, 'duplicate no-op event');
        state = next;
      }
      previousSha256 = digest(raw);
      head = projection(metadata, state, revision, previousSha256);
    }
    const storedHead = readRegular(headPath);
    need(storedHead.raw === encode(head), 'head does not match replayed history');
    owned();
    need(encode(fs.readdirSync(directory).sort()) === encode(names), 'journal inventory changed');
    return { state, head, raw: storedHead.raw };
  }

  function writeHead(head) {
    const temporary = path.join(directory, `head.${randomUUID()}.tmp`);
    writeExclusive(temporary, encode(head));
    syncDirectory(directory);
    owned();
    fs.renameSync(temporary, headPath);
    // Also flush the published name before callers may dispatch a paid request.
    const fd = fs.openSync(headPath, 'r+');
    try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
    syncDirectory(directory);
  }

  function commit(state, operation, revision, previousSha256) {
    owned();
    need(Number.isSafeInteger(revision) && revision >= 0 && revision < 1_000_000_000_000, 'invalid revision');
    const event = { schemaVersion: 1, ...metadata, revision, previousSha256, operation };
    const raw = encode(event), head = projection(metadata, state, revision, digest(raw));
    writing = true;
    try {
      writeExclusive(path.join(directory, eventName(revision)), raw);
      syncDirectory(directory);
      writeHead(head);
      current = replay();
      need(current.raw === encode(head), 'published state changed');
      writing = false;
      return structuredClone(head);
    } catch (error) { poisoned = true; throw error; }
  }

  function check() {
    try {
      const actual = replay();
      need(actual.raw === current.raw, 'head changed outside this journal');
      return actual;
    } catch (error) { poisoned = true; throw error; }
  }

  function close() {
    if (closed) return;
    try {
      if (poisoned) throw new Error('budget journal: uncertain state; lock retained for manual review');
      if (current) check();
      owned();
      fs.unlinkSync(lockPath);
      syncDirectory(directory);
    } finally { closed = true; fs.closeSync(lockFd); }
  }

  try {
    fs.writeFileSync(lockFd, lockRaw, 'utf8');
    fs.fsyncSync(lockFd);
    syncDirectory(directory);
    const names = fs.readdirSync(directory);
    if (names.length === 1 && names[0] === 'journal.lock') commit(empty(), { type: 'open' }, 0, null);
    else current = replay();
  } catch (error) {
    if (writing || poisoned) { closed = true; fs.closeSync(lockFd); }
    else { try { close(); } catch {} }
    throw error;
  }

  function change(type, value) {
    const actual = check();
    const operation = { type, value: type === 'reserve' ? reservation(value) : settlement(value) };
    const next = apply(actual.state, operation);
    if (next === actual.state) return structuredClone(actual.head);
    return commit(next, operation, actual.head.revision + 1, actual.head.eventSha256);
  }

  return Object.freeze({
    reserveAttempt: value => change('reserve', value),
    settleAttempt: value => change('settle', value),
    snapshot: () => structuredClone(check().head),
    close,
  });
}
