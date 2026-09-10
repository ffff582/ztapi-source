import { useCallback, useEffect, useRef, useState } from 'react';
import { apiClient } from '../../api/client';
import {
  parseUserTokenPage,
  type PageEnvelope,
  type UserToken,
} from '../../api/contracts';

const TOKEN_PAGE_SIZE = 100;

type TokenLoadStatus = 'loading' | 'ready' | 'error';
type TokenPageUpdater = (
  current: PageEnvelope<UserToken>,
) => PageEnvelope<UserToken> | null;

export function useTokenPageCoordinator() {
  const [pageNumber, setPageNumber] = useState(1);
  const [page, setPage] = useState<PageEnvelope<UserToken> | null>(null);
  const [loadStatus, setLoadStatus] = useState<TokenLoadStatus>('loading');
  const currentPageNumberRef = useRef(1);
  const currentVisitIdRef = useRef(1);
  const currentPageRef = useRef<PageEnvelope<UserToken> | null>(null);
  const nextSequenceRef = useRef(0);
  const lastTerminalByVisitRef = useRef(new Map<number, number>());
  const activeControllersRef = useRef(
    new Map<AbortController, number>(),
  );
  const mountedRef = useRef(true);

  useEffect(() => {
    const activeControllers = activeControllersRef.current;
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeControllers.forEach((_visitId, controller) => {
        controller.abort();
      });
      activeControllers.clear();
    };
  }, []);

  const commitResponse = useCallback(
    (
      requestedPage: number,
      visitId: number,
      requestId: number,
      value: PageEnvelope<UserToken>,
    ) => {
      const lastTerminal =
        lastTerminalByVisitRef.current.get(visitId) ?? 0;
      if (
        !mountedRef.current ||
        currentPageNumberRef.current !== requestedPage ||
        currentVisitIdRef.current !== visitId ||
        requestId < lastTerminal
      ) {
        return false;
      }

      lastTerminalByVisitRef.current.set(visitId, requestId);
      currentPageRef.current = value;
      setPage(value);
      setLoadStatus('ready');
      return true;
    },
    [],
  );

  const loadPage = useCallback(
    async (requestedPage: number, foreground: boolean) => {
      if (currentPageNumberRef.current !== requestedPage) {
        return;
      }

      const visitId = currentVisitIdRef.current;
      const requestId = ++nextSequenceRef.current;
      const controller = new AbortController();
      activeControllersRef.current.set(controller, visitId);
      if (foreground) {
        setLoadStatus('loading');
      }

      try {
        const value = await apiClient.get<unknown>(
          `/token/?p=${requestedPage}&page_size=${TOKEN_PAGE_SIZE}`,
          { signal: controller.signal },
        );
        if (controller.signal.aborted) {
          return;
        }
        commitResponse(
          requestedPage,
          visitId,
          requestId,
          parseUserTokenPage(value),
        );
      } catch {
        if (controller.signal.aborted) {
          return;
        }
        const lastTerminal =
          lastTerminalByVisitRef.current.get(visitId) ?? 0;
        if (
          foreground &&
          mountedRef.current &&
          currentPageNumberRef.current === requestedPage &&
          currentVisitIdRef.current === visitId &&
          requestId >= lastTerminal
        ) {
          lastTerminalByVisitRef.current.set(visitId, requestId);
          setLoadStatus('error');
        }
      } finally {
        activeControllersRef.current.delete(controller);
      }
    },
    [commitResponse],
  );

  useEffect(() => {
    void loadPage(pageNumber, true);
  }, [loadPage, pageNumber]);

  const navigate = useCallback((requestedPage: number) => {
    const nextPage = Math.max(1, requestedPage);
    if (nextPage === currentPageNumberRef.current) {
      return;
    }

    const previousVisitId = currentVisitIdRef.current;
    currentPageNumberRef.current = nextPage;
    currentVisitIdRef.current += 1;
    activeControllersRef.current.forEach((visitId, controller) => {
      if (visitId === previousVisitId) {
        controller.abort();
        activeControllersRef.current.delete(controller);
      }
    });
    setLoadStatus('loading');
    setPageNumber(nextPage);
  }, []);

  const revalidate = useCallback(
    (requestedPage: number) => {
      void loadPage(requestedPage, false);
    },
    [loadPage],
  );

  const commitMutation = useCallback(
    (capturedPage: number, updater: TokenPageUpdater) => {
      const current = currentPageRef.current;
      if (
        !mountedRef.current ||
        currentPageNumberRef.current !== capturedPage ||
        current === null ||
        current.page !== capturedPage
      ) {
        return null;
      }

      const updated = updater(current);
      if (updated === null) {
        return null;
      }

      const commitId = ++nextSequenceRef.current;
      lastTerminalByVisitRef.current.set(
        currentVisitIdRef.current,
        commitId,
      );
      currentPageRef.current = updated;
      setPage(updated);
      setLoadStatus('ready');
      return updated;
    },
    [],
  );

  return {
    pageNumber,
    page,
    loadStatus,
    navigate,
    revalidate,
    commitMutation,
  };
}
