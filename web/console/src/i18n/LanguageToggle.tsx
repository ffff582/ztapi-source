import { useLocale } from './locale';

type LanguageToggleProps = {
  tone?: 'dark' | 'light';
};

export function LanguageToggle({ tone = 'light' }: LanguageToggleProps) {
  const { locale, setLocale, t } = useLocale();

  return (
    <div className={`language-toggle language-toggle--${tone}`} role="group" aria-label={t('语言')}>
      <button
        aria-label={t('切换到中文')}
        aria-pressed={locale === 'zh-CN'}
        type="button"
        onClick={() => setLocale('zh-CN')}
      >
        中
      </button>
      <span aria-hidden="true">/</span>
      <button
        aria-label={t('切换到英文')}
        aria-pressed={locale === 'en'}
        type="button"
        onClick={() => setLocale('en')}
      >
        EN
      </button>
    </div>
  );
}
