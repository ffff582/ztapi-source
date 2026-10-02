import React from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import LoginForm from './LoginForm';
import { UserContext } from '../../context/User';
import { StatusContext } from '../../context/Status';
import { userLogin } from '../../ztapi/auth/user-session';
import { setUserData, updateAPI } from '../../helpers';

vi.mock('../../ztapi/auth/user-session', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, userLogin: vi.fn() };
});

vi.mock('../../helpers', () => ({
  API: { get: vi.fn(), post: vi.fn() },
  buildAssertionResult: vi.fn(),
  getLogo: () => '/logo.png',
  getOAuthProviderIcon: vi.fn(),
  getSystemName: () => 'ZTAPI',
  isPasskeySupported: vi.fn().mockResolvedValue(false),
  onCustomOAuthClicked: vi.fn(),
  onDiscordOAuthClicked: vi.fn(),
  onGitHubOAuthClicked: vi.fn(),
  onLinuxDOOAuthClicked: vi.fn(),
  onOIDCClicked: vi.fn(),
  prepareCredentialRequestOptions: vi.fn(),
  setUserData: vi.fn(),
  showError: vi.fn(),
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
  updateAPI: vi.fn(),
}));

describe('LoginForm ZTAPI session wiring', () => {
  it('uses the new login session and persists only public user metadata', async () => {
    const dispatch = vi.fn();
    userLogin.mockResolvedValue({
      access_token: 'memory-only-secret',
      expires_in: 900,
      user: {
        id: 23,
        username: 'customer',
        role: 1,
        group: 'default',
      },
    });

    render(
      <MemoryRouter>
        <StatusContext.Provider
          value={[{ status: { self_use_mode_enabled: true } }, vi.fn()]}
        >
          <UserContext.Provider value={[{}, dispatch]}>
            <LoginForm />
          </UserContext.Provider>
        </StatusContext.Provider>
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByLabelText('用户名或邮箱'), {
      target: { value: 'customer' },
    });
    fireEvent.change(screen.getByLabelText('密码'), {
      target: { value: 'customer-password' },
    });
    fireEvent.click(screen.getByRole('button', { name: '继续' }));

    await waitFor(() => {
      expect(userLogin).toHaveBeenCalledWith({
        username: 'customer',
        password: 'customer-password',
      });
    });
    const publicUser = {
      id: 23,
      username: 'customer',
      role: 1,
      group: 'default',
    };
    expect(dispatch).toHaveBeenCalledWith({ type: 'login', payload: publicUser });
    expect(setUserData).toHaveBeenCalledWith(publicUser);
    expect(updateAPI).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(publicUser)).not.toContain('memory-only-secret');
  });
});
