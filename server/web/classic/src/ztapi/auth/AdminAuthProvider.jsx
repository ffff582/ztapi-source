import React, {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import {
  adminLogin,
  adminLogout,
  adminRefresh,
  getAdminSession,
  subscribeAdminSession,
} from './admin-session';

export const AdminAuthContext = createContext(null);

export function AdminAuthProvider(props) {
  const initialSession = getAdminSession();
  const [state, setState] = useState({
    status: initialSession ? 'authenticated' : 'loading',
    session: initialSession,
  });

  useEffect(() => {
    const unsubscribe = subscribeAdminSession((session) => {
      setState({
        status: session ? 'authenticated' : 'unauthenticated',
        session,
      });
    });

    if (!getAdminSession()) {
      adminRefresh().catch(() => {
        setState({ status: 'unauthenticated', session: null });
      });
    }

    return unsubscribe;
  }, []);

  const value = useMemo(
    () => ({
      ...state,
      login: adminLogin,
      refresh: adminRefresh,
      logout: adminLogout,
    }),
    [state],
  );

  return (
    <AdminAuthContext.Provider value={value}>
      {props.children}
    </AdminAuthContext.Provider>
  );
}

export function useAdminAuth() {
  const context = useContext(AdminAuthContext);
  if (!context) {
    throw new Error('useAdminAuth must be used within AdminAuthProvider.');
  }
  return context;
}
