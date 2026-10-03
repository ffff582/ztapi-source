import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { PublicHeader } from './PublicHeader';
import { LocaleProvider } from '../../i18n/locale';
import { ThemeProvider } from '../../theme/theme';

describe('PublicHeader shared controls', () => {
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
