import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConsoleShell } from './ConsoleShell';

vi.mock('../../auth/session', () => ({
  useAuth: () => ({
    user: { username: 'alice' },
    logout: vi.fn(),
  }),
}));

afterEach(cleanup);

describe('ConsoleShell navigation', () => {
  it('groups the first-class call and account workflows without changing destinations', () => {
    render(<MemoryRouter><ConsoleShell /></MemoryRouter>);

    const callGroup = screen.getByRole('group', { name: '调用与测试' });
    const accountGroup = screen.getByRole('group', { name: '账户与数据' });
    expect(within(callGroup).getByRole('link', { name: '概览' })).toHaveAttribute('href', '/console');
    expect(within(callGroup).getByRole('link', { name: '模型支持' })).toHaveAttribute('href', '/console/models');
    expect(within(callGroup).getByRole('link', { name: '在线测试' })).toHaveAttribute('href', '/console/test');
    expect(within(callGroup).getByRole('link', { name: '使用说明' })).toHaveAttribute('href', '/console/guide');
    expect(within(accountGroup).getByRole('link', { name: 'API 密钥' })).toHaveAttribute('href', '/console/keys');
    expect(within(accountGroup).getByRole('link', { name: '使用日志' })).toHaveAttribute('href', '/console/logs');
    expect(within(accountGroup).getByRole('link', { name: '余额充值' })).toHaveAttribute('href', '/console/wallet');
    expect(within(accountGroup).getByRole('link', { name: '账号设置' })).toHaveAttribute('href', '/console/account');
  });
});
