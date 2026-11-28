import { useCallback, useEffect, useRef, useState } from 'react';
import { getAuthSession } from '../../api/client';
import type { UserToken } from '../../api/contracts';
import { fetchWorkbenchKeys, keyReadiness } from '../../api/workbench-readiness';

export function useWorkbenchKeys(userID: number | undefined) {
  const [snapshot, setSnapshot] = useState<{ ownerID?: number; tokens: UserToken[]; status: 'loading' | 'ready' | 'error' }>({ tokens: [], status: 'loading' });
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  const generation = useRef(0);
  const refresh = useCallback(async () => {
    const sequence = ++generation.current;
    setSnapshot({ ownerID: userID, tokens: [], status: 'loading' });
    if (userID === undefined || getAuthSession()?.user.id !== userID) return false;
    try {
      const tokens = await fetchWorkbenchKeys();
      if (sequence !== generation.current || getAuthSession()?.user.id !== userID) return false;
      setSnapshot({ ownerID: userID, tokens, status: 'ready' });
      setNow(Math.floor(Date.now() / 1000));
      return keyReadiness(tokens).status === 'available';
    } catch {
      if (sequence === generation.current && getAuthSession()?.user.id === userID) setSnapshot({ ownerID: userID, tokens: [], status: 'error' });
      return false;
    }
  }, [userID]);

  useEffect(() => {
    void refresh();
    const focus = () => { void refresh(); };
    window.addEventListener('focus', focus);
    return () => { generation.current += 1; window.removeEventListener('focus', focus); };
  }, [refresh]);

  useEffect(() => {
    if (snapshot.status !== 'ready') return;
    const expiry = snapshot.tokens.filter((token) => token.status === 1 && token.expired_time >= now).map((token) => token.expired_time);
    if (expiry.length === 0) return;
    const timer = window.setTimeout(() => setNow(Math.floor(Date.now() / 1000)), Math.min(2_147_483_647, Math.max(1, (Math.min(...expiry) + 1) * 1000 - Date.now())));
    return () => window.clearTimeout(timer);
  }, [now, snapshot]);

  const current = snapshot.ownerID === userID && userID !== undefined;
  const readiness = keyReadiness(current ? snapshot.tokens : [], now);
  const status = !current ? 'loading' : snapshot.status === 'ready' ? readiness.status : snapshot.status;
  return { status, canSubmit: status === 'available', canQuery: current && snapshot.status === 'ready' && readiness.canQuery, refresh };
}
