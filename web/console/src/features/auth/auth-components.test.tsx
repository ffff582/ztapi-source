import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';
import { PasswordField } from './PasswordField';

describe('shared authentication components', () => {
  it('renders one task-focused authentication workspace', () => {
    render(
      <MemoryRouter>
        <AuthShell
          eyebrow="Console access"
          title="登录 ZTAPI"
          intro="管理 API 调用。"
        >
          <form aria-label="登录表单" />
        </AuthShell>
      </MemoryRouter>,
    );

    expect(screen.getAllByRole('main')).toHaveLength(1);
    expect(
      screen.getByRole('heading', { name: '登录 ZTAPI' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '返回首页' })).toHaveAttribute(
      'href',
      '/',
    );
    expect(screen.getByText('https://ztapi.vip/v1')).toBeInTheDocument();
  });

  it('toggles password visibility without changing its value', () => {
    const change = vi.fn();
    render(
      <PasswordField
        id="password"
        label="密码"
        name="password"
        autoComplete="current-password"
        value="correct-horse"
        onChange={change}
      />,
    );

    const input = screen.getByLabelText('密码');
    expect(input).toHaveAttribute('type', 'password');

    fireEvent.click(screen.getByRole('button', { name: '显示密码' }));

    expect(input).toHaveAttribute('type', 'text');
    expect(input).toHaveValue('correct-horse');
    expect(
      screen.getByRole('button', { name: '隐藏密码' }),
    ).toBeInTheDocument();
  });

  it('preserves password manager and accessibility input attributes', () => {
    render(
      <PasswordField
        id="new-password"
        label="密码"
        name="password"
        autoComplete="new-password"
        value="correct-horse"
        aria-describedby="password-help"
        aria-invalid
        onChange={() => undefined}
      />,
    );

    const input = screen.getByLabelText('密码');
    expect(input).toHaveAttribute('name', 'password');
    expect(input).toHaveAttribute('autocomplete', 'new-password');
    expect(input).toHaveAttribute('aria-describedby', 'password-help');
    expect(input).toHaveAttribute('aria-invalid', 'true');
  });

  it('does not render an empty error notice', () => {
    const { rerender } = render(<InlineNotice message={null} />);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();

    rerender(<InlineNotice message="服务暂不可用，请稍后重试" />);

    expect(screen.getByRole('alert')).toHaveTextContent(
      '服务暂不可用，请稍后重试',
    );
  });
});
