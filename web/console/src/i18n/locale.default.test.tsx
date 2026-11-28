import { render, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { LocaleProvider, useLocale } from './locale';

function Label() { const { t } = useLocale(); return <h1>{t('推广返利')}</h1>; }

it('defaults to Chinese without a saved preference, even on an English browser', () => {
  vi.spyOn(navigator, 'language', 'get').mockReturnValue('en-US');
  render(<LocaleProvider><Label /></LocaleProvider>);
  expect(screen.getByRole('heading', { name: '推广返利' })).toBeVisible();
});
