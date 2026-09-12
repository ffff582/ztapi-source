import {
  createBrowserRouter,
  createMemoryRouter,
  type RouteObject,
} from 'react-router-dom';
import { ProtectedRoute, PublicOnlyRoute } from '../auth/session';
import { ConsoleShell } from '../components/layout/ConsoleShell';
import { LoginPage } from '../features/auth/LoginPage';
import { RegisterPage } from '../features/auth/RegisterPage';
import { DashboardPage } from '../features/dashboard/DashboardPage';
import { UsageGuidePage } from '../features/guide/UsageGuidePage';
import { KeysPage } from '../features/keys/KeysPage';
import { LogsPage } from '../features/logs/LogsPage';
import { ModelsPage } from '../features/models/ModelsPage';
import { SupportedModelsPage } from '../features/models/SupportedModelsPage';
import { WalletPage } from '../features/wallet/WalletPage';

const routes: RouteObject[] = [
  {
    path: '/',
    HydrateFallback: () => null,
    lazy: async () => {
      const { HomePage } = await import('../features/home/HomePage');

      return { Component: HomePage };
    },
  },
  {
    path: '/models',
    element: <ModelsPage />,
  },
  {
    path: '/login',
    element: (
      <PublicOnlyRoute>
        <LoginPage />
      </PublicOnlyRoute>
    ),
  },
  {
    path: '/register',
    element: (
      <PublicOnlyRoute>
        <RegisterPage />
      </PublicOnlyRoute>
    ),
  },
  {
    path: '/console',
    element: (
      <ProtectedRoute>
        <ConsoleShell />
      </ProtectedRoute>
    ),
    children: [
      {
        index: true,
        element: <DashboardPage />,
      },
      {
        path: 'keys',
        element: <KeysPage />,
      },
      {
        path: 'models',
        element: <SupportedModelsPage />,
      },
      {
        path: 'guide',
        element: <UsageGuidePage />,
      },
      {
        path: 'logs',
        element: <LogsPage />,
      },
      {
        path: 'wallet',
        element: <WalletPage />,
      },
    ],
  },
];

export function createZTAPIRouter(initialEntries?: string[]) {
  return initialEntries === undefined
    ? createBrowserRouter(routes)
    : createMemoryRouter(routes, { initialEntries });
}
