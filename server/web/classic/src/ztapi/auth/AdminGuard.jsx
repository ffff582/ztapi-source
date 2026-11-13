import React from 'react';
import { Navigate } from 'react-router-dom';
import { hasAdminPermission } from './admin-permission-policy';
import { useAdminAuth } from './AdminAuthProvider';

export default function AdminGuard(props) {
  const auth = useAdminAuth();

  if (auth.status === 'loading') {
    return <div role='status'>Checking administrator session...</div>;
  }

  if (!auth.session) {
    if (props.redirectToLogin) {
      return <Navigate to='/login' replace />;
    }
    return <div role='alert'>Administrator sign-in required.</div>;
  }

  if (!hasAdminPermission(auth.session.user.role, props.permission)) {
    return (
      <div role='alert'>
        {props.forbiddenMessage || 'Administrator access required.'}
      </div>
    );
  }

  return props.children;
}
