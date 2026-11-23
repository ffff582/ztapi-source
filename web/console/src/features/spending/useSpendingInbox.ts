import { useSyncExternalStore } from 'react';
import { getAuthSession, getAuthSessionLifecycle, subscribeAuthSession } from '../../api/client';
import { spendingApi, SpendingError, type SpendingInbox } from '../../api/spending';

const POLL_MS = 60_000;
export const spendingSessionKey = () => `${getAuthSessionLifecycle()}:${getAuthSession()?.user.id ?? 'none'}`;
export function useSpendingSessionKey() {
  return useSyncExternalStore(subscribeAuthSession, spendingSessionKey);
}
type InboxState = { status: 'idle' | 'loading' | 'ready' | 'error' | 'session-expired'; data: SpendingInbox | null; reading: readonly number[]; readError: boolean };
const empty = (): InboxState => ({ status: getAuthSession() ? 'loading' : 'idle', data: null, reading: [], readError: false });
let state = empty();
let identity = spendingSessionKey();
let lastStarted = -Infinity;
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight: AbortController | undefined;
const acknowledgements = new Map<number, string>();
const writes = new Map<number, AbortController>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((listener) => listener());
function clearTimer() { clearTimeout(timer); timer = undefined; }
function resetOwner() {
  const next = spendingSessionKey();
  if (next === identity) return;
  identity = next;
  clearTimer();
  inflight?.abort(); inflight = undefined;
  writes.forEach((controller) => controller.abort()); writes.clear();
  acknowledgements.clear();
  lastStarted = -Infinity;
  state = empty(); emit();
  if (listeners.size) schedule();
}
function schedule() {
  clearTimer();
  if (!listeners.size || !getAuthSession() || document.visibilityState !== 'visible' || inflight) return;
  const delay = Math.max(0, POLL_MS - (Date.now() - lastStarted));
  if (delay === 0) void poll();
  else timer = setTimeout(() => { void poll(); }, delay);
}
async function poll() {
  if (inflight || !getAuthSession() || document.visibilityState !== 'visible') return;
  const owner = identity;
  const controller = new AbortController();
  inflight = controller;
  lastStarted = Date.now();
  try {
    const data = await spendingApi.alerts(controller.signal);
    if (controller.signal.aborted || owner !== spendingSessionKey()) return;
    const newlyRead = data.items.filter((item) => !item.read_at && acknowledgements.has(item.id)).length;
    state = { ...state, status: 'ready', data: { items: data.items.map((item) => acknowledgements.has(item.id) ? { ...item, read_at: acknowledgements.get(item.id) } : item), unread_count: Math.max(0, data.unread_count - newlyRead) } };
  } catch (error) {
    if (!controller.signal.aborted && owner === spendingSessionKey()) state = { ...state, status: error instanceof SpendingError && error.status === 401 ? 'session-expired' : 'error', data: null };
  } finally {
    if (inflight === controller) {
      inflight = undefined;
      emit(); schedule();
    }
  }
}

async function readAlert(id: number) {
  if (!getAuthSession() || writes.has(id) || !state.data?.items.some((item) => item.id === id && !item.read_at)) return;
  const owner = identity;
  const controller = new AbortController();
  writes.set(id, controller);
  state = { ...state, readError: false, reading: [...writes.keys()] }; emit();
  try {
    await spendingApi.read(id, controller.signal);
    if (controller.signal.aborted || owner !== spendingSessionKey()) return;
    const at = new Date().toISOString();
    acknowledgements.set(id, at);
    if (state.data) {
      const wasUnread = state.data.items.some((item) => item.id === id && !item.read_at);
      state = { ...state, data: { items: state.data.items.map((item) => item.id === id ? { ...item, read_at: at } : item), unread_count: Math.max(0, state.data.unread_count - Number(wasUnread)) } };
    }
  } catch (error) {
    if (!controller.signal.aborted && owner === spendingSessionKey()) state = error instanceof SpendingError && error.status === 401
      ? { ...state, status: 'session-expired', data: null, readError: false }
      : { ...state, readError: true };
  } finally {
    if (writes.get(id) === controller) {
      writes.delete(id); state = { ...state, reading: [...writes.keys()] }; emit();
    }
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  if (listeners.size === 1) {
    document.addEventListener('visibilitychange', schedule);
    resetOwner(); schedule();
  }
  return () => {
    listeners.delete(listener);
    if (!listeners.size) {
      clearTimer();
      document.removeEventListener('visibilitychange', schedule);
      // A started read has a bounded API timeout. Keep it through StrictMode's
      // unsubscribe/resubscribe instead of losing the first result for 60s.
      writes.forEach((controller) => controller.abort()); writes.clear();
      state = { ...state, reading: [] };
    }
  };
}
function snapshot() {
  return state;
}
// Session changes clear private memory even while no inbox is mounted.
subscribeAuthSession(resetOwner);
export function useSpendingInbox() {
  const inbox = useSyncExternalStore(subscribe, snapshot);
  return { ...inbox, read: readAlert };
}
