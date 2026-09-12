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
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import AdminApp from '../AdminApp.jsx';
import { clearAdminSession, setAdminSession } from '../auth/admin-session';

function sessionFixture(role) {
  return {
    access_token: `access-token-${role}`,
    expires_in: 3600,
    user: {
      id: 7,
      username: 'operator',
      role,
      group: 'default',
    },
  };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderAdminApp({ role, path = '/overview' } = {}) {
  window.history.pushState({}, '', path);
  if (role !== undefined) {
    setAdminSession(sessionFixture(role));
  }
  return render(<AdminApp />);
}

describe('ZTAPI administration shell', () => {
  beforeEach(() => {
    clearAdminSession();
    vi.stubGlobal(
      'fetch',
      vi.fn((url) => {
        if (String(url).startsWith('/api/admin/overview')) {
          return new Promise(() => {});
        }
        return Promise.resolve(new Response(null, { status: 204 }));
      }),
    );
  });

  afterEach(() => {
    cleanup();
    clearAdminSession();
    vi.unstubAllGlobals();
  });

  it('does not show channel or settings navigation to finance staff', () => {
    renderAdminApp({ role: 3 });

    expect(screen.getByRole('link', { name: '财务管理' })).toBeVisible();
    expect(screen.queryByRole('link', { name: '上游渠道' })).toBeNull();
    expect(screen.queryByRole('link', { name: '系统设置' })).toBeNull();
  });

  it('shows root-only settings and staff navigation to root operators', () => {
    renderAdminApp({ role: 100 });

    expect(screen.getByRole('link', { name: '系统设置' })).toBeVisible();
    expect(screen.getByRole('link', { name: '员工管理' })).toBeVisible();
    expect(screen.getByRole('link', { name: '审计日志' })).toBeVisible();
  });

  it('keeps root-only settings and staff navigation hidden from administrators', () => {
    renderAdminApp({ role: 10 });

    expect(screen.getByRole('link', { name: '上游渠道' })).toBeVisible();
    expect(screen.getByRole('link', { name: '审计日志' })).toBeVisible();
    expect(screen.queryByRole('link', { name: '系统设置' })).toBeNull();
    expect(screen.queryByRole('link', { name: '员工管理' })).toBeNull();
  });

  it('limits support staff to their permitted operational navigation', () => {
    renderAdminApp({ role: 2 });

    expect(screen.getByRole('link', { name: '用户管理' })).toBeVisible();
    expect(screen.getByRole('link', { name: '操作日志' })).toBeVisible();
    expect(screen.queryByRole('link', { name: '财务管理' })).toBeNull();
    expect(screen.queryByRole('link', { name: '审计日志' })).toBeNull();
  });

  it('marks the current route as active', async () => {
    renderAdminApp({ role: 10, path: '/channels' });

    expect(screen.getByRole('link', { name: '上游渠道' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    expect(await screen.findByText('暂无上游渠道。')).toBeVisible();
  });

  it('preserves a deep link while recovering an administrator session', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((url) => {
        if (String(url) === '/api/auth/refresh') {
          return Promise.resolve(
            jsonResponse({ success: true, data: sessionFixture(100) }),
          );
        }
        if (String(url).startsWith('/api/models/ztapi/pricing-preview')) {
          return Promise.resolve(
            jsonResponse({ success: true, data: { items: [] } }),
          );
        }
        if (String(url).startsWith('/api/models/ztapi/')) {
          return Promise.resolve(
            jsonResponse({
              success: true,
              data: { items: [], total: 0, page: 1, page_size: 20 },
            }),
          );
        }
        return Promise.resolve(new Response(null, { status: 204 }));
      }),
    );

    renderAdminApp({ path: '/models' });

    await waitFor(() =>
      expect(document.querySelector('#ztapi-model-title')).toBeInTheDocument(),
    );
    expect(window.location.pathname).toBe('/models');
    expect(document.querySelector('a[href="/models"]')).toHaveAttribute(
      'aria-current',
      'page',
    );
  });

  it('traps focus in the mobile drawer and restores it after navigation', async () => {
    renderAdminApp({ role: 10 });

    const trigger = screen.getByRole('button', { name: '打开导航' });
    trigger.focus();
    fireEvent.click(trigger);
    const drawer = screen.getByRole('dialog', { name: '管理导航' });
    const closeButton = screen.getByRole('button', { name: '关闭导航' });

    expect(drawer).toHaveAttribute('aria-modal', 'true');
    expect(closeButton).toHaveFocus();

    fireEvent.keyDown(drawer, { key: 'Tab', shiftKey: true });
    expect(
      screen
        .getAllByRole('link', { name: '审计日志' })
        .find((link) => drawer.contains(link)),
    ).toHaveFocus();

    fireEvent.keyDown(drawer, { key: 'Tab' });
    expect(closeButton).toHaveFocus();

    fireEvent.click(
      screen
        .getAllByRole('link', { name: '上游渠道' })
        .find((link) => drawer.contains(link)),
    );
    expect(screen.queryByRole('dialog', { name: '管理导航' })).toBeNull();
    expect(trigger).toHaveFocus();
    expect(await screen.findByText('暂无上游渠道。')).toBeVisible();
  });

  it('closes the mobile drawer with Escape and restores focus to the trigger', () => {
    renderAdminApp({ role: 10 });

    const trigger = screen.getByRole('button', { name: '打开导航' });
    trigger.focus();
    fireEvent.click(trigger);
    const drawer = screen.getByRole('dialog', { name: '管理导航' });

    fireEvent.keyDown(drawer, { key: 'Escape' });

    expect(screen.queryByRole('dialog', { name: '管理导航' })).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it('does not expose a notification control without a data source', () => {
    renderAdminApp({ role: 10 });

    expect(screen.queryByRole('button', { name: '通知' })).toBeNull();
  });

  it('opens and closes the account menu while retaining upstream attribution', () => {
    renderAdminApp({ role: 10 });

    const accountButton = screen.getByRole('button', {
      name: 'operator 账户菜单',
    });
    fireEvent.click(accountButton);

    expect(
      screen.getByRole('link', { name: '查看 AGPL-3.0 许可与上游归属' }),
    ).toHaveAttribute('href', 'https://github.com/QuantumNous/new-api');
    expect(
      screen.getByRole('link', { name: '查看 ZTAPI 对应源码' }),
    ).toHaveAttribute('href', '/.well-known/source');

    fireEvent.click(accountButton);
    expect(
      screen.queryByRole('link', { name: '查看 AGPL-3.0 许可与上游归属' }),
    ).toBeNull();
    expect(
      screen.queryByRole('link', { name: '查看 ZTAPI 对应源码' }),
    ).toBeNull();
  });

  it('clears the session and returns to sign-in after logout', async () => {
    renderAdminApp({ role: 10 });

    fireEvent.click(screen.getByRole('button', { name: 'operator 账户菜单' }));
    fireEvent.click(screen.getByRole('button', { name: '退出登录' }));

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: '管理员登录' })).toBeVisible(),
    );
  });

  it('renders a forbidden state for an authenticated role without route permission', () => {
    renderAdminApp({ role: 3, path: '/channels' });

    expect(screen.getByRole('alert')).toHaveTextContent('无权访问此页面');
  });

  it('renders a compact not-found page for unknown routes', () => {
    renderAdminApp({ role: 100, path: '/not-a-route' });

    expect(screen.getByRole('heading', { name: '未找到页面' })).toBeVisible();
  });

  it('redirects a known root operator from the root route to overview', async () => {
    renderAdminApp({ role: 100, path: '/' });

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: '概览' })).toBeVisible(),
    );
    expect(window.location.pathname).toBe('/overview');
  });

  it('moves a successful login to overview with replacement navigation', async () => {
    renderAdminApp({ path: '/login' });

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: '管理员登录' })).toBeVisible(),
    );
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ success: true, data: sessionFixture(100) }),
        ),
    );

    fireEvent.change(screen.getByRole('textbox', { name: '用户名' }), {
      target: { value: 'operator' },
    });
    fireEvent.change(screen.getByLabelText('密码'), {
      target: { value: 'password' },
    });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: '概览' })).toBeVisible(),
    );
    expect(window.location.pathname).toBe('/overview');
  });
});
