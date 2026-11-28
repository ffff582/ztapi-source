import { ArrowUpRight, Info } from 'lucide-react';
import { Link } from 'react-router-dom';
import { useLocale } from '../i18n/locale';
import './resource-disclosure.css';

export function ResourceDisclosureNotice() {
  const { t } = useLocale();
  return (
    <div className="resource-disclosure" role="note" aria-label={t('资源来源说明')}>
      <div className="resource-disclosure__inner public-shell">
        <Info aria-hidden="true" size={18} />
        <p>{t('部分模型使用上游反代资源，并非全部为原厂 API 直连')}</p>
        <Link to="/docs/resource-disclosure">
          {t('了解资源来源与价格说明')}<ArrowUpRight aria-hidden="true" size={16} />
        </Link>
      </div>
    </div>
  );
}
