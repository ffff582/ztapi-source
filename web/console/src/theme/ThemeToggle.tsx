import { Moon, Sun } from 'lucide-react';
import { useLocale } from '../i18n/locale';
import { useTheme } from './theme';

export function ThemeToggle() {
  const { t } = useLocale();
  const { theme, setTheme } = useTheme();

  return (
    <div className="theme-toggle" role="group" aria-label={t('主题')}>
      <button
        type="button"
        aria-label={t('亮系')}
        title={t('切换为亮系')}
        aria-pressed={theme === 'light'}
        onClick={() => setTheme('light')}
      >
        <Sun size={13} aria-hidden="true" />
        <span>{t('亮系')}</span>
      </button>
      <button
        type="button"
        aria-label={t('暗系')}
        title={t('切换为暗系')}
        aria-pressed={theme === 'dark'}
        onClick={() => setTheme('dark')}
      >
        <Moon size={13} aria-hidden="true" />
        <span>{t('暗系')}</span>
      </button>
    </div>
  );
}
