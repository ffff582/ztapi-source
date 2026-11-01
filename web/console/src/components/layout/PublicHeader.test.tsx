import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { PublicHeader } from './PublicHeader';
import { LocaleProvider } from '../../i18n/locale';
import { ThemeProvider } from '../../theme/theme';

afterEach(cleanup);

function renderPublicHeader() {
  return render(
    <MemoryRouter>
      <LocaleProvider>
        <ThemeProvider>
          <PublicHeader />
        </ThemeProvider>
      </LocaleProvider>
    </MemoryRouter>,
  );
}

describe('PublicHeader shared controls', () => {
  it('exposes the authorized community link without an authenticated session', () => {
    renderPublicHeader();
    const community = screen.getByRole('link', { name: '用户交流群' });
    expect(community).toHaveAttribute('href', 'https://t.me/ztapivip');
    expect(community).toHaveAttribute('target', '_blank');
    expect(community).toHaveAttribute('rel', 'noopener noreferrer');
    expect(community.querySelector('svg')).toBeInTheDocument();
  });

  it('includes the community link in the public mobile menu and closes the menu on selection', () => {
    renderPublicHeader();
    fireEvent.click(screen.getByRole('button', { name: '打开导航菜单' }));
    expect(
      screen.getByRole('navigation', { name: '公共导航' }),
    ).toHaveAttribute('data-open', 'true');
    const community = screen.getByRole('link', { name: '用户交流群' });
    community.addEventListener('click', (event) => event.preventDefault());
    fireEvent.click(community);
    expect(
      screen.getByRole('navigation', { name: '公共导航' }),
    ).toHaveAttribute('data-open', 'false');
  });
  it('keeps language and theme controls visible on public pages', () => {
    render(
      <MemoryRouter>
        <LocaleProvider>
          <ThemeProvider>
            <PublicHeader />
          </ThemeProvider>
        </LocaleProvider>
      </MemoryRouter>,
    );

    expect(screen.getByRole('group', { name: '语言' })).toBeInTheDocument();
    expect(screen.getByRole('group', { name: '主题' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '亮系' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '暗系' })).toBeInTheDocument();
  });
});
