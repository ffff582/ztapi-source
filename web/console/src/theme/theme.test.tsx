import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { LocaleProvider } from '../i18n/locale';
import { ThemeProvider, useTheme } from './theme';
import { ThemeToggle } from './ThemeToggle';

function ThemeProbe() {
  const { theme, setTheme } = useTheme();
  return (
    <>
      <output data-testid="theme-value">{theme}</output>
      <button type="button" onClick={() => setTheme('light')}>
        set-light
      </button>
    </>
  );
}

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.style.removeProperty('color-scheme');
});

describe('theme system', () => {
  it('defaults to dark and persists a light-theme selection', () => {
    render(
      <ThemeProvider>
        <ThemeProbe />
      </ThemeProvider>,
    );

    expect(screen.getByTestId('theme-value')).toHaveTextContent('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');

    fireEvent.click(screen.getByRole('button', { name: 'set-light' }));

    expect(screen.getByTestId('theme-value')).toHaveTextContent('light');
    expect(document.documentElement).toHaveAttribute('data-theme', 'light');
    expect(document.documentElement.style.colorScheme).toBe('light');
    expect(localStorage.getItem('ztapi.theme')).toBe('light');
  });

  it('lets users switch between light and dark themes with labeled controls', () => {
    render(
      <LocaleProvider>
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      </LocaleProvider>,
    );

    expect(screen.getByRole('group', { name: '主题' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '亮系' })).toHaveAttribute(
      'aria-pressed',
      'false',
    );

    fireEvent.click(screen.getByRole('button', { name: '亮系' }));

    expect(document.documentElement).toHaveAttribute('data-theme', 'light');
    expect(document.documentElement.style.colorScheme).toBe('light');
    expect(localStorage.getItem('ztapi.theme')).toBe('light');
    expect(screen.getByRole('button', { name: '亮系' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });
});
