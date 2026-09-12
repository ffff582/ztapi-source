/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import React from 'react';
import {
  BrowserRouter,
  Navigate,
  Route,
  Routes,
  useNavigate,
} from 'react-router-dom';
import { AdminAuthProvider, useAdminAuth } from './auth/AdminAuthProvider';
import AdminGuard from './auth/AdminGuard';
import AdminLoginPage from './auth/AdminLoginPage';
import AdminShell from './layout/AdminShell';
import {
  AdminPermission,
  AdminRole,
  hasAdminPermission,
} from './auth/admin-permission-policy';
import AdminOverviewPage from './features/overview/AdminOverviewPage.jsx';
import AdminChannelsPage from './features/channels/AdminChannelsPage.jsx';
import AdminModelsPage from './features/models/AdminModelsPage.jsx';
import AdminUsersPage from './features/users/AdminUsersPage.jsx';
import StaffRolesPage from './features/users/StaffRolesPage.jsx';
import OrdersPage from './features/finance/OrdersPage.jsx';
import LedgerPage from './features/finance/LedgerPage.jsx';
import ReconciliationPage from './features/finance/ReconciliationPage.jsx';
import AdminLogsPage from './features/logs/AdminLogsPage.jsx';
import AuditLogPage from './features/logs/AuditLogPage.jsx';
import AdminSettingsPage from './features/settings/AdminSettingsPage.jsx';
import './styles/admin.css';

function SectionPage({ title }) {
  return (
    <section className='ztapi-admin-page' aria-labelledby='ztapi-page-title'>
      <h1 id='ztapi-page-title'>{title}</h1>
      <p>此模块的业务内容将在后续管理功能中接入。</p>
    </section>
  );
}

function ProtectedPage({ permission, title, children }) {
  const auth = useAdminAuth();
  const navigate = useNavigate();

  if (auth.status === 'loading') {
    return <div role='status'>正在检查管理员会话...</div>;
  }

  if (!auth.session) {
    return <Navigate to='/login' replace />;
  }

  const logout = async () => {
    await auth.logout();
    navigate('/login', { replace: true });
  };

  return (
    <AdminGuard
      permission={permission}
      redirectToLogin
      forbiddenMessage='无权访问此页面'
    >
      <AdminShell user={auth.session.user} onLogout={logout}>
        {children || <SectionPage title={title} />}
      </AdminShell>
    </AdminGuard>
  );
}

function OverviewRedirect() {
  const auth = useAdminAuth();

  if (auth.status === 'loading') {
    return <div role='status'>正在检查管理员会话...</div>;
  }

  return <Navigate to={auth.session ? '/overview' : '/login'} replace />;
}

function LoginRoute() {
  const auth = useAdminAuth();
  const navigate = useNavigate();

  if (auth.status === 'loading') {
    return <div role='status'>正在检查管理员会话...</div>;
  }

  if (auth.session) {
    return <Navigate to='/overview' replace />;
  }

  return (
    <AdminLoginPage
      onSuccess={() => navigate('/overview', { replace: true })}
    />
  );
}

function NotFoundPage() {
  return (
    <section className='ztapi-admin-not-found'>
      <h1>未找到页面</h1>
      <p>请从管理导航中选择可访问的页面。</p>
    </section>
  );
}

function AdminRoutes() {
  const auth = useAdminAuth();
  const channelCanWrite = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.channelWrite,
  );
  const modelCanWrite = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.modelWrite,
  );
  const modelCanConfirmBelowCost = auth.session?.user?.role === AdminRole.root;
  const userCanChangeStatus = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.userStatusWrite,
  );
  const userCanAdjustBalance = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.balanceWrite,
  );
  const userCanViewLedger = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.financeRead,
  );
  const financeCanWrite = hasAdminPermission(
    auth.session?.user?.role,
    AdminPermission.financeWrite,
  );

  return (
    <Routes>
      <Route path='/' element={<OverviewRedirect />} />
      <Route path='/login' element={<LoginRoute />} />
      <Route
        path='/overview'
        element={
          <ProtectedPage permission={AdminPermission.overviewRead} title='概览'>
            <AdminOverviewPage />
          </ProtectedPage>
        }
      />
      <Route
        path='/channels'
        element={
          <ProtectedPage
            permission={AdminPermission.channelRead}
            title='上游渠道'
          >
            <AdminChannelsPage canWrite={channelCanWrite} />
          </ProtectedPage>
        }
      />
      <Route
        path='/models'
        element={
          <ProtectedPage
            permission={AdminPermission.modelRead}
            title='模型管理'
          >
            <AdminModelsPage
              canWrite={modelCanWrite}
              canConfirmBelowCost={modelCanConfirmBelowCost}
            />
          </ProtectedPage>
        }
      />
      <Route
        path='/users'
        element={
          <ProtectedPage permission={AdminPermission.userRead} title='用户管理'>
            <AdminUsersPage
              canAdjustBalance={userCanAdjustBalance}
              canChangeStaffStatus={auth.session?.user?.role === AdminRole.root}
              canChangeStatus={userCanChangeStatus}
              canViewLedger={userCanViewLedger}
            />
          </ProtectedPage>
        }
      />
      <Route
        path='/finance/orders'
        element={
          <ProtectedPage
            permission={AdminPermission.financeRead}
            title='财务管理'
          >
            <OrdersPage canWrite={financeCanWrite} />
          </ProtectedPage>
        }
      />
      <Route
        path='/finance/ledger'
        element={
          <ProtectedPage
            permission={AdminPermission.financeRead}
            title='余额账本'
          >
            <LedgerPage />
          </ProtectedPage>
        }
      />
      <Route
        path='/finance/reconciliation'
        element={
          <ProtectedPage
            permission={AdminPermission.financeRead}
            title='财务对账'
          >
            <ReconciliationPage canWrite={financeCanWrite} />
          </ProtectedPage>
        }
      />
      <Route
        path='/logs'
        element={
          <ProtectedPage permission={AdminPermission.logRead} title='操作日志'>
            <AdminLogsPage />
          </ProtectedPage>
        }
      />
      <Route
        path='/settings'
        element={
          <ProtectedPage
            permission={AdminPermission.systemWrite}
            title='系统设置'
          >
            <AdminSettingsPage />
          </ProtectedPage>
        }
      />
      <Route
        path='/staff'
        element={
          <ProtectedPage
            permission={AdminPermission.roleWrite}
            title='员工管理'
          >
            <StaffRolesPage />
          </ProtectedPage>
        }
      />
      <Route
        path='/audit'
        element={
          <ProtectedPage
            permission={AdminPermission.auditRead}
            title='审计日志'
          >
            <AuditLogPage />
          </ProtectedPage>
        }
      />
      <Route path='*' element={<NotFoundPage />} />
    </Routes>
  );
}

export default function AdminApp() {
  return (
    <AdminAuthProvider>
      <BrowserRouter
        future={{
          v7_startTransition: true,
          v7_relativeSplatPath: true,
        }}
      >
        <AdminRoutes />
      </BrowserRouter>
    </AdminAuthProvider>
  );
}
