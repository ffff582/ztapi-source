import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type PropsWithChildren,
  type ReactNode,
} from 'react';
import { Navigate } from 'react-router-dom';
import {
  login as loginRequest,
  logoutSession,
  refreshSession,
  register as registerRequest,
  subscribeAuthSession,
} from '../api/client';
import type { AuthCredentials, AuthUser } from '../api/contracts';

type AuthState =
  | { status: 'loading'; user: null }
  | { status: 'unauthenticated'; user: null }
  | { status: 'authenticated'; user: AuthUser };

type AuthContextValue = AuthState & {
  login: (credentials: AuthCredentials) => Promise<void>;
  register: (credentials: AuthCredentials) => Promise<void>;
  logout: () => Promise<void>;
};

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: PropsWithChildren) {
  const [state, setState] = useState<AuthState>({
    status: 'loading',
    user: null,
  });

  useEffect(() => {
    let active = true;
    const unsubscribe = subscribeAuthSession((user) => {
      if (!active) {
        return;
      }

      setState(
        user === null
          ? { status: 'unauthenticated', user: null }
          : { status: 'authenticated', user },
      );
    });

    void refreshSession().catch(() => {
      if (active) {
        setState({ status: 'unauthenticated', user: null });
      }
    });

    return () => {
      active = false;
      unsubscribe();
    };
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      ...state,
      login: async (credentials) => {
        await loginRequest(credentials);
      },
      register: async (credentials) => {
        await registerRequest(credentials);
      },
      logout: logoutSession,
    }),
    [state],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);

  if (context === null) {
    throw new Error('useAuth must be used within AuthProvider.');
  }

  return context;
}

function SessionLoading() {
  return (
    <main className="session-loading" aria-live="polite" aria-busy="true">
      <img src="/brand/ztapi-mark.png" alt="" width="24" height="24" />
      <span>正在确认会话...</span>
    </main>
  );
}

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const { status } = useAuth();

  if (status === 'loading') {
    return <SessionLoading />;
  }

  if (status === 'unauthenticated') {
    return <Navigate to="/login" replace />;
  }

  return children;
}

export function PublicOnlyRoute({ children }: { children: ReactNode }) {
  const { status } = useAuth();

  if (status === 'loading') {
    return <SessionLoading />;
  }

  if (status === 'authenticated') {
    return <Navigate to="/console" replace />;
  }

  return children;
}
