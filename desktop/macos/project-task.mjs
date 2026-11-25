import { createHash, randomBytes } from 'node:crypto';
import { createDarwinProjectReview } from './project-review.mjs';
import { validateProjectTestInput } from './project-test-input.mjs';

const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const decode = value => Buffer.from(value, 'base64');
const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
const maxTotal = 32 * 1024 * 1024;
const maxFile = 2 * 1024 * 1024;

export class MacProjectTask {
  #snapshot;
  #review;
  #last;
  #busy = false;
  #draft = false;
  #test;
  #diff;
  #base;

  constructor({ source, accountId, confirm, restore }, ports) {
    if (!Number.isSafeInteger(accountId) || accountId < 1) throw Error('SIGN_IN_REQUIRED');
    if (typeof confirm !== 'function' || !['snapshot', 'test', 'diff', 'commit'].every(key => typeof ports?.[key] === 'function'))
      throw Error('NATIVE_PROJECT_NOT_READY');
    const snapshot = structuredClone(ports.snapshot(source));
    if (snapshot?.root !== source) throw Error('PROJECT_SCAN_ROOT_MISMATCH');
    this.#snapshot = snapshot;
    this.#base = new Map(snapshot.files.map(file => [file.path, file]));
    this.#review = createDarwinProjectReview(snapshot, {
      confirm: ({ root, path }) => confirm({ root, files: [path] }),
      commit: async request => {
        const receipt = await ports.commit(request);
        this.#pendingReceipt = receipt?.file;
        return receipt;
      },
    });
    this.#test = ports.test;
    this.#diff = ports.diff;
    if (restore) {
      if (restore.root !== source || restore.fingerprint !== this.#fingerprint() || !Array.isArray(restore.edits))
        throw Error('SESSION_PROJECT_CHANGED');
      const seen = new Set();
      for (const { path, text } of restore.edits) {
        if (seen.has(path)) throw Error('SESSION_INVALID');
        seen.add(path);
        this.#review.model.stageEdit(path, text);
      }
    }
  }

  #fingerprint() {
    return hash({ root: this.#snapshot.root, rootIdentity: this.#snapshot.rootIdentity,
      files: [...this.#base.values()].map(({ path, identity, data }) => [path, identity, data]) });
  }
  #assertIdle() { if (this.#busy || this.#draft) throw Error('PROJECT_TASK_BUSY'); }
  #changes() {
    return this.#review.model.testSnapshot().filter(file => file.data !== this.#base.get(file.path).data);
  }
  list() { return [...this.#base.keys()]; }
  read(name) { return this.#review.model.read(name); }
  checkpoint() {
    return { root: this.#snapshot.root, fingerprint: this.#fingerprint(),
      edits: this.#changes().map(file => ({ path: file.path, text: text.decode(decode(file.data)) })) };
  }
  changeStatus() { return { count: this.#changes().length }; }
  stageEdit(name, content) {
    this.#assertIdle();
    this.#review.model.stageEdit(name, content);
  }
  async #run(name, files) {
    if (this.#busy) throw Error('PROJECT_TASK_BUSY');
    validateProjectTestInput({ name, files });
    this.#busy = true;
    try { return await this.#test({ name, files }); }
    finally { this.#busy = false; }
  }
  runTest(name) {
    this.#assertIdle();
    return this.#run(name, this.#review.model.testSnapshot());
  }
  openDraft() {
    this.#assertIdle();
    this.#draft = true;
    const files = new Map(this.#review.model.testSnapshot().map(file => [file.path, file.data]));
    const added = new Map();
    let total = [...files.values()].reduce((sum, data) => sum + decode(data).length, 0);
    let open = true;
    const active = () => { if (!open) throw Error('PROJECT_DRAFT_CLOSED'); };
    const read = name => {
      active();
      if (!files.has(name)) throw Error('PROJECT_FILE_NOT_FOUND');
      const data = decode(files.get(name));
      if (data.includes(0)) throw Error('PROJECT_FILE_NOT_TEXT');
      try { return text.decode(data); }
      catch { throw Error('PROJECT_FILE_NOT_TEXT'); }
    };
    const close = () => { active(); open = this.#draft = false; };
    return Object.freeze({
      draft: Object.freeze({
        read,
        stageEdit: (name, content) => {
          read(name);
          if (this.#busy) throw Error('PROJECT_TASK_BUSY');
          if (typeof content !== 'string' || content.includes('\0') || Buffer.byteLength(content) > maxFile)
            throw Error('PROJECT_DATA_INVALID');
          const bytes = Buffer.from(content);
          const size = total - decode(files.get(name)).length + bytes.length;
          if (size > maxTotal) throw Error('PROJECT_LIMIT');
          total = size;
          files.set(name, bytes.toString('base64'));
          added.set(name, text.decode(bytes));
        },
        runTest: name => {
          active();
          return this.#run(name, [...files].map(([path, data]) => ({ path, data })));
        },
      }),
      discard: close,
      commit: () => {
        active();
        if (this.#busy) throw Error('PROJECT_TASK_BUSY');
        const edits = [...added].map(([name, content]) => ({
          name, content, delta: Buffer.byteLength(content) - Buffer.byteLength(this.#review.model.read(name)),
        })).sort((a, b) => a.delta - b.delta);
        for (const { name, content } of edits) this.#review.model.stageEdit(name, content);
        close();
      },
    });
  }
  prepareReview() {
    this.#assertIdle();
    const changes = this.#changes();
    this.#last = { id: randomBytes(16).toString('hex'), changes };
    return { id: this.#last.id, files: changes.map(({ path }) => ({ path, kind: 'modified' })) };
  }
  diff(id, path, offset = 0) {
    const change = this.#last?.id === id && this.#last.changes.find(file => file.path === path);
    if (!change || !Number.isSafeInteger(offset) || offset < 0) throw Error('INVALID_SELECTION');
    const detail = this.#diff(this.#base.get(path).data, change.data);
    const end = offset + 131072;
    return { path, offset, text: detail.slice(offset, end), nextOffset: end < detail.length ? end : null, total: detail.length };
  }
  async approve(id, paths) {
    this.#assertIdle();
    if (!Array.isArray(paths) || paths.length !== 1) throw Error('REVIEW_FILE_LIMIT');
    if (!this.#last || this.#last.id !== id) throw Error('REVIEW_ALREADY_USED');
    const last = this.#last;
    this.#last = undefined;
    const name = paths[0];
    if (!last.changes.some(file => file.path === name)) throw Error('INVALID_SELECTION');
    if (hash(last.changes) !== hash(this.#changes())) throw Error('REVIEW_STALE');
    this.#busy = true;
    try {
      const chosen = this.#review.local.prepare(name);
      const result = await this.#review.local.approve(chosen.id);
      if (result.status === 'committed') {
        const { path, data, identity } = this.#pendingReceipt;
        this.#base.set(name, { path, data, identity });
      }
      return result;
    } finally {
      this.#busy = false;
      this.#pendingReceipt = undefined;
    }
  }
  #pendingReceipt;
}
