import { RefreshCw } from 'lucide-react';
import { useState } from 'react';
import { useLocale } from '../../i18n/locale';

export function SpendingReauthenticate() {
  const { t } = useLocale();
  const [reloading, setReloading] = useState(false);
  function reload() {
    if (reloading || !window.confirm(t('将重新加载当前页面以重新确认主登录。未保存的修改或未完成的操作可能丢失，是否继续？'))) return;
    setReloading(true);
    window.location.reload();
  }
  return <div className="spending-warning spending-session-notice" role="alert">
    <p>{t('消费预警登录凭证已过期或未被接受，主会话未退出。')}</p>
    <button className="console-button console-button--secondary" type="button" disabled={reloading} aria-busy={reloading} onClick={reload}>
      <RefreshCw size={16} aria-hidden="true" />{t('重新确认登录并刷新页面')}
    </button>
  </div>;
}
