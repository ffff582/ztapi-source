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
  it('separates console navigation from the workbench mode', () => {
    render(<MemoryRouter><ConsoleShell /></MemoryRouter>);

    const workspaceGroup = screen.getByRole('group', { name: '控制台' });
    const accountGroup = screen.getByRole('group', { name: '账户与数据' });
    expect(within(workspaceGroup).getByRole('link', { name: '看板' })).toHaveAttribute('href', '/console');
    expect(within(workspaceGroup).getByRole('link', { name: '模型市场' })).toHaveAttribute('href', '/console/models');
    expect(within(workspaceGroup).getByRole('link', { name: 'API 文档' })).toHaveAttribute('href', '/console/guide');
    expect(within(accountGroup).getByRole('link', { name: 'API Key' })).toHaveAttribute('href', '/console/keys');
    expect(within(accountGroup).getByRole('link', { name: '用量日志' })).toHaveAttribute('href', '/console/logs');
    expect(within(accountGroup).getByRole('link', { name: '账单充值' })).toHaveAttribute('href', '/console/wallet');
    expect(within(accountGroup).getByRole('link', { name: '账号设置' })).toHaveAttribute('href', '/console/account');
    const productMode = screen.getByRole('group', { name: '产品模式' });
    expect(within(productMode).getByRole('link', { name: '控制台' })).toHaveAttribute('href', '/console');
    expect(within(productMode).getByRole('link', { name: '工作台' })).toHaveAttribute('href', '/console/workbench/text');
    expect(screen.getByRole('group', { name: '语言' })).toBeInTheDocument();
    expect(screen.getByRole('group', { name: '主题' })).toBeInTheDocument();
  });
});
