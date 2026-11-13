import { getAuthSession, getAuthSessionLifecycle, refreshSession } from './client';
import { parseUserTokenPage, type UserToken } from './contracts';
import { PlaygroundClientError } from './playground';

// Unlike generic console GETs, a workbench read must not retry under another account.
export async function workbenchGet(path: string, session = getAuthSession(), retry = true, lifecycle = getAuthSessionLifecycle()): Promise<unknown> {
  if (session === null || getAuthSession() !== session || getAuthSessionLifecycle() !== lifecycle) throw new PlaygroundClientError('unauthorized');
  const currentLogin = () => getAuthSessionLifecycle() === lifecycle && getAuthSession()?.user.id === session.user.id;
  let response: Response;
  try {
    response = await fetch(path, {
      method: 'GET', credentials: 'include', cache: 'no-store',
      headers: { Accept: 'application/json', Authorization: `Bearer ${session.access_token}`, 'New-Api-User': String(session.user.id) },
    });
  } catch {
    throw new PlaygroundClientError('unknown');
  }
  if (!currentLogin()) throw new PlaygroundClientError('unauthorized');
  if (response.status === 401 && retry) {
    if (getAuthSession() === session) {
      try { await refreshSession(); } catch { throw new PlaygroundClientError('unauthorized'); }
    }
    const refreshed = getAuthSession();
    if (refreshed === null || !currentLogin()) throw new PlaygroundClientError('unauthorized');
    return workbenchGet(path, refreshed, false, lifecycle);
  }
  if (!response.ok) throw new PlaygroundClientError(response.status === 401 ? 'unauthorized' : 'unknown');
  let body: unknown;
  try { body = await response.json(); } catch { throw new PlaygroundClientError('invalid_response'); }
  if (!currentLogin()) throw new PlaygroundClientError('unauthorized');
  return body;
}

export type WorkbenchKeyState = 'available' | 'none' | 'expired' | 'disabled' | 'exhausted' | 'unavailable';
export type WorkbenchKeyReadiness = { status: WorkbenchKeyState; canQuery: boolean };

export function keyReadiness(tokens: readonly UserToken[], now = Math.floor(Date.now() / 1000)): WorkbenchKeyReadiness {
  // preparePlayground considers only the newest 100, even if older keys are usable.
  const candidates = [...tokens].sort((a, b) => b.id - a.id).slice(0, 100);
  const enabled = candidates.filter((token) => token.status === 1 && (token.expired_time === -1 || token.expired_time >= now));
  const billing = enabled.find((token) => token.unlimited_quota) ?? enabled.find((token) => token.remain_quota > 0);
  if (billing !== undefined) return { status: 'available', canQuery: true };
  if (enabled.length > 0) return { status: 'exhausted', canQuery: true };
  if (candidates.length === 0) return { status: 'none', canQuery: false };
  if (candidates.every((token) => token.status === 2)) return { status: 'disabled', canQuery: false };
  if (candidates.every((token) => token.status === 3 || (token.status === 1 && token.expired_time !== -1 && token.expired_time < now))) {
    return { status: 'expired', canQuery: false };
  }
  if (candidates.every((token) => token.status === 4)) return { status: 'exhausted', canQuery: false };
  return { status: 'unavailable', canQuery: false };
}

export async function fetchWorkbenchKeys(): Promise<UserToken[]> {
  const session = getAuthSession();
  const body = await workbenchGet('/api/token/?p=1&page_size=100', session);
  if (typeof body !== 'object' || body === null || !('success' in body) || body.success !== true || !('data' in body)) {
    throw new PlaygroundClientError('invalid_response');
  }
  const page = parseUserTokenPage(body.data);
  const rawItems = (body.data as { items: Record<string, unknown>[] }).items;
  if (rawItems.some((item) => item.user_id !== undefined && item.user_id !== session?.user.id)) {
    throw new PlaygroundClientError('invalid_response');
  }
  // Never treat a truncated first page as a complete view of the backend's candidates.
  if (page.page !== 1 || page.items.length !== Math.min(page.total, 100)) throw new PlaygroundClientError('invalid_response');
  return page.items;
}
