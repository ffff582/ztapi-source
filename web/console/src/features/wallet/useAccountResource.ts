import { useCallback, useEffect, useState } from 'react';

type Resource<T> =
  | { status: 'loading' | 'error'; data: null }
  | { status: 'ready'; data: T };

export function useAccountResource<T>(
  load: (signal: AbortSignal) => Promise<T>,
  refreshKey = '',
) {
  const [resource, setResource] = useState<Resource<T>>({ status: 'loading', data: null });
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((value) => value + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    setResource({ status: 'loading', data: null });
    void load(controller.signal).then((data) => {
      if (!controller.signal.aborted) setResource({ status: 'ready', data });
    }).catch(() => {
      if (!controller.signal.aborted) setResource({ status: 'error', data: null });
    });
    return () => controller.abort();
  }, [load, refreshKey, revision]);

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') refresh();
    };
    window.addEventListener('focus', onVisible);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.removeEventListener('focus', onVisible);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [refresh]);

  return { ...resource, refresh };
}
