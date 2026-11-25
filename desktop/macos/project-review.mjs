import { randomUUID } from 'node:crypto';
import { posix } from 'node:path';

const identity = /^-?\d+:\d+:-?\d+:\d+$/;
const maxFile = 2 * 1024 * 1024;
const utf8 = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
const samePath = name => typeof name === 'string' && !name.startsWith('/') &&
  Buffer.byteLength(name) <= 500 && !/[\\:\x00-\x1f]/.test(name) &&
  name.split('/').every(part => part && part !== '.' && part !== '..');

function decode(data) {
  if (typeof data !== 'string' || data.length > Math.ceil(maxFile / 3) * 4) throw Error('PROJECT_DATA_INVALID');
  const bytes = Buffer.from(data, 'base64');
  if (bytes.length > maxFile || bytes.toString('base64') !== data) throw Error('PROJECT_DATA_INVALID');
  return bytes;
}

function text(data) {
  const bytes = decode(data);
  if (bytes.includes(0)) throw Error('PROJECT_FILE_NOT_TEXT');
  try { return utf8.decode(bytes); }
  catch { throw Error('PROJECT_FILE_NOT_TEXT'); }
}

export function createDarwinProjectReview({ root, rootIdentity, files }, { commit, confirm }) {
  if (typeof commit !== 'function' || typeof confirm !== 'function' || typeof root !== 'string' ||
      !root.startsWith('/') || root === '/' || posix.normalize(root) !== root || /[\x00-\x1f]/.test(root) ||
      !identity.test(rootIdentity ?? '') || !Array.isArray(files) || files.length > 3000)
    throw Error('PROJECT_SNAPSHOT_INVALID');
  const base = new Map();
  const paths = new Set();
  let bytes = 0;
  for (const { path, identity: id, data } of files) {
    if (!samePath(path) || !identity.test(id ?? '')) throw Error('PROJECT_SNAPSHOT_INVALID');
    const normalizedPath = path.normalize('NFC').toLowerCase();
    if (paths.has(normalizedPath) || (bytes += decode(data).length) > 32 * 1024 * 1024)
      throw Error('PROJECT_SNAPSHOT_INVALID');
    paths.add(normalizedPath);
    base.set(path, Object.freeze({ path, identity: id, data }));
  }
  const staged = new Map();
  let stagedBytes = bytes;
  let revision = 0, review, approving = false, committing = false, uncertain = false;
  const safe = () => { if (uncertain) throw Error('PROJECT_REOPEN_REQUIRED'); };
  const original = path => {
    safe();
    if (typeof path !== 'string' || !base.has(path)) throw Error('PROJECT_FILE_NOT_FOUND');
    return base.get(path);
  };
  return Object.freeze({
    model: Object.freeze({
      read(path) {
        const before = original(path);
        return text(staged.get(path) ?? before.data);
      },
      stageEdit(path, content) {
        if (committing) throw Error('REVIEW_BUSY');
        const before = original(path);
        text(before.data);
        if (typeof content !== 'string' || Buffer.byteLength(content) > maxFile || content.includes('\0'))
          throw Error('PROJECT_DATA_INVALID');
        const next = Buffer.from(content);
        const data = next.toString('base64');
        const total = stagedBytes - decode(staged.get(path) ?? before.data).length + next.length;
        if (total > 32 * 1024 * 1024) throw Error('PROJECT_LIMIT');
        stagedBytes = total;
        if (data === before.data) staged.delete(path);
        else staged.set(path, data);
        revision++;
      },
    }),
    local: Object.freeze({
      prepare(path) {
        if (approving) throw Error('REVIEW_BUSY');
        const before = original(path);
        const after = staged.get(path);
        if (after === undefined) throw Error('REVIEW_EMPTY');
        review = { id: randomUUID(), revision, before, after };
        return Object.freeze({ id: review.id, path, before: text(before.data), after: text(after) });
      },
      async approve(id) {
        safe();
        if (approving) throw Error('REVIEW_BUSY');
        if (!review || review.id !== id) throw Error('REVIEW_ALREADY_USED');
        const selected = review;
        review = undefined;
        if (selected.revision !== revision) throw Error('REVIEW_STALE');
        approving = true;
        try {
          if (await confirm(Object.freeze({ root, path: selected.before.path })) !== true) return { status: 'rejected' };
          if (selected.revision !== revision) throw Error('REVIEW_STALE');
          committing = true;
          const outcome = await commit(Object.freeze({ root, rootIdentity, before: selected.before, after: selected.after }));
          if (outcome?.status !== 'committed' || outcome.file?.path !== selected.before.path ||
              !identity.test(outcome.file?.identity ?? '') || outcome.file.data !== selected.after)
            throw Error('PROJECT_WRITEBACK_UNCERTAIN');
          const { path, data, identity: id } = outcome.file;
          base.set(path, Object.freeze({ path, data, identity: id }));
          staged.delete(path);
          revision++;
          return { status: 'committed' };
        } catch (error) {
          if (committing && error?.message !== 'PROJECT_CHANGED') uncertain = true;
          throw error;
        } finally {
          committing = false;
          approving = false;
        }
      },
    }),
  });
}
