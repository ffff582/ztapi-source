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
import { render, screen, waitFor } from '@testing-library/react';
import { AdminAuthProvider } from './AdminAuthProvider';
import AdminGuard from './AdminGuard';
import { clearAdminSession, setAdminSession } from './admin-session';

function sessionFixture(role) {
  return {
    access_token: `token-${role}`,
    expires_in: 3600,
    user: { id: 7, username: 'operator', role, group: 'default' },
  };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderGuard(props = {}) {
  return render(
    <AdminAuthProvider>
      <AdminGuard {...props}>
        <div>authorized content</div>
      </AdminGuard>
    </AdminAuthProvider>,
  );
}

describe('AdminGuard', () => {
  beforeEach(() => {
    clearAdminSession();
    localStorage.clear();
    sessionStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('shows a loading state while the refresh cookie is checked', async () => {
    let resolveRefresh;
    vi.stubGlobal(
      'fetch',
      vi.fn(
        () =>
          new Promise((resolve) => {
            resolveRefresh = resolve;
          }),
      ),
    );

    renderGuard();

    expect(
      screen.getByText('Checking administrator session...'),
    ).toBeInTheDocument();
    await waitFor(() => expect(resolveRefresh).toEqual(expect.any(Function)));
    resolveRefresh(
      jsonResponse({ success: false, message: 'unauthorized' }, 401),
    );
    await waitFor(() =>
      expect(
        screen.getByText('Administrator sign-in required.'),
      ).toBeInTheDocument(),
    );
  });

  it('renders the unauthenticated state after refresh is rejected', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ success: false, message: 'unauthorized' }, 401),
        ),
    );

    renderGuard();

    await waitFor(() =>
      expect(
        screen.getByText('Administrator sign-in required.'),
      ).toBeInTheDocument(),
    );
  });

  it('renders forbidden for a regular user session', async () => {
    setAdminSession(sessionFixture(1));
    vi.stubGlobal('fetch', vi.fn());

    renderGuard();

    expect(
      screen.getByText('Administrator access required.'),
    ).toBeInTheDocument();
  });

  it('renders children for an authorized administrator session', () => {
    setAdminSession(sessionFixture(100));
    vi.stubGlobal('fetch', vi.fn());

    renderGuard({ permission: 'overview.read' });

    expect(screen.getByText('authorized content')).toBeInTheDocument();
  });

  it.each([
    ['omitted permission', {}],
    ['empty permission', { permission: '' }],
    ['unknown permission', { permission: 'not.a.permission' }],
  ])('denies root access for %s', (_, props) => {
    setAdminSession(sessionFixture(100));
    vi.stubGlobal('fetch', vi.fn());

    renderGuard(props);

    expect(
      screen.getByText('Administrator access required.'),
    ).toBeInTheDocument();
    expect(screen.queryByText('authorized content')).toBeNull();
  });
});
